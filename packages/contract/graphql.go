package contract

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// extractGraphQL walks the project root for `.graphql` / `.graphqls` files
// and emits one KindGraphQL ContractDef per Mutation/Query/Subscription
// operation declared at the top level (or via `extend type Mutation {...}`
// blocks).
//
// When the project contains zero GraphQL files this pass returns no
// records and no warnings — the caller can detect "GraphQL is absent"
// purely by checking len(defs) at this kind.
//
// This pass is intentionally regex-based rather than running a full
// GraphQL schema-parser dependency. The legacy testreg parser does the
// same (internal/adapters/graphql_schema_parser.go); its shape is what
// we port here. Line numbers are tracked per-line so each operation
// surfaces a usable FilePosition.
func (e *Extractor) extractGraphQL(ctx context.Context) ([]ContractDef, []string) {
	var (
		defs  []ContractDef
		warns []string
	)
	if e.opts.ProjectRoot == "" {
		return defs, warns
	}
	err := filepath.WalkDir(e.opts.ProjectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".graphql" && ext != ".graphqls" {
			return nil
		}
		relPath := normaliseRelPath(e.opts.ProjectRoot, path)
		fileDefs, fileWarns := parseGraphQLFile(path, relPath)
		defs = append(defs, fileDefs...)
		warns = append(warns, fileWarns...)
		return nil
	})
	if err != nil {
		warns = append(warns, fmt.Sprintf("contract graphql: walk: %v", err))
	}
	return defs, warns
}

// graphqlParser is the per-file state machine driven by parseGraphQLFile.
type graphqlParser struct {
	relPath     string
	defs        []ContractDef
	blockType   string // "Mutation" | "Query" | "Subscription" | "" (not inside an op block)
	blockTarget string // "type:<name>" or ""
	inDocString bool
}

// parseGraphQLFile parses one .graphql / .graphqls file and returns a
// ContractDef per Mutation/Query/Subscription operation. Type definitions
// (input, type, scalar, enum) are NOT emitted as contracts here — they're
// supporting shapes, not operations.
func parseGraphQLFile(absPath, relPath string) ([]ContractDef, []string) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("contract graphql: open %s: %v", relPath, err)}
	}
	defer func() { _ = f.Close() }()

	p := &graphqlParser{relPath: relPath}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		p.handleLine(scanner.Text(), lineNum)
	}
	if err := scanner.Err(); err != nil {
		return p.defs, []string{fmt.Sprintf("contract graphql: scan %s: %v", relPath, err)}
	}
	return p.defs, nil
}

func (p *graphqlParser) handleLine(line string, lineNum int) {
	if reTripleQuote.MatchString(line) {
		p.inDocString = !p.inDocString
		return
	}
	if p.inDocString || reComment.MatchString(line) {
		return
	}
	if p.blockType == "" && p.blockTarget == "" {
		p.handleTopLevel(line)
		return
	}
	if p.blockType != "" {
		p.handleOpBlock(line, lineNum)
		return
	}
	// Inside a non-op type block — track close-brace only.
	if reBlockClose.MatchString(line) {
		p.blockTarget = ""
	}
}

func (p *graphqlParser) handleTopLevel(line string) {
	if m := reExtendOpType.FindStringSubmatch(line); m != nil {
		p.blockType = m[1]
		return
	}
	if m := reOpTypeStart.FindStringSubmatch(line); m != nil {
		p.blockType = m[2]
		return
	}
	if m := reTypeStart.FindStringSubmatch(line); m != nil {
		p.blockTarget = "type:" + m[2]
	}
}

func (p *graphqlParser) handleOpBlock(line string, lineNum int) {
	if reBlockClose.MatchString(line) {
		p.blockType = ""
		return
	}
	m := reField.FindStringSubmatch(line)
	if m == nil {
		return
	}
	fieldName := m[1]
	args := strings.TrimSpace(m[2])
	retType := strings.TrimSpace(m[3])
	p.defs = append(p.defs, ContractDef{
		Name:      p.blockType + "." + fieldName,
		Kind:      KindGraphQL,
		Language:  LangGraphQL,
		Signature: formatGraphQLSig(p.blockType, fieldName, args, retType),
		FilePath:  p.relPath,
		Line:      lineNum,
		Source:    "graphql",
		Operation: OperationDetail{
			GraphQLType: p.blockType,
			ReturnType:  retType,
		},
	})
}

// formatGraphQLSig produces a single-line readable signature for a GraphQL
// operation. Mirrors the shape used in the legacy testreg contract
// renderer so downstream tooling (existing dashboards, golden output) can
// continue to match on it.
func formatGraphQLSig(opType, fieldName, args, retType string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(opType[:1]))
	b.WriteString(opType[1:])
	b.WriteString(" { ")
	b.WriteString(fieldName)
	if args != "" {
		b.WriteString("(")
		b.WriteString(args)
		b.WriteString(")")
	}
	b.WriteString(": ")
	b.WriteString(retType)
	b.WriteString(" }")
	return b.String()
}

// Regexes — small + fast; documented inline because they are the parser.
var (
	// "type Foo {" or "input Foo {" — top-level type definition.
	reTypeStart = regexp.MustCompile(`^\s*(type|input)\s+(\w+)\s*\{`)
	// "type Mutation {" / "type Query {" / "type Subscription {".
	reOpTypeStart = regexp.MustCompile(`^\s*(type)\s+(Mutation|Query|Subscription)\s*\{`)
	// "extend type Mutation {" — most common form in stitched schemas.
	reExtendOpType = regexp.MustCompile(`^\s*extend\s+type\s+(Mutation|Query|Subscription)\s*\{`)
	// "fieldName: TypeName!" or "fieldName(args): ReturnType!".
	reField = regexp.MustCompile(`^\s+(\w+)\s*(?:\(([^)]*)\))?\s*:\s*(.+?)\s*$`)
	// "}".
	reBlockClose = regexp.MustCompile(`^\s*\}`)
	// "#" comments, "..." spread directives, triple-quote markers.
	reComment = regexp.MustCompile(`^\s*(#|\.\.\.)`)
	// Block doc strings.
	reTripleQuote = regexp.MustCompile(`^\s*"""`)
)
