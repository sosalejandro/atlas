package affected

import (
	"context"
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// SymbolSource is the read side of the `symbols` table. Satisfied by
// store.Symbols.
type SymbolSource interface {
	List(ctx context.Context, f store.SymbolFilter) ([]store.SymbolRow, error)
}

// EvidenceSource is the read side of `test_coverage`. Satisfied by
// store.TestCoverage.
type EvidenceSource interface {
	// TestsExecuting names the tests that ran one symbol — the inverse index
	// the whole selection is built on.
	TestsExecuting(ctx context.Context, runID, symbolID int64) ([]store.TestExecution, error)

	// SymbolsExecutedBy is the forward direction, used for exactly one
	// question: does this test have ANY execution history? A test edited by
	// the diff is selected either way, but whether it is NEW (never run) or
	// merely modified is the difference between "expect this to be the one
	// that fails" and "expect this to keep passing".
	SymbolsExecutedBy(ctx context.Context, runID, testSymbolID int64) ([]store.TestExecution, error)

	// CountTests is how many distinct tests contributed evidence to a run.
	// Zero across the frontier is the "I have no evidence" signal.
	CountTests(ctx context.Context, runID int64) (int, error)
}

// FeatureSource is the read side of `feature_symbols`. Optional: nil disables
// the feature roll-up, which is a reporting convenience and never affects
// which tests are selected.
type FeatureSource interface {
	ListBySymbol(ctx context.Context, symbolID int64) ([]store.FeatureSymbolLink, error)
}

// Inputs are everything Select needs. Git, Symbols and Evidence are required.
type Inputs struct {
	Git      GitDiff
	Symbols  SymbolSource
	Evidence EvidenceSource
	Features FeatureSource

	// Frontier is the coverage the selection reads evidence from — normally
	// store.Coverage().LatestFrontier. An empty frontier is not an error; it
	// is the commonest reason to run everything.
	Frontier store.CoverageFrontier

	// Since is the git ref to diff against. Required.
	Since string

	// Now seams the clock so the reported age of the evidence is testable.
	Now func() time.Time
}

// Select answers "given this diff, what do I need to run?".
//
// The order of the passes is load-bearing. Repo-wide bail-outs (no evidence,
// no indexed tests) are decided BEFORE any file is examined, so a store that
// has never ingested per-test coverage reports the true reason rather than
// "none of your changed symbols matched anything" — the same output an empty
// selection would produce, with the opposite meaning.
func Select(ctx context.Context, in Inputs) (Selection, error) {
	if err := in.validate(); err != nil {
		return Selection{}, err
	}
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}

	idx, err := buildSymbolIndex(ctx, in.Symbols)
	if err != nil {
		return Selection{}, err
	}

	s := &selector{
		in: in, idx: idx, now: now,
		seen:     map[int64]bool{},
		selected: map[int64]*SelectedTest{},
		history:  map[int64]bool{},
	}
	s.sel = Selection{Outcome: OutcomeSelected, Since: in.Since, TotalTests: len(idx.tests)}

	if err := s.readEvidence(ctx); err != nil {
		return Selection{}, err
	}
	s.checkRepoWidePreconditions()

	files, err := in.Git.ChangedFiles(ctx, in.Since)
	if err != nil {
		return Selection{}, err
	}
	s.sel.ChangedFiles = dedupeSorted(files)
	if len(s.sel.ChangedFiles) == 0 {
		// Nothing changed. Running nothing is the right answer, and it is not
		// a bail-out — but it is also not a "selection", so it gets its own
		// outcome rather than masquerading as a 100% reduction.
		s.sel.Outcome = OutcomeNoChanges
		return s.sel, nil
	}

	lines, err := in.Git.ChangedLines(ctx, in.Since)
	if err != nil {
		return Selection{}, err
	}
	s.mapFilesToSymbols(s.sel.ChangedFiles, lines)

	if err := s.selectTests(ctx); err != nil {
		return Selection{}, err
	}
	if err := s.rollUpFeatures(ctx); err != nil {
		return Selection{}, err
	}
	s.finish()
	return s.sel, nil
}

