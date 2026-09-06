package mcp

import "fmt"

// Truncation is present on a result ONLY when rows were actually dropped.
//
// The alternative — always emitting it, with returned == total — makes the
// field meaningless: a reader (human or model) stops noticing a block that is
// always there. Its absence is therefore a positive statement that the list is
// complete.
type Truncation struct {
	Returned int    `json:"returned"`
	Total    int    `json:"total"`
	Limit    int    `json:"limit"`
	Note     string `json:"note"`
}

// NoData is the structured "atlas cannot answer this yet, and here is the
// command that would let it".
//
// It replaces the list rather than accompanying it. An agent handed
// `{"features": [], "no_data": {...}}` reads the empty array first and
// concludes there are no features; omitting the array leaves nothing to
// misread.
type NoData struct {
	// Reason is a stable machine-readable slug, so a client can branch on the
	// kind of gap without parsing prose.
	Reason string `json:"reason"`
	// Detail says what is missing in the caller's own terms.
	Detail string `json:"detail"`
	// Run is the atlas command that produces the missing data.
	Run string `json:"run"`
}

// Reason slugs. Each names a DIFFERENT gap with a different fix, which is the
// whole point of having more than one: "no symbols indexed" and "no per-test
// coverage" both look like an empty answer and are repaired by unrelated
// commands.
const (
	ReasonNoIndex        = "index-empty"
	ReasonNoFeatureLinks = "feature-has-no-linked-symbols"
	ReasonNoCoverage     = "no-coverage-frontier"
	ReasonNoPerTest      = "no-per-test-evidence"
)

func noIndex() *NoData {
	return &NoData{
		Reason: ReasonNoIndex,
		Detail: "the atlas store holds no symbols: this repository has not been scanned yet, so an empty answer here says nothing about the code",
		Run:    "atlas init  (first scan)  or  atlas scan  (incremental re-scan)",
	}
}

func noCoverage() *NoData {
	return &NoData{
		Reason: ReasonNoCoverage,
		Detail: "no coverage run has been ingested, so atlas knows nothing about what executes; this is not the same as nothing being covered",
		Run:    "atlas cov sync --framework go-cover --input coverage.out",
	}
}

func noPerTestEvidence() *NoData {
	return &NoData{
		Reason: ReasonNoPerTest,
		Detail: "the current coverage frontier records THAT symbols ran, not WHICH test ran them; per-test attribution needs a per-test ingest",
		Run:    "atlas cov sync --framework go-cover --per-test <dir of per-test coverprofiles>",
	}
}

func noFeatureLinks(id string) *NoData {
	return &NoData{
		Reason: ReasonNoFeatureLinks,
		Detail: fmt.Sprintf("feature %q exists but no symbol is annotated for it, so atlas has no implementation to name", id),
		Run:    "annotate the implementation with @atlas:feature " + id + ", then run atlas scan",
	}
}

// bound cuts a result set to `limit` and reports what it dropped.
//
// The Truncation return is nil when nothing was cut. Callers must propagate it
// verbatim: an agent that receives a partial callers() list without knowing it
// is partial will conclude a symbol has no other callers and delete it.
func bound[T any](rows []T, limit int, what string) ([]T, *Truncation) {
	if limit <= 0 || len(rows) <= limit {
		return rows, nil
	}
	return rows[:limit], &Truncation{
		Returned: limit,
		Total:    len(rows),
		Limit:    limit,
		Note:     truncationNote(limit, len(rows), what),
	}
}

// truncationNote tells the model what it can actually DO about the cut.
//
// This surface has no pagination: every inputSchema is
// additionalProperties:false with no cursor, offset or page token, so an
// instruction to "fetch the next page" — or to raise `limit` past a cap the
// caller has already hit — is an instruction the protocol cannot satisfy, and
// the model burns its turn discovering that. Saying plainly that the remainder
// is unreachable from here, and naming the two things that ARE reachable
// (a narrower question, or the uncapped CLI), is the honest version.
func truncationNote(limit, total int, what string) string {
	return fmt.Sprintf(
		"TRUNCATED: showing %d of %d %s. The remaining %d are NOT in this response — do not conclude they "+
			"do not exist. There is NO cursor and no offset: calling this tool again cannot retrieve them, and "+
			"`limit` may only narrow the server cap, never exceed it. Ask a narrower question, or read the "+
			"complete set outside MCP with the atlas CLI (e.g. `atlas trace --json` for call edges). The server "+
			"cap itself is set by the operator with `atlas mcp --max-features/--max-symbols/--max-edges/--max-tests`.",
		limit, total, what, total-limit)
}
