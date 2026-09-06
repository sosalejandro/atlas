package sqlops

import "strings"

// The lexer exists because the alternative -- regular expressions over raw
// SQL -- gets wrong the two things that matter most here. A `?` inside a
// string literal is not a bind parameter, and a `LIMIT` inside a line comment
// is not a limit; both mistakes flow straight into an advisory that is simply
// false. Tokenising is a hundred lines and removes the whole class.
//
// This is NOT a SQL parser. It knows nothing about grammar, precedence or
// dialect semantics. It produces a flat token stream that statement.go walks
// with a clause state machine, which is enough to answer "what tables, what
// predicate columns, is it bounded" and nothing more.

type tokenKind int

const (
	tokIdent tokenKind = iota
	tokKeyword
	tokNumber
	tokString
	tokParam
	tokPunct
	tokStar
)

// sqlToken is one lexeme. val holds the keyword upper-cased, the identifier with
// its quoting stripped, and the punctuation verbatim; string literal bodies
// are dropped entirely, because nothing downstream reads them and keeping them
// would put user data into the token stream and from there into the database.
type sqlToken struct {
	kind tokenKind
	val  string
}

// sqlKeywords is the reserved-word set the clause walker switches on. It is
// deliberately small: a word absent from this set lexes as an identifier,
// which for an unknown dialect keyword is the harmless outcome -- it may be
// mistaken for a table alias, never for a LIMIT that is not there.
var sqlKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"FROM": true, "INTO": true, "WHERE": true, "JOIN": true, "INNER": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "OUTER": true, "CROSS": true,
	"LATERAL": true, "ON": true, "USING": true, "AND": true, "OR": true,
	"NOT": true, "IN": true, "IS": true, "NULL": true, "LIKE": true,
	"ILIKE": true, "BETWEEN": true, "ORDER": true, "BY": true, "GROUP": true,
	"HAVING": true, "LIMIT": true, "OFFSET": true, "FETCH": true, "AS": true,
	"VALUES": true, "SET": true, "RETURNING": true, "WITH": true,
	"RECURSIVE": true, "UNION": true, "EXCEPT": true, "INTERSECT": true,
	"ALL": true, "DISTINCT": true, "EXISTS": true, "CASE": true, "WHEN": true,
	"THEN": true, "ELSE": true, "END": true, "ASC": true, "DESC": true,
	"NULLS": true, "FIRST": true, "LAST": true, "ONLY": true, "ROWS": true,
	"ROW": true, "NEXT": true, "CREATE": true, "TABLE": true, "INDEX": true,
	"UNIQUE": true, "PRIMARY": true, "KEY": true, "CONSTRAINT": true,
	"REFERENCES": true, "FOREIGN": true, "IF": true, "CONFLICT": true,
	"DO": true, "NOTHING": true, "REPLACE": true, "OVER": true,
	"PARTITION": true, "WITHOUT": true, "ROWID": true, "DEFAULT": true,
	"CHECK": true, "COLLATE": true, "CASCADE": true, "AUTOINCREMENT": true,
	"TEMP": true, "TEMPORARY": true, "VIEW": true, "TRIGGER": true,
	"EXPLAIN": true, "PRAGMA": true, "TRUNCATE": true, "ALTER": true,
	"DROP": true, "MERGE": true, "CALL": true,
}

// comparisonOps are the punctuation operators a predicate can be built on.
var comparisonOps = map[string]bool{
	"=": true, "<": true, ">": true, "<=": true, ">=": true,
	"<>": true, "!=": true,
}

// keywordOps are the operators spelled as words. BETWEEN is included even
// though only its lower bound is captured -- an index advisory cares that the
// column is a range predicate, not what the bounds are.
var keywordOps = map[string]bool{
	"IN": true, "LIKE": true, "ILIKE": true, "IS": true, "BETWEEN": true,
}

// rangeOps are the subset that make a predicate a *range* rather than an
// equality -- the shape keyset pagination uses.
var rangeOps = map[string]bool{"<": true, ">": true, "<=": true, ">=": true}

// lex tokenises src. It never fails: an unterminated string or comment simply
// consumes the rest of the input, because a malformed fragment must degrade to
// "Atlas saw less than it hoped", never to a hard error that drops the
// operation from the inventory entirely.
func lex(src string) []sqlToken {
	var out []sqlToken
	for i := 0; i < len(src); {
		if next, ok := skipTrivia(src, i); ok {
			i = next
			continue
		}
		tok, next := scanToken(src, i)
		out = append(out, tok)
		i = next
	}
	return out
}

// scanToken reads exactly one token starting at a non-trivia byte.
func scanToken(src string, i int) (sqlToken, int) {
	if tok, next, ok := scanQuoted(src, i); ok {
		return tok, next
	}
	if tok, next, ok := scanParam(src, i); ok {
		return tok, next
	}
	if tok, next, ok := scanWord(src, i); ok {
		return tok, next
	}
	return scanPunct(src, i)
}