func (in Inputs) validate() error {
	if in.Since == "" {
		return fmt.Errorf("affected: a --since ref is required; there is no safe default to diff against")
	}
	if in.Git == nil || in.Symbols == nil || in.Evidence == nil {
		return fmt.Errorf("affected: Git, Symbols and Evidence inputs are all required")
	}
	return nil
}

// selector carries the mutable state of one Select call. It exists so each
// pass stays a short, separately readable method instead of one long function
// threading a dozen accumulators.
type selector struct {
	in       Inputs
	idx      *symbolIndex
	now      func() time.Time
	sel      Selection
	seen     map[int64]bool          // changed symbol ids already recorded
	selected map[int64]*SelectedTest // test symbol id -> the row being built

	// history records which selected tests have ANY execution evidence. It is
	// kept separately, and resolved in finish(), so the NoHistory label does
	// not depend on the order the changed symbols happened to be visited in.
	history map[int64]bool
}

func (s *selector) bail(reason, filePath, detail string) {
	s.sel.Fallbacks = append(s.sel.Fallbacks, Fallback{Reason: reason, Path: filePath, Detail: detail})
}

// readEvidence summarises the frontier the selection is computed from. It is
// reported unconditionally — including on the run-all path — because a reader
// deciding whether to trust a narrowed run needs to know how old the evidence
// behind it is.
func (s *selector) readEvidence(ctx context.Context) error {
	f := s.in.Frontier
	ev := Evidence{RunIDs: f.RunIDs(), Group: f.Group, PerRunTests: map[int64]int{}}
	for _, run := range f.Runs {
		n, err := s.in.Evidence.CountTests(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("affected: count tests in run %d: %w", run.ID, err)
		}
		ev.PerRunTests[run.ID] = n
		ev.TestsWithEvidence += n
		ev.Frameworks = append(ev.Frameworks, string(run.Framework))
		if run.FinishedAt.After(ev.NewestFinishedAt) {
			ev.NewestFinishedAt = run.FinishedAt
		}
	}
	ev.Frameworks = dedupeSorted(ev.Frameworks)
	if !ev.NewestFinishedAt.IsZero() {
		ev.Age = s.now().Sub(ev.NewestFinishedAt)
		ev.AgeSeconds = int64(ev.Age.Seconds())
	}
	s.sel.Evidence = ev
	return nil
}

// checkRepoWidePreconditions decides the two bail-outs that do not depend on
// the diff at all.
//
// The empty-evidence case is the one this whole package exists to get right:
// with no rows in test_coverage every inverse lookup returns nothing, so the
// natural result is an empty selection — which a CI runner cannot tell apart
// from "your change needs no tests".
func (s *selector) checkRepoWidePreconditions() {
	if s.in.Frontier.Empty() || s.sel.Evidence.TestsWithEvidence == 0 {
		s.bail(ReasonNoEvidence, "",
			"no per-test coverage evidence for the current frontier; run `atlas cov sync --per-test` before expecting a subset")
	}
	if len(s.idx.tests) == 0 {
		s.bail(ReasonNoTestsIndexed, "",
			"atlas has indexed no test symbols, so there is no suite to take a subset of; run `atlas scan`")
	}
}

// mapFilesToSymbols turns changed paths into the changed-symbol set, bailing
// on any path it cannot account for.
func (s *selector) mapFilesToSymbols(files []string, lines map[string][]LineRange) {
	for _, f := range files {
		switch class := classify(f); class {
		case classInert:
			s.sel.InertFiles = append(s.sel.InertFiles, f)
		case classBuildConfig, classTestInfra:
			reason, detail := classifyDetail(class)
			s.bail(reason, f, detail)
		case classSource:
			s.mapSourceFile(f, lines[f])
		}
	}
	sort.Slice(s.sel.ChangedSymbols, func(i, j int) bool {
		return s.sel.ChangedSymbols[i].QualifiedName < s.sel.ChangedSymbols[j].QualifiedName
	})
}

