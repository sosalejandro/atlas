package coverage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Parser is the interface every framework sub-package satisfies. The
// orchestrator depends on this surface only; callers wire concrete
// parsers from coverage/gotest, coverage/playwright, etc.
type Parser interface {
	Parse(r io.Reader) (Run, []Result, error)
}

// SymbolResolver looks up a Go symbol id by qualified name. The
// store.Symbols port satisfies this interface via its FindByQualifiedName
// method — we declare a narrower local alias so the orchestrator can be
// used with stubs in tests, and so a non-Go framework caller can pass
// nil to disable resolution entirely.
type SymbolResolver interface {
	FindByQualifiedName(ctx context.Context, qn shared.SymbolID) (store.SymbolRow, error)
}

// IngestOptions configures one Ingest call.
type IngestOptions struct {
	// Framework is the framework tag persisted on the run row. When zero
	// the Run.Framework reported by the parser is used.
	Framework Framework
	// RawPath is the path to the raw report file on disk (informational
	// only — recorded alongside the run for traceability).
	RawPath *string
	// Resolver maps Result.QualifiedSymbol → symbol_id. Optional; when
	// nil the result row's symbol_id stays NULL even if the parser
	// emitted a qualified name.
	Resolver SymbolResolver
	// Now overrides time.Now (test seam). When nil, time.Now().UTC() is
	// used to backfill empty StartedAt/FinishedAt on the run row.
	Now func() time.Time
}

// Ingest parses r with parser and writes the resulting Run + Results
// atomically through store.Coverage().InsertRunWithResults.
//
// Returns the new run's surrogate id.
//
// Resolution behaviour:
//
//   - If opts.Resolver is non-nil AND a Result carries a non-empty
//     QualifiedSymbol, the orchestrator calls FindByQualifiedName and
//     stamps the surrogate symbol_id onto the row. shared.ErrSymbolNotFound
//     is tolerated (the row is still written, with symbol_id NULL); any
//     other error aborts the ingest with the partial run rolled back.
//
//   - If a Result's FeatureID points to a feature that does not exist in
//     the features table, the SQLite FK will reject the insert. Callers
//     SHOULD ensure features are upserted before ingesting (Phase 4's
//     codeindex.Ingest pre-populates this from annotations); the
//     orchestrator does not auto-create features because doing so would
//     mask annotation typos at ingest time.
func Ingest(ctx context.Context, s *store.Store, parser Parser, r io.Reader, opts IngestOptions) (int64, error) {
	if s == nil {
		return 0, errors.New("coverage.Ingest: store required")
	}
	if parser == nil {
		return 0, errors.New("coverage.Ingest: parser required")
	}
	if r == nil {
		return 0, errors.New("coverage.Ingest: reader required")
	}

	run, results, err := parser.Parse(r)
	if err != nil {
		return 0, fmt.Errorf("coverage.Ingest: parse: %w", err)
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	fw := opts.Framework
	if fw == "" {
		fw = run.Framework
	}
	if fw == "" {
		return 0, errors.New("coverage.Ingest: framework required (parser left it empty)")
	}

	storeRun := store.CoverageRun{
		Framework:   store.Framework(fw),
		StartedAt:   run.StartedAt,
		FinishedAt:  run.FinishedAt,
		RawPath:     opts.RawPath,
		SummaryJSON: run.SummaryJSON,
	}
	if storeRun.StartedAt.IsZero() {
		storeRun.StartedAt = now().UTC()
	}
	if storeRun.FinishedAt.IsZero() {
		storeRun.FinishedAt = storeRun.StartedAt
	}

	storeResults, err := materialize(ctx, results, opts.Resolver)
	if err != nil {
		return 0, err
	}
	id, err := s.Coverage().InsertRunWithResults(ctx, storeRun, storeResults)
	if err != nil {
		return 0, fmt.Errorf("coverage.Ingest: persist: %w", err)
	}
	return id, nil
}

// materialize converts parser Result rows into store.CoverageResult rows,
// performing symbol_id lookup via the resolver. The slice is preserved
// in order so test counts line up with the input report.
func materialize(ctx context.Context, results []Result, resolver SymbolResolver) ([]store.CoverageResult, error) {
	out := make([]store.CoverageResult, 0, len(results))
	for _, r := range results {
		row := store.CoverageResult{
			Status:     store.CoverageStatus(r.Status),
			DurationMS: r.Duration.Milliseconds(),
			FeatureID:  r.FeatureID,
		}
		if r.Message != "" {
			msg := r.Message
			row.Message = &msg
		}
		if resolver != nil && r.QualifiedSymbol != "" {
			sym, err := resolver.FindByQualifiedName(ctx, r.QualifiedSymbol)
			switch {
			case err == nil:
				id := sym.ID
				row.SymbolID = &id
			case errors.Is(err, shared.ErrSymbolNotFound):
				// expected for tests in packages we haven't scanned —
				// leave symbol_id nil
			default:
				return nil, fmt.Errorf("coverage.Ingest: resolve %q: %w", r.QualifiedSymbol, err)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// ParseFunc adapts a plain `func(io.Reader) (Run, []Result, error)` (the
// signature every framework sub-package exports) into the Parser
// interface. Use it to pass a sub-package's Parse function directly to
// Ingest without a wrapper struct:
//
//	id, err := coverage.Ingest(ctx, s, coverage.ParseFunc(gotest.Parse), r, opts)
type ParseFunc func(r io.Reader) (Run, []Result, error)

// Parse satisfies the Parser interface.
func (p ParseFunc) Parse(r io.Reader) (Run, []Result, error) { return p(r) }