// skipTrivia advances past whitespace and comments, reporting whether it
// consumed anything.
func skipTrivia(src string, i int) (int, bool) {
	if isSpace(src[i]) {
		return i + 1, true
	}
	return skipComment(src, i)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// skipComment consumes the three comment spellings Atlas meets: `--` and `#`
// to end of line, and `/* */` blocks.
func skipComment(src string, i int) (int, bool) {
	if i+1 >= len(src) {
		return i, false
	}
	switch src[i] {
	case '-':
		if src[i+1] == '-' {
			return skipTo(src, i, "\n"), true
		}
	case '#':
		// MySQL line comment, guarded on a following space so a bare `#`
		// used as an operator elsewhere still lexes as punctuation.
		if src[i+1] == ' ' || src[i+1] == '#' {
			return skipTo(src, i, "\n"), true
		}
	case '/':
		if src[i+1] == '*' {
			return skipTo(src, i+2, "*/"), true
		}
	}
	return i, false
}

// scanQuoted handles string literals and every quoted-identifier spelling
// Atlas is likely to meet: "ansi", `mysql`, [tsql].
func scanQuoted(src string, i int) (sqlToken, int, bool) {
	switch c := src[i]; c {
	case '\'':
		return sqlToken{kind: tokString}, skipQuoted(src, i, '\''), true
	case '"', '`':
		end := skipQuoted(src, i, c)
		return sqlToken{kind: tokIdent, val: unquote(src[i:end], c)}, end, true
	case '[':
		end := skipTo(src, i+1, "]")
		return sqlToken{kind: tokIdent, val: strings.Trim(src[i:end], "[]")}, end, true
	}
	return sqlToken{}, i, false
}

// scanParam recognises the four bind-parameter spellings across dialects:
// $1 (Postgres), ? and ?1 (SQLite/MySQL), :name and @name (named).
//
// The `::` cast operator is explicitly excluded -- `id::text` would otherwise
// yield a phantom parameter named ":text" and inflate every Postgres query's
// parameter count.
func scanParam(src string, i int) (sqlToken, int, bool) {
	switch src[i] {
	case '?':
		tok, next := paramToken(src, i, isDigit)
		return tok, next, true
	case '$':
		if i+1 < len(src) && isDigit(src[i+1]) {
			tok, next := paramToken(src, i, isDigit)
			return tok, next, true
		}
	case ':', '@':
		if namedParamStart(src, i) {
			tok, next := paramToken(src, i, isIdentPart)
			return tok, next, true
		}
	}
	return sqlToken{}, i, false
}

// namedParamStart distinguishes `:name` from the `::` cast operator, which
// would otherwise yield a phantom parameter for every Postgres cast.
func namedParamStart(src string, i int) bool {
	if i+1 >= len(src) || !isIdentStart(src[i+1]) {
		return false
	}
	if src[i] != ':' {
		return true
	}
	return i == 0 || src[i-1] != ':'
}

// paramToken consumes the marker at i plus every following byte the predicate
// accepts.
func paramToken(src string, i int, accept func(byte) bool) (sqlToken, int) {
	j := i + 1
	for j < len(src) && accept(src[j]) {
		j++
	}
	return sqlToken{kind: tokParam, val: src[i:j]}, j
}

// scanWord handles bare identifiers, keywords and numeric literals.
func scanWord(src string, i int) (sqlToken, int, bool) {
	n := len(src)
	switch c := src[i]; {
	case isIdentStart(c):
		j := i
		for j < n && isIdentPart(src[j]) {
			j++
		}
		word := src[i:j]
		if upper := strings.ToUpper(word); sqlKeywords[upper] {
			return sqlToken{kind: tokKeyword, val: upper}, j, true
		}
		return sqlToken{kind: tokIdent, val: word}, j, true
	case isDigit(c):
		j := i
		for j < n && (isDigit(src[j]) || src[j] == '.') {
			j++
		}
		return sqlToken{kind: tokNumber, val: src[i:j]}, j, true
	}
	return sqlToken{}, i, false
}

// scanPunct is the fallback: two-character operators first, then a single
// byte, so `<=` never lexes as `<` followed by `=`.
func scanPunct(src string, i int) (sqlToken, int) {
	if src[i] == '*' {
		return sqlToken{kind: tokStar, val: "*"}, i + 1
	}
	if i+1 < len(src) {
		switch two := src[i : i+2]; two {
		case "<=", ">=", "<>", "!=", "||", "::", "->":
			return sqlToken{kind: tokPunct, val: two}, i + 2
		}
	}
	return sqlToken{kind: tokPunct, val: src[i : i+1]}, i + 1
}

// skipTo returns the index just past the first occurrence of term at or after
// i, or len(src) when term never appears.
func skipTo(src string, i int, term string) int {
	idx := strings.Index(src[i:], term)
	if idx < 0 {
		return len(src)
	}
	return i + idx + len(term)
}

// skipQuoted returns the index just past the closing q, treating a doubled
// quote as an escaped one (the SQL standard's escape, and the only one that
// works across every dialect Atlas is likely to meet). A backslash escape is
// honoured inside single quotes for MySQL's benefit.
func skipQuoted(src string, i int, q byte) int {
	for j := i + 1; j < len(src); {
		switch {
		case src[j] == '\\' && q == '\'' && j+1 < len(src):
			j += 2
		case src[j] == q && j+1 < len(src) && src[j+1] == q:
			j += 2
		case src[j] == q:
			return j + 1
		default:
			j++
		}
	}
	return len(src)
}

func unquote(s string, q byte) string {
	s = strings.Trim(s, string(q))
	return strings.ReplaceAll(s, string([]byte{q, q}), string(q))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }
