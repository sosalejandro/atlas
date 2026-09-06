package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// featureLinkage checks that the annotation layer and the symbol layer
// still agree about which features exist.
//
// They drift apart in two directions, and neither one errors:
//
//   - A feature outlives its symbols. feature_symbols cascades when a
//     symbol is deleted, but the features row does not, so a renamed or
//     removed function leaves an empty shell behind that still ranks in
//     `atlas sprint` and still scores in `atlas health` -- about nothing.
//
//   - An annotation that DOES resolve to an indexed symbol still names a
//     feature the store has no row for. The ingest materializes a feature
//     for exactly that shape, so its absence means the two layers were
//     written by different passes and no longer agree.
//
// The second case is narrower than "any annotation naming an unknown
// feature", and deliberately so: see annotationSweep.
type featureLinkage struct{}

func (featureLinkage) Name() string { return "feature.linkage" }

func (featureLinkage) Examines() string {
	return "features with no linked symbols, and annotations that resolve to an indexed " +
		"symbol yet name a feature the store does not have"
}

func (c featureLinkage) Run(ctx context.Context, env *Env) (Result, error) {
	if res, ok := env.requireStore(); !ok {
		return res, nil
	}
	features, err := env.Store.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return Result{}, fmt.Errorf("doctor linkage: list features: %w", err)
	}
	if len(features) == 0 {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: "no features are declared in the store, so there is no linkage to check " +
				"(nothing in this repo carries an @atlas:feature annotation yet)",
			Remediation: "annotate a symbol with @atlas:feature <id>, then: atlas scan",
			Details:     map[string]any{"features": 0},
		}, nil
	}

	unlinked, err := featuresWithoutSymbols(ctx, env, features)
	if err != nil {
		return Result{}, err
	}
	sweep, err := sweepAnnotations(ctx, env, features)
	if err != nil {
		return Result{}, err
	}

	details := map[string]any{
		"features":                 len(features),
		"features_without_symbols": len(unlinked),
		"unlinked_features":        samples(unlinked),
		"annotation_sweep":         sweep.state(),
	}
	// The counts go in ONLY when the sweep ran. A `dangling_annotations:
	// 0` beside an empty list for a half that never executed reads
	// exactly like a half that executed and found nothing, which is the
	// one thing a diagnostic must never let a reader conclude.
	if sweep.ran {
		details["dangling_annotations"] = len(sweep.dangling)
		details["dangling_refs"] = samples(sweep.dangling)
		details["unanchored_annotations"] = sweep.unanchored
	}
	return linkageVerdict(len(features), unlinked, sweep, details)
}

// linkageVerdict scores the two halves: features with no symbols, and the
// annotation sweep.
//
// Warn, not fail. Both findings mean a slice of the picture is missing,
// which makes atlas's answers incomplete -- but unlike a stale index they
// do not make the answers it does give wrong, and a half-migrated
// annotation rollout would otherwise red-light every build for weeks.
func linkageVerdict(features int, unlinked []string, sweep annotationSweep, details map[string]any) (Result, error) {
	var complaints []string
	if len(unlinked) > 0 {
		complaints = append(complaints, fmt.Sprintf(
			"%d of %d features have no linked symbols", len(unlinked), features))
	}
	if len(sweep.dangling) > 0 {
		complaints = append(complaints, fmt.Sprintf(
			"%d annotations resolve to an indexed symbol but name a feature the store does not have",
			len(sweep.dangling)))
	}
	if len(complaints) > 0 {
		return Result{
			Severity:    SeverityWarn,
			Finding:     strings.Join(complaints, "; "),
			Remediation: "atlas scan",
			Details:     details,
		}, nil
	}
	if !sweep.ran {
		// The features half is clean, but the annotation half never ran.
		// "ok" here would let a check that examined half its input read
		// as a check that examined all of it.
		return Result{
			Severity: SeverityNotApplicable,
			Finding: fmt.Sprintf(
				"all %d features are linked to symbols, but the annotation sweep could not run "+
					"(doctor's read-only handle on the state database did not open; the "+
					"store.schema check carries the reason), so nothing was established about "+
					"the annotation layer",
				features),
			Remediation: "resolve the store.schema finding, then re-run: atlas doctor",
			Details:     details,
		}, nil
	}
	finding := fmt.Sprintf(
		"all %d features are linked to symbols, and every annotation that resolves to an "+
			"indexed symbol names one of them", features)
	if sweep.unanchored > 0 {
		finding += fmt.Sprintf(
			" (%d annotations resolve to no symbol at all and materialize no feature by design)",
			sweep.unanchored)
	}
	return Result{Severity: SeverityOK, Finding: finding, Details: details}, nil
}

// featuresWithoutSymbols returns the ids of features no symbol is linked
// to, sorted by the List order (feature id).
func featuresWithoutSymbols(ctx context.Context, env *Env, features []store.Feature) ([]string, error) {
	var out []string
	for _, f := range features {
		links, err := env.Store.FeatureSymbols().ListByFeature(ctx, f.ID)
		if err != nil {
			return nil, fmt.Errorf("doctor linkage: list links for %q: %w", f.ID, err)
		}
		if len(links) == 0 {
			out = append(out, string(f.ID))
		}
	}
	return out, nil
}

