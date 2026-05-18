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
	defer func() { _ = f.Close() }()

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
		return nil, fmt.Errorf("parse %s: %w", absPath, err)
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
	kind, ok := Kinds[m[1]]
	if !ok {
		return shared.Annotation{}, false
	}
	payload := strings.TrimSpace(m[2])
	ids, tags := splitIDsAndTags(payload, false)

	ids, ok = resolveConsumerIDs(kind, ids, tags)
	if !ok {
		return shared.Annotation{}, false
	}
	if len(ids) == 0 {
		return shared.Annotation{}, false
	}
	if !validateAtlasIDs(kind, ids) {
		return shared.Annotation{}, false
	}
	if !validateAtlasTags(kind, tags) {
		return shared.Annotation{}, false
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

// resolveConsumerIDs handles the `consumer` kind's stream= → IDs promotion.
//
// `@atlas:consumer` carries its identity in the `stream=<name>` tag rather
// than the id slot. This helper:
//
//   - leaves non-consumer kinds untouched
//   - rejects `@atlas:consumer` without a stream= tag
//   - rejects `@atlas:consumer x stream=y` (extra free-form id is ambiguous)
//   - rejects a non-idValidationRe-matching stream value
//   - returns ids = [streamVal] on success
//
// ok=false means "skip this annotation; ill-formed".
func resolveConsumerIDs(kind shared.AnnotationKind, ids, tags []string) ([]string, bool) {
	if kind != shared.AnnConsumer {
		return ids, true
	}
	streamVal, hasStream := findTagValue(tags, "stream")
	if !hasStream {
		return nil, false
	}
	if !idValidationRe.MatchString(streamVal) {
		return nil, false
	}
	if len(ids) > 0 {
		return nil, false
	}
	return []string{streamVal}, true
}

// validateAtlasIDs enforces the strict id-grammar regex for the kinds that
// require it (feature, contract, all EDA-pattern kinds). Free-form kinds
// (owner, deprecated, since) bypass.
//
// Returns false on first invalid id; the caller treats that as "skip".
func validateAtlasIDs(kind shared.AnnotationKind, ids []string) bool {
	if kind != shared.AnnFeature && kind != shared.AnnContract && !edaStrictIDKinds[kind] {
		return true
	}
	for _, id := range ids {
		if !idValidationRe.MatchString(id) {
			return false
		}
	}
	return true
}

// validateAtlasTags enforces per-kind structural rules on tags. Currently
// only `saga` carries a structural rule: `step=N` must be a non-negative
// integer. Returns false to mean "skip; ill-formed".
func validateAtlasTags(kind shared.AnnotationKind, tags []string) bool {
	if kind != shared.AnnSaga {
		return true
	}
	stepRaw, hasStep := findTagValue(tags, "step")
	if !hasStep {
		return true
	}
	return sagaStepTagRe.MatchString("step=" + stepRaw)
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
