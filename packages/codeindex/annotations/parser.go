package annotations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Kinds is the closed enum of recognised @atlas:<kind> values.
//
// Per docs/annotations.md §Forward compatibility, adding a new kind here is
// non-breaking: older Atlas versions encounter the unknown kind, emit a
// one-time advisory warning, and skip the annotation.
var Kinds = map[string]shared.AnnotationKind{
	"feature":    shared.AnnFeature,
	"contract":   shared.AnnContract,
	"owner":      shared.AnnOwner,
	"deprecated": shared.AnnDeprecated,
	"since":      shared.AnnSince,

	// EDA-pattern kinds (Phase 6e). Same canonical grammar
	// (@atlas:<kind> <id> [#tags...] [key=value...]). The id is
	// regex-validated for every EDA kind — these are NOT free-form like
	// owner/since/deprecated.
	"bc":                shared.AnnBC,
	"aggregate":         shared.AnnAggregate,
	"aggregate-service": shared.AnnAggregateService,
	"saga":              shared.AnnSaga,
	"consumer":          shared.AnnConsumer,
	"event-emit":        shared.AnnEventEmit,
	"outbox-publish":    shared.AnnOutboxPublish,
}

// edaStrictIDKinds are the EDA kinds whose ids must match idValidationRe.
// Identical handling to feature/contract — bare lower-case dot-namespaced ids.
var edaStrictIDKinds = map[shared.AnnotationKind]bool{
	shared.AnnBC:               true,
	shared.AnnAggregate:        true,
	shared.AnnAggregateService: true,
	shared.AnnSaga:             true,
	shared.AnnConsumer:         true,
	shared.AnnEventEmit:        true,
	shared.AnnOutboxPublish:    true,
}

// sagaStepTagRe matches a well-formed `step=<N>` tag. Non-numeric step values
// like `step=two` or empty `step=` are rejected at parse time so the saga
// walk query never has to defend against them.
var sagaStepTagRe = regexp.MustCompile(`^step=([0-9]+)$`)

// idValidationRe is the case-sensitive ID grammar per docs/annotations.md.
//
// Only applies to NEW-grammar (@atlas:...) annotations. Legacy @testreg
// annotations are intentionally NOT re-validated to preserve the existing
// 1,110 annotations in nutrition-v2-go untouched.
var idValidationRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

var (
	// New canonical grammar: `@atlas:<kind> <payload>`.
	atlasAnnotationRe = regexp.MustCompile(`@atlas:([a-zA-Z][a-zA-Z0-9_-]*)\s+(.+?)\s*$`)
	// Legacy grammar: `@testreg <payload>` (still recognised indefinitely).
	testregAnnotationRe = regexp.MustCompile(`@testreg\s+(.+?)\s*$`)
	// `@api METHOD /path` — orthogonal to @atlas:<kind>; used for handler
	// discovery in Go source files.
	apiAnnotationRe = regexp.MustCompile(`@api\s+(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+(\S+)`)
)

// Parse reads filePath, detects its language by extension, and returns the
// list of shared.Annotation records the parser found.
//
// filePath SHOULD be the **repo-relative** path used in shared.FilePosition
// records. If you only have an absolute path, pass it as both arguments —
// the parser only uses filePath for I/O and the relative form in
// FilePosition.Path; pass them separately via ParseRelative.
//
// Returns an empty slice for unsupported file types (no error). Errors are
// returned only for I/O failures and grammar-rejected lines.
func Parse(ctx context.Context, filePath string) ([]shared.Annotation, error) {
	return ParseRelative(ctx, filePath, filePath)
}

// ParseRelative is Parse with separate absolute (for reading) and
// repo-relative (for FilePosition.Path) paths.
func ParseRelative(ctx context.Context, absPath, relPath string) ([]shared.Annotation, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(absPath))
	style := commentStyleFor(ext)
	if style == styleUnsupported {
		return nil, nil
	}

	// Read the whole file so we can unwrap block comments correctly.
	var buf bytes.Buffer
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		buf.Write(scanner.Bytes())
		buf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", absPath, err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return ParseBytes(relPath, buf.Bytes(), style), nil
}