// mapSourceFile resolves one source file's changed lines to symbols.
//
// Three outcomes, in decreasing order of precision: every range landed inside
// a symbol (select those); some range landed outside every symbol (widen to
// the package, because a package-level declaration, an import or a build tag
// can change how everything around it behaves); atlas holds no symbols for the
// file at all (bail — there is nothing to reason with).
func (s *selector) mapSourceFile(filePath string, ranges []LineRange) {
	rows := s.idx.byFile[filePath]
	if len(rows) == 0 {
		s.bail(ReasonUnindexedFile, filePath,
			"atlas holds no symbols for this file; it may be an unscanned language, generated output, or newer than the last `atlas scan`")
		return
	}
	if len(ranges) == 0 {
		// --name-only saw the file but --unified=0 produced no hunks: a rename
		// or a mode change. Take the whole file rather than nothing.
		s.recordSymbols(rows, true)
		s.widen(filePath, "file", "the diff carries no line hunks for this path (a rename or mode change), so every symbol in it is treated as changed")
		return
	}
	hits, allMatched := s.idx.spansAt(filePath, ranges)
	s.recordSymbols(hits, false)
	if !allMatched {
		dir := path.Dir(filePath)
		s.recordSymbols(s.idx.byDir[dir], true)
		s.widen(filePath, "package",
			"an edited line fell outside every indexed symbol (a package-level declaration, an import block or a build tag), so the whole package is treated as changed")
	}
}

func (s *selector) widen(filePath, scope, detail string) {
	s.sel.Widenings = append(s.sel.Widenings, Widening{Path: filePath, Scope: scope, Detail: detail})
}

func (s *selector) recordSymbols(rows []store.SymbolRow, widened bool) {
	for _, r := range rows {
		if s.seen[r.ID] {
			continue
		}
		s.seen[r.ID] = true
		cs := ChangedSymbol{
			SymbolID:      r.ID,
			QualifiedName: r.QualifiedName,
			FilePath:      r.FilePath,
			Line:          r.Line,
			IsTest:        store.IsTestPath(r.FilePath),
			Widened:       widened,
		}
		if r.EndLine != nil {
			cs.EndLine = *r.EndLine
		}
		s.sel.ChangedSymbols = append(s.sel.ChangedSymbols, cs)
	}
}

// selectTests turns the changed-symbol set into the tests to run.
func (s *selector) selectTests(ctx context.Context) error {
	for _, cs := range s.sel.ChangedSymbols {
		if cs.IsTest {
			if err := s.selectChangedTest(ctx, cs); err != nil {
				return err
			}
			continue
		}
		found, err := s.selectTestsExecuting(ctx, cs)
		if err != nil {
			return err
		}
		if !found {
			// Nothing recorded executing this symbol. Because the per-test
			// ingest records EVERY executing test, that is evidence of
			// absence — a change nothing covers — rather than missing
			// evidence, so it is reported instead of forcing a full run.
			s.sel.UncoveredSymbols = append(s.sel.UncoveredSymbols, cs)
		}
	}
	return nil
}

// selectChangedTest handles a test symbol that the diff itself edited.
//
// It is selected unconditionally, BEFORE any evidence is consulted. A test
// added in this diff has no rows in test_coverage precisely because it has
// never run, and reading that emptiness as "reaches nothing" would skip the
// one test most likely to fail.
//
// The evidence lookup that follows decides only how to LABEL it. That
// distinction is worth the query: "new, never executed" and "existing test,
// edited" carry very different expectations into the run.
func (s *selector) selectChangedTest(ctx context.Context, cs ChangedSymbol) error {
	name, ok := s.idx.tests[cs.SymbolID]
	if !ok {
		// A symbol in a test file that `go test` will not dispatch: a helper,
		// a fixture builder, TestMain. Its callers are other tests, and no
		// coverage row records a test calling a helper.
		s.bail(ReasonUnrunnableTest, cs.FilePath, fmt.Sprintf(
			"%s is a test-file symbol that `go test` does not run on its own; the tests that depend on it are not recorded anywhere",
			cs.QualifiedName))
		return nil
	}
	t := s.ensureSelected(cs.SymbolID, name, s.idx.byID[cs.SymbolID])
	t.Why = append(t.Why, "changed by this diff")

	if s.history[cs.SymbolID] {
		return nil
	}
	for _, runID := range s.sel.Evidence.RunIDs {
		rows, err := s.in.Evidence.SymbolsExecutedBy(ctx, runID, cs.SymbolID)
		if err != nil {
			return fmt.Errorf("affected: execution history for %s: %w", cs.QualifiedName, err)
		}
		if len(rows) > 0 {
			s.history[cs.SymbolID] = true
			return nil
		}
	}
	return nil
}

