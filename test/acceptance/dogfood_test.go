//go:build dogfood

// Atlas gating atlas.
//
// Behind a build tag because it needs a coverprofile for this whole
// repository, and producing one means running the suite that would be running
// it. run.sh resolves that: CI's existing `go test` step already writes a
// profile, and the runner passes it in. See docs/testing/strategy.md.
//
// What this layer is for: every other test in the repo asserts that some
// function behaves. These run the SHIPPED commands against the SHIPPED repo
// and check the answers. That is a different kind of evidence — the subject
// and the instrument are the same artefact, so a claim that survives here has
// survived the only reviewer who does not have to trust the test suite.
//
// On thresholds. Where a command can gate honestly it gates. Where it cannot
// yet, the current value is recorded as a baseline with the measurement that
// produced it, and the assertion is "no worse than this" rather than a target
// nobody has met. Ratchet the baselines upward in a PR that says why; never
// downward without one.

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Baselines. Every number here was MEASURED, and the comment says how.
//
// Measurement run: this worktree at integrate/m3-batch (7e9ccc2), Go 1.26.4,
// linux/amd64, using a coverprofile from
//
//	go test ./packages/... ./internal/... -coverprofile=... \
//	        -coverpkg=./packages/...,./internal/...
//
// Re-measure with `ATLAS_DOGFOOD_KEEP=1 ./test/acceptance/run.sh -v`, which
// prints every observed value next to its baseline.
const (
	// minAttributedFraction is the share of executed statements the ingest
	// charges to a symbol. Observed 22396 of 23165 = 0.9668.
	//
	// The floor is set below the observation rather than at it because the
	// figure moves with the code: a new generated file, or a package whose
	// symbols the scanner cannot yet name, lowers it without anything being
	// broken. What must never happen is a quiet slide — the remaining ~3.4%
	// is dominated by packages/store/sqlc, which is generated and excluded
	// from the index by design.
	minAttributedFraction = 0.95

	// minSQLResolvedFraction is the share of SQL operations `atlas sql`
	// resolves to a known table set. Observed 128 of 129 = 0.9922.
	//
	// This one is gated close to the observation because it is a property of
	// queries this repo authors: an unresolvable operation is a query atlas
	// cannot advise on, and adding one should be a deliberate act.
	minSQLResolvedFraction = 0.97

	// minSymbols / minEdges are floor checks on the scan itself. They are
	// deliberately far below the observation (4,565 symbols and 10,433 edges
	// at the time of writing) because their job is to catch a scan that indexed
	// almost nothing — the failure where every downstream number is
	// technically correct about an empty repo. A tight bound here would fail
	// on every ordinary week.
	minSymbols = 2000
	minEdges   = 2000
)

// dogfoodEnv is the runner's contract with this file.
type dogfoodEnv struct {
	repoRoot string
	db       string
	profile  string
	base     string
}

