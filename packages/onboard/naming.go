package onboard

import (
	"strings"
	"unicode"

	"github.com/sosalejandro/atlas/packages/shared"
)

// validID reports whether an inferred id could actually be promoted into an
// annotation. Every naming rule in this file runs its output through it, and
// the inference pass drops anything that fails: a proposal a user cannot
// accept is worse than no proposal, because it is only discovered at promote
// time, on their repository, after they have already agreed to it.
func validID(id string) bool { return shared.IsValidFeatureID(shared.FeatureID(id)) }

// structuralSegments are directory names that describe a repository's LAYOUT
// rather than its subject matter. "packages/store" is a capability called
// store that happens to live under packages/; "billing/checkout" is a
// capability called checkout inside the billing domain. Skipping these when
// choosing the domain half of an id is the difference between forty
// capabilities all in a domain called "packages" and forty capabilities in
// the domains their authors named.
var structuralSegments = map[string]bool{
	"src": true, "lib": true, "pkg": true, "app": true, "apps": true,
	"modules": true, "services": true, "components": true,
}

// capabilityIDFromDir names a provisional capability after the directory it
// lives in, returning the promotable id and the short display name.
//
// The id is always exactly two segments -- "<domain>.<name>" -- because a
// feature id needs at least one dot to be valid and more than two segments
// buys nothing a reader can use. The domain is the nearest ancestor
// directory that names a subject rather than a layout; when there is no
// ancestor at all (a top-level directory, or the repository root) it is
// "root", which reads honestly as "atlas had nothing to qualify this with".
//
// Directory is the grouping of last resort, used for the code that no
// stronger signal claimed. It is the weakest proposal in the report and is
// labelled as such, but it is never wrong in the way a guess can be wrong:
// it states a fact about the tree, and the user is the one who decides
// whether that fact is a capability.
func capabilityIDFromDir(dir string) (id, name string) {
	segs := splitPath(dir)
	if len(segs) == 0 {
		return "root.root", "root"
	}
	name = sanitizeSegment(segs[len(segs)-1])
	if name == "" {
		return "root.root", "root"
	}
	domain := "root"
	for i := len(segs) - 2; i >= 0; i-- {
		s := sanitizeSegment(segs[i])
		if s == "" || structuralSegments[s] {
			continue
		}
		domain = s
		break
	}
	// A directory whose only ancestor is structural still needs a domain,
	// and the structural name is a truer answer than "root": "src.parser"
	// tells the reader where to look, "root.parser" does not.
	if domain == "root" && len(segs) >= 2 {
		if s := sanitizeSegment(segs[len(segs)-2]); s != "" {
			domain = s
		}
	}
	return domain + "." + name, name
}

// routeVerbs maps an HTTP method onto the verb half of a capability id.
//
// GET is the one method that needs the path to disambiguate: a collection
// GET is a list and an item GET is a read, and calling both "read" would
// merge two capabilities that fail, page and get authorised differently.
var routeVerbs = map[string]string{
	"POST": "create", "PUT": "replace", "PATCH": "update",
	"DELETE": "delete", "HEAD": "head", "OPTIONS": "options",
}

// capabilityIDFromRoute turns one route registration into a capability id of
// the form "<resource>.<verb>", e.g. POST /measurements -> measurements.create.
//
// An HTTP surface is the most legible capability list a newcomer can be
// handed, because it is the one description of the system that its own
// clients already agree with. The resource is the last non-parameter segment
// -- for GET /users/{id}/sessions that is "sessions", which is what the
// request is actually about -- and version and prefix segments are dropped
// because "v1" names a release, not a capability.
func capabilityIDFromRoute(method, path string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	segs := splitPath(path)
	resource := ""
	lastIsParam := false
	for _, raw := range segs {
		if isPathParam(raw) {
			lastIsParam = true
			continue
		}
		s := sanitizeSegment(raw)
		if s == "" || isPrefixSegment(s) {
			continue
		}
		resource = s
		lastIsParam = false
	}
	if resource == "" {
		resource = "root"
	}
	verb, ok := routeVerbs[method]
	switch {
	case ok:
	case method == "":
		// A registration with no method serves every method -- the stdlib
		// mux shape. "handle" says that; the empty string would collapse the
		// id to a single segment and make the proposal unpromotable.
		verb = "handle"
	case method == "GET" && lastIsParam:
		verb = "read"
	case method == "GET":
		verb = "list"
	default:
		// An unrecognised method is still a real registration. Naming it
		// after the method keeps the proposal promotable and keeps the
		// oddity visible instead of quietly folding it into "read".
		verb = sanitizeSegment(method)
		if verb == "" {
			verb = "call"
		}
	}
	return resource + "." + verb
}