// selectTestsExecuting unions the tests that recorded executing one changed
// symbol, across every run in the frontier.
func (s *selector) selectTestsExecuting(ctx context.Context, cs ChangedSymbol) (bool, error) {
	found := false
	for _, runID := range s.sel.Evidence.RunIDs {
		rows, err := s.in.Evidence.TestsExecuting(ctx, runID, cs.SymbolID)
		if err != nil {
			return false, fmt.Errorf("affected: tests executing %s: %w", cs.QualifiedName, err)
		}
		for _, r := range rows {
			found = true
			s.selectByEvidence(r.TestSymbolID, cs.QualifiedName)
		}
	}
	return found, nil
}

// selectByEvidence adds a test named by a coverage row.
//
// A row pointing at a symbol id the index no longer holds is a test that has
// since been deleted; skipping it is correct and needs no fallback. A row
// pointing at a symbol that exists but is not a runnable test means the
// evidence and the index disagree about what a test is, which would silently
// drop a test from the -run pattern — so that bails.
func (s *selector) selectByEvidence(testSymbolID int64, via shared.SymbolID) {
	row, ok := s.idx.byID[testSymbolID]
	if !ok {
		return
	}
	name, runnable := s.idx.tests[testSymbolID]
	if !runnable {
		s.bail(ReasonUnrunnableTest, row.FilePath, fmt.Sprintf(
			"coverage names %s as a test, but it yields no `go test -run` token; the pattern would silently omit it",
			row.QualifiedName))
		return
	}
	t := s.ensureSelected(testSymbolID, name, row)
	s.history[testSymbolID] = true
	t.Why = append(t.Why, "executes "+string(via))
}

func (s *selector) ensureSelected(id int64, runName string, row store.SymbolRow) *SelectedTest {
	if t, ok := s.selected[id]; ok {
		return t
	}
	t := &SelectedTest{
		SymbolID:      id,
		QualifiedName: row.QualifiedName,
		RunName:       runName,
		FilePath:      row.FilePath,
		Package:       path.Dir(row.FilePath),
	}
	s.selected[id] = t
	return t
}

// rollUpFeatures reports which annotated features the change lands in. It is
// a reporting convenience — the selection is identical with or without it —
// so a store error here must not sink the whole command.
func (s *selector) rollUpFeatures(ctx context.Context) error {
	if s.in.Features == nil {
		return nil
	}
	seen := map[shared.FeatureID]bool{}
	for _, cs := range s.sel.ChangedSymbols {
		links, err := s.in.Features.ListBySymbol(ctx, cs.SymbolID)
		if err != nil {
			return fmt.Errorf("affected: features for %s: %w", cs.QualifiedName, err)
		}
		for _, l := range links {
			if !seen[l.FeatureID] {
				seen[l.FeatureID] = true
				s.sel.Features = append(s.sel.Features, l.FeatureID)
			}
		}
	}
	sort.Slice(s.sel.Features, func(i, j int) bool { return s.sel.Features[i] < s.sel.Features[j] })
	return nil
}

// finish materialises the selected set and applies the closing rule: any
// fallback at all means run everything.
//
// The selected tests are DISCARDED on that path rather than left in place as a
// hint. A partial list next to outcome="run-all" is exactly the shape a
// caller reads the wrong way — take the tests, ignore the outcome, ship the
// regression.
func (s *selector) finish() {
	if len(s.sel.Fallbacks) > 0 {
		s.sel.Outcome = OutcomeRunAll
		s.sel.SelectedTests = nil
		s.sel.Packages = nil
		return
	}
	tests := make([]SelectedTest, 0, len(s.selected))
	pkgs := make([]string, 0, len(s.selected))
	for id, t := range s.selected {
		t.NoHistory = !s.history[id]
		tests = append(tests, *t)
		pkgs = append(pkgs, t.Package)
	}
	sort.Slice(tests, func(i, j int) bool {
		if tests[i].RunName != tests[j].RunName {
			return tests[i].RunName < tests[j].RunName
		}
		return tests[i].QualifiedName < tests[j].QualifiedName
	})
	s.sel.SelectedTests = tests
	s.sel.Packages = dedupeSorted(pkgs)
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