// ParseBytes parses content as the given language style and returns
// annotation records keyed off relPath in their positions.
//
// Exported so unit tests and in-memory consumers can drive the parser
// without going through the filesystem. There is no ctx parameter — pure
// byte-in / record-out, nothing to cancel.
func ParseBytes(relPath string, content []byte, style CommentStyle) []shared.Annotation {
	if style == styleUnsupported {
		return nil
	}
	logicalLines := unwrapBlockComments(content, style)

	out := make([]shared.Annotation, 0, 8)
	for _, ll := range logicalLines {
		// Each logical line might carry an @atlas:, an @api, or a @testreg
		// match — at most one annotation total (docs/annotations.md §Parser
		// rules: "One annotation per comment line").
		if ann, ok := parseAtlasLine(ll, relPath); ok {
			out = append(out, ann)
			continue
		}
		if ann, ok := parseTestregLine(ll, relPath); ok {
			out = append(out, ann)
			continue
		}
		if ann, ok := parseAPILine(ll, relPath); ok {
			out = append(out, ann)
			continue
		}
	}
	return out
}

// parseAtlasLine matches the canonical `@atlas:<kind> <payload>` grammar.
//
// On match: returns the annotation and ok=true. If the kind is unknown,
// returns ok=false (the caller will not append it — per docs §Forward
// compatibility, unknown kinds are silently skipped at parse time; the
// resolution layer is what emits advisory warnings).
//
// On a malformed `@atlas:feature` (no IDs), returns ok=false. The error
// surface is deliberately lossy here — strict validation is the resolver's
// job; the parser's only job is "best effort extract".
func parseAtlasLine(ll logicalLine, relPath string) (shared.Annotation, bool) {
	m := atlasAnnotationRe.FindStringSubmatch(ll.text)
	if m == nil {
		return shared.Annotation{}, false
	}
	// Kind is case-sensitive — per docs/annotations.md §Parser ambiguity
	// `@atlas:Feature` is rejected ("unknown kind 'Feature' — did you mean
	// 'feature'?"). Map lookup is the enforcement; no ToLower here.
	kindStr := m[1]
	kind, ok := Kinds[kindStr]
	if !ok {
		return shared.Annotation{}, false
	}
	payload := strings.TrimSpace(m[2])
	ids, tags := splitIDsAndTags(payload, false)

	// `consumer` carries its identity entirely in the `stream=` tag — the
	// id slot is intentionally empty. Promote the stream value to the id
	// list so downstream queries (Store.ListConsumers) can index on it
	// uniformly. Every other kind requires at least one id.
	if kind == shared.AnnConsumer {
		streamVal, hasStream := findTagValue(tags, "stream")
		if !hasStream {
			// `@atlas:consumer` without `stream=<name>` is ill-formed.
			return shared.Annotation{}, false
		}
		if !idValidationRe.MatchString(streamVal) {
			return shared.Annotation{}, false
		}
		// Reject extra free-form ids — `@atlas:consumer x stream=y` is
		// ambiguous and not part of the grammar.
		if len(ids) > 0 {
			return shared.Annotation{}, false
		}
		ids = []string{streamVal}
	}

	if len(ids) == 0 {
		// `@atlas:feature` with no value is invalid grammar; skip.
		return shared.Annotation{}, false
	}

	// Strict ID validation applies to `feature` and `contract` plus all
	// EDA-pattern kinds (bc/aggregate/aggregate-service/saga/consumer/
	// event-emit/outbox-publish). `owner`, `deprecated`, and `since`
	// carry free-form values (team handles, version strings, date strings)
	// and intentionally bypass the regex.
	if kind == shared.AnnFeature || kind == shared.AnnContract || edaStrictIDKinds[kind] {
		for _, id := range ids {
			if !idValidationRe.MatchString(id) {
				return shared.Annotation{}, false
			}
		}
	}

	// Per-kind tag validation (only structural validators that the parser
	// MUST enforce — value-level semantics are still resolver-domain).
	if kind == shared.AnnSaga {
		if stepRaw, hasStep := findTagValue(tags, "step"); hasStep {
			if !sagaStepTagRe.MatchString("step=" + stepRaw) {
				// step= must be a non-negative integer; reject otherwise.
				return shared.Annotation{}, false
			}
		}
	}

	return shared.Annotation{
		Kind:     kind,
		IDs:      ids,
		Tags:     tags,
		Source:   shared.SourceAtlas,
		Position: shared.FilePosition{Path: relPath, Line: ll.lineNum},
		Raw:      payload,
	}, true
}