// isPathParam reports whether a route segment is a placeholder rather than a
// name: {id}, :id and * across the router libraries atlas reads.
func isPathParam(seg string) bool {
	if seg == "" {
		return false
	}
	return strings.HasPrefix(seg, "{") || strings.HasPrefix(seg, ":") ||
		strings.HasPrefix(seg, "*") || strings.HasPrefix(seg, "<")
}

// isPrefixSegment reports whether a path segment is mount plumbing rather
// than a resource. Every API has one of these and none of them is a
// capability.
func isPrefixSegment(seg string) bool {
	if seg == "api" || seg == "rest" || seg == "graphql" {
		return true
	}
	// v1, v2, v10 -- a version names a release, not a capability.
	if len(seg) >= 2 && seg[0] == 'v' {
		for _, r := range seg[1:] {
			if !unicode.IsDigit(r) {
				return false
			}
		}
		return true
	}
	return false
}

// testStopWords are leading test-name words that describe the SHAPE of a
// test rather than the capability under test. Clustering on them would
// propose a capability called "new" holding every constructor test in the
// package, which is a grouping nobody would accept.
var testStopWords = map[string]bool{
	"new": true, "get": true, "set": true, "add": true, "is": true,
	"has": true, "with": true, "when": true, "should": true, "must": true,
	"parse": true, "run": true, "do": true, "make": true, "build": true,
	"nil": true, "empty": true, "error": true, "errors": true, "ok": true,
	"the": true, "a": true, "it": true, "table": true, "helper": true,
}

// testNameCluster extracts the capability word a Go test function name
// leads with: TestCheckoutIdempotent -> "checkout". Returns "" when the
// name carries no such word.
//
// The first word after the Test prefix is the subject; everything after it
// is the scenario. That convention is near-universal in Go and it is the
// only part of a test name that groups: two tests whose names begin with
// the same word are about the same thing, and two tests that merely share a
// later word usually are not.
//
// A leading all-caps acronym is joined to the word after it, because "HTTP"
// alone names a protocol rather than a capability while "httproute" names
// the thing the test is actually about.
func testNameCluster(fn string) string {
	rest, ok := strings.CutPrefix(fn, "Test")
	if !ok {
		return ""
	}
	rest = strings.TrimLeft(rest, "_")
	words := splitCamel(rest)
	if len(words) == 0 {
		return ""
	}
	first := strings.ToLower(words[0])
	if isAcronym(words[0]) && len(words) > 1 {
		first += strings.ToLower(words[1])
	}
	if testStopWords[first] || len(first) < 2 {
		return ""
	}
	return first
}

// isAcronym reports whether a word is two or more consecutive capitals
// (HTTP, SQL, API) -- the shape splitCamel keeps together.
func isAcronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, r := range w {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// splitCamel splits a CamelCase or snake_case identifier into words,
// keeping runs of capitals together as one acronym word.
func splitCamel(s string) []string {
	var (
		out  []string
		curr []rune
	)
	flush := func() {
		if len(curr) > 0 {
			out = append(out, string(curr))
			curr = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if r == '_' || r == '-' || r == ' ' {
			flush()
			continue
		}
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			// Start a new word at a lower->upper transition (userID) and at
			// the tail of an acronym run (HTTPRoute -> HTTP | Route).
			if !unicode.IsUpper(prev) || (next != 0 && unicode.IsLower(next)) {
				flush()
			}
		}
		curr = append(curr, r)
	}
	flush()
	return out
}

// splitPath splits a slash path into non-empty segments, tolerating
// Windows separators and leading/trailing slashes.
func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

// sanitizeSegment lowercases one id segment and reduces it to the character
// set a feature id accepts, collapsing runs of rejected characters into a
// single dash so "Feature Flags" becomes "feature-flags" rather than
// "featureflags".
func sanitizeSegment(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteRune('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
