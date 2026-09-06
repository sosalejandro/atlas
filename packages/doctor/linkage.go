package doctor

import (
	"context"
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
//     `atlas sprint` and still scores in `atlas audit` -- about nothing.
//
//   - An annotation outlives its symbol. The ingest materializes a feature
//     only when it can resolve the annotation to a nearby declaration; when
//     it cannot, the annotation row is written and silently produces no
//     feature. The user sees an annotated function that no atlas command
//     knows about.
type featureLinkage struct{}

func (featureLinkage) Name() string { return "feature.linkage" }

func (featureLinkage) Examines() string {
	return "features with no linked symbols, and annotations naming a feature the store does not have"
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
	dangling, err := danglingAnnotations(ctx, env, features)
	if err != nil {
		return Result{}, err
	}

	details := map[string]any{
		"features":                 len(features),
		"features_without_symbols": len(unlinked),
		"dangling_annotations":     len(dangling),
		"unlinked_features":        samples(unlinked),
		"dangling_refs":            samples(dangling),
	}

	// Warn, not fail. Both findings mean a slice of the picture is
	// missing, which makes atlas's answers incomplete -- but unlike a
	// stale index they do not make the answers it does give wrong, and a
	// half-migrated annotation rollout would otherwise red-light every
	// build for weeks.
	var complaints []string
	if len(unlinked) > 0 {
		complaints = append(complaints, fmt.Sprintf(
			"%d of %d features have no linked symbols", len(unlinked), len(features)))
	}
	if len(dangling) > 0 {
		complaints = append(complaints, fmt.Sprintf(
			"%d annotations name a feature the store does not have", len(dangling)))
	}
	if len(complaints) > 0 {
		return Result{
			Severity:    SeverityWarn,
			Finding:     strings.Join(complaints, "; "),
			Remediation: "atlas scan",
			Details:     details,
		}, nil
	}
	return Result{
		Severity: SeverityOK,
		Finding: fmt.Sprintf("all %d features are linked to symbols, and every annotation names one of them",
			len(features)),
		Details: details,
	}, nil
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

// danglingAnnotations returns "path:line -> id" refs for feature ids that
// appear in an annotation but have no features row.
//
// The payload is split with the same rule the store's materialization
// pass uses (annotations.IsDottedFeatureID), which is what keeps tags and
// tier keywords -- `#mocked`, `integration` -- out of the result. A token
// this rule cannot classify is simply not reported: the check would
// rather under-report than invent a missing feature, because a false
// alarm here costs more than a missed one.
func danglingAnnotations(ctx context.Context, env *Env, features []store.Feature) ([]string, error) {
	// The annotation sweep needs the read-only probe. When it did not
	// open, report nothing for this half rather than failing the whole
	// check: the features half above is still valid, and the schema check
	// already carries the reason the probe is missing.
	if env.probe == nil {
		return nil, nil
	}
	known := make(map[shared.FeatureID]bool, len(features))
	for _, f := range features {
		known[f.ID] = true
	}
	refs, err := env.probe.listFeatureAnnotations(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ref := range refs {
		for _, id := range featureIDsIn(ref.Value) {
			if !known[shared.FeatureID(id)] {
				out = append(out, fmt.Sprintf("%s:%d -> %s", ref.FilePath, ref.Line, id))
			}
		}
	}
	return out, nil
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