// parseTestregLine matches the legacy `@testreg <payload>` grammar.
//
// Per docs/annotations.md §Legacy reader, legacy IDs are NOT re-validated
// against the new regex — comma-separated IDs are tolerated and normalised
// to space-separated.
func parseTestregLine(ll logicalLine, relPath string) (shared.Annotation, bool) {
	m := testregAnnotationRe.FindStringSubmatch(ll.text)
	if m == nil {
		return shared.Annotation{}, false
	}
	payload := strings.TrimSpace(m[1])
	ids, tags := splitIDsAndTags(payload, true)
	if len(ids) == 0 {
		return shared.Annotation{}, false
	}
	return shared.Annotation{
		Kind:     shared.AnnFeature, // legacy maps to "feature"
		IDs:      ids,
		Tags:     tags,
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: relPath, Line: ll.lineNum},
		Raw:      payload,
	}, true
}

// parseAPILine matches the `@api METHOD /path` discovery grammar. Orthogonal
// to the @atlas:<kind> grammar — Go AST scanner uses it to link handler
// functions to their HTTP endpoints without a dedicated route parser.
func parseAPILine(ll logicalLine, relPath string) (shared.Annotation, bool) {
	m := apiAnnotationRe.FindStringSubmatch(ll.text)
	if m == nil {
		return shared.Annotation{}, false
	}
	return shared.Annotation{
		Kind:     shared.AnnAPI,
		Source:   shared.SourceAPI,
		Position: shared.FilePosition{Path: relPath, Line: ll.lineNum},
		Method:   m[1],
		Path:     m[2],
		Raw:      m[1] + " " + m[2],
	}, true
}

// splitIDsAndTags splits a whitespace-separated payload into IDs (bare
// tokens) and tags. Two tag shapes are accepted:
//
//   - `#tag`        — boolean tag (e.g. `#real`, `#flaky`)
//   - `key=value`   — key/value tag (e.g. `step=1`, `stream=meal_prep_events`)
//
// Tags must follow IDs. The first tag token (either shape) terminates the
// ID list (docs/annotations.md §Parser rules #4).
//
// allowCommaSplit=true honors legacy testreg semantics where IDs can be
// comma-separated; allowCommaSplit=false enforces the new strict
// whitespace-only rule.
func splitIDsAndTags(payload string, allowCommaSplit bool) (ids []string, tags []string) {
	fields := strings.Fields(payload)
	tagsStarted := false
	for _, f := range fields {
		if strings.HasPrefix(f, "#") || isKVTag(f) {
			tagsStarted = true
			tags = append(tags, f)
			continue
		}
		if tagsStarted {
			// IDs after tags are ill-formed; skip (docs §Parser rules #4).
			continue
		}
		if allowCommaSplit && strings.Contains(f, ",") {
			for _, part := range strings.Split(f, ",") {
				part = strings.TrimSpace(part)
				if part != "" {
					ids = append(ids, part)
				}
			}
			continue
		}
		ids = append(ids, f)
	}
	return ids, tags
}

// isKVTag reports whether token has the `key=value` shape used by EDA
// annotations (`step=1`, `stream=meal_prep_events`). It is intentionally
// permissive on the value side — `splitIDsAndTags` only needs to classify
// the token as "this is a tag, not an id"; per-kind tag validators run later.
func isKVTag(token string) bool {
	if token == "" {
		return false
	}
	if strings.HasPrefix(token, "#") {
		return false
	}
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		// No `=` at all, or `=value` with empty key — not a kv tag.
		return false
	}
	return true
}

// findTagValue returns the value of the first `key=value` tag matching the
// given key, or "" if none. Used by per-kind validators (saga step=, consumer
// stream=). Tag tokens must be in the form returned by splitIDsAndTags.
func findTagValue(tags []string, key string) (value string, ok bool) {
	prefix := key + "="
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			return t[len(prefix):], true
		}
	}
	return "", false
}
