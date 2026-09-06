package doctor

import (
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// Default thresholds. They are package constants rather than flags
// because a threshold a user has to choose is a threshold nobody sets --
// the point of `atlas doctor` is that it works with no arguments. A team
// that wants a different gate tightens --fail-on instead, which changes
// which severities matter without changing what "stale" means.
const (
	// DefaultCoverageMaxAge is how old the coverage frontier may get
	// before it stops describing the code people are writing. A week is
	// one sprint's worth of drift: long enough that a repo whose CI only
	// syncs on release is not nagged daily, short enough that a coverage
	// number nobody has refreshed since last month is called out.
	DefaultCoverageMaxAge = 7 * 24 * time.Hour

	// DefaultUnattributedWarn / DefaultUnattributedFail are the fraction
	// of executed statements the ingest could not charge to any symbol.
	// A blind spot understates every coverage number that follows it
	// (issues #85 / #100), so it is scored on the size of the lie: a tenth
	// of the report unattributed is worth flagging, a third of it means
	// the coverage figures are not about this repo.
	DefaultUnattributedWarn = 0.10
	DefaultUnattributedFail = 0.33
)

// Env is everything the checks read. Constructed by the CLI, passed
// unchanged to every check.
//
// Store is allowed to be nil. That is not an oversight: the single most
// useful moment for `atlas doctor` is when the store will not open at
// all, which is exactly when every other atlas command dies in a wall of
// golang-migrate output. With Store nil and StoreErr set, the schema
// check still reports the store's recorded (version, dirty) state from
// the database file directly, and every other check reports
// not-applicable naming the open failure as the reason.
type Env struct {
	// Store is the opened state DB, or nil when it could not be opened.
	Store *store.Store

	// StoreErr is why Store is nil. Ignored when Store is non-nil.
	StoreErr error

	// DBPath is the state DB's path on disk. Required even when Store is
	// nil -- the schema check reads the file directly.
	DBPath string

	// Root is the working tree the index is compared against. Every
	// file_hashes path is relative to it.
	Root string

	// SkipDirs are directory names the scan excludes. doctor must honour
	// the same list or it would report every vendored file as unindexed,
	// and a doctor that cries wolf is a doctor nobody runs.
	SkipDirs []string

	// GeneratedGlobs mirrors the scan.generated config globs, for the
	// same reason: files the scanner excluded by policy are not evidence
	// of a stale index.
	GeneratedGlobs []string

	// Now is the clock, injectable so the age-based checks are testable
	// without sleeping.
	Now func() time.Time

	// CoverageMaxAge, UnattributedWarn and UnattributedFail default to
	// the Default* constants above when left at zero.
	CoverageMaxAge   time.Duration
	UnattributedWarn float64
	UnattributedFail float64

	// probe is the read-only SQL handle (see probe.go), opened by Run and
	// closed when it returns. probeErr is why it is nil.
	probe    *probe
	probeErr error
}

// withDefaults returns a copy with the zero-valued knobs filled in, so a
// caller can construct an Env with two fields set and still get sane
// behaviour. A copy rather than in-place mutation keeps Run free of
// side effects on the caller's value.
func (e *Env) withDefaults() *Env {
	out := *e
	if out.Now == nil {
		out.Now = func() time.Time { return time.Now().UTC() }
	}
	if out.CoverageMaxAge <= 0 {
		out.CoverageMaxAge = DefaultCoverageMaxAge
	}
	if out.UnattributedWarn <= 0 {
		out.UnattributedWarn = DefaultUnattributedWarn
	}
	if out.UnattributedFail <= 0 {
		out.UnattributedFail = DefaultUnattributedFail
	}
	if out.Root == "" {
		out.Root = "."
	}
	return &out
}

// requireStore is the guard every store-backed check opens with. It
// returns a not-applicable Result naming the open failure when there is
// no store to read.
//
// Not-applicable rather than fail is right here even though a store that
// will not open is a genuine problem: the schema check reports that
// problem as a fail once, with the actual error, and repeating it as
// four more failures would bury the one line that says what to do.
func (e *Env) requireStore() (Result, bool) {
	if e.Store != nil {
		return Result{}, true
	}
	// The reason itself is deliberately NOT repeated here. Every
	// store-backed check would print the same golang-migrate sentence,
	// and four copies of it would bury the one line -- the schema check's
	// -- that says what to do about it.
	return Result{
		Severity: SeverityNotApplicable,
		Finding: fmt.Sprintf(
			"the state database at %s could not be opened, so there is nothing to examine "+
				"(the store.schema check carries the reason)", e.DBPath),
		Remediation: "atlas init",
	}, false
}