// annotationSweep is the outcome of the whole-table annotation pass.
//
// ran is the field that matters most: it separates "the sweep found no
// dangling annotations" from "the sweep did not happen", which a bare
// count cannot express.
type annotationSweep struct {
	// ran is false when doctor's read-only probe did not open, so no
	// annotation was examined at all.
	ran bool

	// dangling holds "path:line -> id" refs for annotations that resolve
	// to an indexed symbol yet name a feature with no features row.
	dangling []string

	// unanchored counts annotations that resolve to NO symbol. They are
	// not a finding; see sweepAnnotations.
	unanchored int
}

func (s annotationSweep) state() string {
	if s.ran {
		return "ran"
	}
	return "skipped"
}

// sweepAnnotations walks every feature/contract annotation and sorts it
// into the two buckets the check can honestly tell apart.
//
// The narrowing is the point. The check used to report every annotation
// naming a feature the store does not have, which conflated two
// completely different states:
//
//   - a genuine break: the annotation sits above an indexed declaration,
//     so the ingest's materialization pass WOULD have written a features
//     row for it, and the row is not there.
//
//   - normal operation: the annotation resolves to no symbol within
//     LookupAtPosition's window. packages/store/ingest.go documents this
//     as intentional -- "annotations on non-code files are legitimate but
//     cannot be materialized without a symbol to anchor on, and we'd
//     rather have no link than a phantom one" -- and it is the everyday
//     shape of a markdown file, a package-doc comment, an end-of-file
//     marker. Reporting these was reporting normal state as a problem.
//
// Nothing in the store distinguishes them after the fact, so the check
// re-asks the ingest's own question through the same port
// (Symbols.LookupAtPosition, same 30-line window) and reports only the
// first bucket. The second is counted, not complained about.
//
// The narrowing under-reports in one known place: the ingest also
// materializes a test-file annotation by falling back to the
// corresponding impl file, and that fallback lives behind unexported
// store helpers doctor cannot call. Such an annotation is counted here
// as unanchored rather than dangling. Under-reporting is the direction
// this check errs in on purpose -- a false alarm costs more than a
// missed one, because it is what teaches people to ignore the output.
//
// The payload is split with the same rule the materialization pass uses
// (annotations.IsDottedFeatureID), which is what keeps tags and tier
// keywords -- `#mocked`, `integration` -- out of the result.
func sweepAnnotations(ctx context.Context, env *Env, features []store.Feature) (annotationSweep, error) {
	// The sweep needs the read-only probe. When it did not open, say so
	// rather than failing the whole check: the features half is still
	// valid, and the schema check already carries the reason.
	if env.probe == nil {
		return annotationSweep{ran: false}, nil
	}
	known := make(map[shared.FeatureID]bool, len(features))
	for _, f := range features {
		known[f.ID] = true
	}
	refs, err := env.probe.listFeatureAnnotations(ctx)
	if err != nil {
		return annotationSweep{}, err
	}

	sweep := annotationSweep{ran: true}
	for _, ref := range refs {
		ids := featureIDsIn(ref.Value)
		if len(ids) == 0 {
			continue
		}
		anchored, err := annotationAnchored(ctx, env, ref)
		if err != nil {
			return annotationSweep{}, err
		}
		if !anchored {
			sweep.unanchored++
			continue
		}
		for _, id := range ids {
			if !known[shared.FeatureID(id)] {
				sweep.dangling = append(sweep.dangling,
					fmt.Sprintf("%s:%d -> %s", ref.FilePath, ref.Line, id))
			}
		}
	}
	return sweep, nil
}

// annotationAnchored asks the exact question the ingest asks when it
// decides whether an annotation can become a feature: is there a symbol
// at or just after this line in this file? Asked through the same port
// with the same window, so the two cannot drift.
func annotationAnchored(ctx context.Context, env *Env, ref annotationRef) (bool, error) {
	if ref.FilePath == "" || ref.Line <= 0 {
		return false, nil
	}
	_, err := env.Store.Symbols().LookupAtPosition(ctx, ref.FilePath, ref.Line)
	if errors.Is(err, shared.ErrSymbolNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("doctor linkage: resolve annotation %s:%d: %w",
			ref.FilePath, ref.Line, err)
	}
	return true, nil
}

// featureIDsIn extracts the dotted feature ids from an annotation
// payload, dropping `#tag` suffixes and bare tier keywords exactly as
// the ingest does.
func featureIDsIn(value string) []string {
	var out []string
	for _, tok := range strings.Fields(value) {
		if strings.HasPrefix(tok, "#") {
			continue
		}
		if annotations.IsDottedFeatureID(tok) {
			out = append(out, tok)
		}
	}
	return out
}