func setupDogfood(t *testing.T) dogfoodEnv {
	t.Helper()
	profile := os.Getenv("ATLAS_DOGFOOD_PROFILE")
	if profile == "" {
		t.Fatal("ATLAS_DOGFOOD_PROFILE is unset. This layer needs a coverprofile for " +
			"the whole repo; run it through ./test/acceptance/run.sh, which either " +
			"reuses CI's profile or generates one.")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("ATLAS_DOGFOOD_PROFILE=%s: %v", profile, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return dogfoodEnv{
		repoRoot: root,
		db:       filepath.Join(t.TempDir(), "atlas.db"),
		profile:  profile,
		base:     os.Getenv("ATLAS_DOGFOOD_BASE"),
	}
}

// TestDogfood_AtlasMeasuresItself is the whole gate, in the order a user would
// run it: scan, sync, doctor, sql, cov diff.
//
// One test because the steps share a database and a scan of this repo takes
// seconds; splitting them would either re-scan four times or share mutable
// state between tests, and a suite that depends on its own ordering is worse
// than a long test.
func TestDogfood_AtlasMeasuresItself(t *testing.T) {
	env := setupDogfood(t)

	t.Run("scan", func(t *testing.T) { dogfoodScan(t, env) })
	t.Run("cov-sync", func(t *testing.T) { dogfoodCovSync(t, env) })
	t.Run("doctor", func(t *testing.T) { dogfoodDoctor(t, env) })
	t.Run("sql", func(t *testing.T) { dogfoodSQL(t, env) })
	t.Run("cov-diff", func(t *testing.T) { dogfoodCovDiff(t, env) })
}

// dogfoodScan indexes this repo and checks the scan produced a real index.
func dogfoodScan(t *testing.T, env dogfoodEnv) {
	env2 := runAtlas(t, "scan", "--root", env.repoRoot, "--db-path", env.db)
	var res struct {
		SymbolsInserted int `json:"symbols_inserted"`
		EdgesInserted   int `json:"edges_inserted"`
		FilesScanned    int `json:"files_scanned"`
	}
	decodeResult(t, env2, &res)
	t.Logf("scan: %d symbols, %d edges, %d files (floors: %d / %d)",
		res.SymbolsInserted, res.EdgesInserted, res.FilesScanned, minSymbols, minEdges)

	if res.SymbolsInserted < minSymbols {
		t.Errorf("scan indexed %d symbols, floor is %d — the index is nearly empty and every "+
			"number downstream of it is about a repo that does not exist", res.SymbolsInserted, minSymbols)
	}
	if res.EdgesInserted < minEdges {
		t.Errorf("scan recorded %d edges, floor is %d", res.EdgesInserted, minEdges)
	}
}

// dogfoodCovSync ingests the repo's own coverprofile and gates the share of
// execution atlas can actually place.
//
// This is the number the whole product rests on. Coverage atlas cannot charge
// to a symbol is coverage it cannot charge to a FEATURE, so it silently
// shrinks every per-feature figure the audit reports (issues #85 / #100).
func dogfoodCovSync(t *testing.T, env dogfoodEnv) {
	res := runAtlas(t, "cov", "sync", "--framework", "go-cover",
		"--input", env.profile, "--db-path", env.db)
	var out struct {
		Attribution struct {
			FilesInProfile    int `json:"files_in_profile"`
			FilesMatched      int `json:"files_matched"`
			FilesUnmatched    int `json:"files_unmatched"`
			StmtsAttributed   int `json:"stmts_attributed"`
			StmtsUnattributed int `json:"stmts_unattributed"`
			Gaps              []struct {
				Path   string `json:"path"`
				Stmts  int    `json:"stmts"`
				Reason string `json:"reason"`
			} `json:"gaps"`
		} `json:"attribution"`
	}
	decodeResult(t, res, &out)

	a := out.Attribution
	total := a.StmtsAttributed + a.StmtsUnattributed
	if total == 0 {
		t.Fatal("the coverprofile accounted for zero statements; it is empty or did not reconcile at all")
	}
	frac := float64(a.StmtsAttributed) / float64(total)
	t.Logf("attribution: %d/%d statements charged to a symbol = %.4f (floor %.4f); "+
		"%d of %d profile files unmatched",
		a.StmtsAttributed, total, frac, minAttributedFraction, a.FilesUnmatched, a.FilesInProfile)
	for _, g := range a.Gaps {
		if g.Stmts >= 20 {
			t.Logf("  gap: %s — %d statements (%s)", g.Path, g.Stmts, g.Reason)
		}
	}

	if frac < minAttributedFraction {
		t.Errorf("attribution fell to %.4f, below the committed floor of %.4f. "+
			"Either the scanner stopped indexing something the compiler instruments, "+
			"or new unindexed code landed. Fix the cause, or move the floor in a PR that says why.",
			frac, minAttributedFraction)
	}
}

// dogfoodDoctor is the plainest claim in this file: atlas's own self-check,
// run against atlas, must pass.
//
// The index was written from this working tree moments ago and the coverage
// frontier from a profile of the same tree, so every check has the inputs it
// needs. A failure here means atlas is reporting that its picture of this repo
// is not true — which is the one thing it must never be wrong about, because
// every other verb answers from that picture.
func dogfoodDoctor(t *testing.T, env dogfoodEnv) {
	res := runAtlas(t, "doctor", "--root", env.repoRoot, "--db-path", env.db)
	var out struct {
		Checks []struct {
			Name        string `json:"name"`
			Severity    string `json:"severity"`
			Finding     string `json:"finding"`
			Remediation string `json:"remediation"`
		} `json:"checks"`
		Worst string `json:"worst"`
	}
	decodeResult(t, res, &out)

	for _, c := range out.Checks {
		t.Logf("doctor %-22s %-4s %s", c.Name, c.Severity, c.Finding)
	}
	for _, c := range out.Checks {
		switch c.Severity {
		case "fail":
			t.Errorf("doctor check %q FAILED: %s (remediation: %s)", c.Name, c.Finding, c.Remediation)
		case "warn":
			// A warn is a finding, not a gate. It is surfaced loudly so a
			// reviewer sees it, but failing on it would make this suite red
			// for a coverage frontier that is a week old, which is a
			// scheduling fact rather than a defect.
			t.Logf("doctor check %q warns: %s", c.Name, c.Finding)
		case "n/a":
			// Every input was prepared above, so nothing should be
			// inapplicable. When something is, the gate is weaker than it
			// looks and the reader should know.
			t.Errorf("doctor check %q reported n/a even though scan and cov sync both ran: %s",
				c.Name, c.Finding)
		}
	}
	if out.Worst == "fail" {
		t.Errorf("atlas doctor reports %q against atlas's own repo", out.Worst)
	}
}

// dogfoodSQL gates the share of this repo's SQL that `atlas sql` can resolve.
//
// Atlas's queries live in packages/store/queries/*.sql against a schema in
// packages/store/schema/*.sql, which is precisely the shape the verb claims to
// understand. If it cannot resolve its own, the claim does not survive its
// first customer.
func dogfoodSQL(t *testing.T, env dogfoodEnv) {
	res := runAtlas(t, "sql", "scan", "--db-path", env.db)
	var out struct {
		Operations       int     `json:"operations"`
		Resolved         int     `json:"resolved"`
		Unresolved       int     `json:"unresolved"`
		ResolvedFraction float64 `json:"resolved_fraction"`
		Tables           int     `json:"tables"`
	}
	decodeResult(t, res, &out)
	t.Logf("sql: %d operations, %d resolved, %d unresolved = %.4f (floor %.4f) over %d tables",
		out.Operations, out.Resolved, out.Unresolved, out.ResolvedFraction,
		minSQLResolvedFraction, out.Tables)

	if out.Operations == 0 {
		t.Fatal("atlas sql found no operations in a repo whose queries are all in .sql files; " +
			"the scan found nothing rather than resolving everything")
	}
	if out.ResolvedFraction < minSQLResolvedFraction {
		t.Errorf("sql resolved %.4f of %d operations, below the committed floor of %.4f",
			out.ResolvedFraction, out.Operations, minSQLResolvedFraction)
	}
}

// dogfoodCovDiff asserts `atlas cov diff` reports a REAL number for this
// branch — not a target, a number.
//
// The honest gate here is narrow on purpose. Patch coverage on any given
// branch is a property of that branch, so asserting a threshold would fail on
// a legitimate docs-only commit and teach everyone to ignore it. What can be
// asserted is that the command reaches an answer: it finds the base, joins the
// diff onto the index, and either produces a percentage or says in as many
// words that the diff touched no measurable code. Silence, or a confident
// percentage over zero known lines, are the failures.
func dogfoodCovDiff(t *testing.T, env dogfoodEnv) {
	if env.base == "" {
		t.Skip("no base ref available (shallow checkout, no origin/main and no HEAD~1); " +
			"cov diff has nothing to compare against")
	}
	res := runAtlas(t, "cov", "diff", "--base", env.base, "--db-path", env.db)
	var out struct {
		Base          string   `json:"base"`
		ChangedFiles  int      `json:"changed_files"`
		ChangedLines  int      `json:"changed_lines"`
		KnownLines    float64  `json:"known_lines"`
		UnknownLines  float64  `json:"unknown_lines"`
		Measurable    bool     `json:"measurable"`
		Percent       *float64 `json:"percent"`
		StaleIndexFls []string `json:"stale_index_files"`
	}
	decodeResult(t, res, &out)

	pct := "n/a"
	if out.Percent != nil {
		pct = fmt.Sprintf("%.1f%%", *out.Percent)
	}
	t.Logf("cov diff vs %s: %d files, %d changed lines, %.0f known / %.0f unknown, measurable=%v, patch=%s",
		out.Base, out.ChangedFiles, out.ChangedLines, out.KnownLines, out.UnknownLines, out.Measurable, pct)

	// A stale index would make every line-number join meaningless (#89/#90),
	// and the scan above ran seconds ago — so anything stale here is a bug in
	// freshness detection, not a stale checkout.
	if len(out.StaleIndexFls) > 0 {
		t.Errorf("cov diff reports %d stale-index files immediately after a scan: %v",
			len(out.StaleIndexFls), out.StaleIndexFls)
	}
	if !out.Measurable {
		// Legitimate for a docs-only branch. Reported rather than asserted,
		// because the alternative is a gate that fails on correct commits.
		t.Logf("cov diff found no measurable change against %s; nothing to gate on this branch", out.Base)
		return
	}
	if out.Percent == nil {
		t.Fatal("cov diff called the change measurable but reported no percentage")
	}
	if out.KnownLines <= 0 {
		t.Errorf("cov diff reported %.1f%% patch coverage over %.0f known lines; "+
			"a percentage with no denominator is not a measurement", *out.Percent, out.KnownLines)
	}
	if *out.Percent < 0 || *out.Percent > 100 {
		t.Errorf("cov diff reported %.4f%% patch coverage, which is not a percentage", *out.Percent)
	}
}
