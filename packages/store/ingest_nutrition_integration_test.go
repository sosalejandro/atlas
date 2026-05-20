//go:build integration

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// TestIngest_NutritionDogfood is the end-to-end smoke test that exercises
// the full pipeline (codeindex.IndexProject -> store.Ingest -> feature
// materialization) against the real nutrition-v2-go monorepo.
//
// It is gated behind the `integration` build tag so CI does not require the
// nutrition checkout to be present. Run locally with:
//
//	go test -tags=integration ./packages/store/... -run TestIngest_NutritionDogfood
//
// Or via the env var when the path is non-canonical:
//
//	NUTRITION_ROOT=/path/to/repo go test -tags=integration ./packages/store/...
//
// Acceptance bar:
//
//   - codeindex.IndexProject succeeds.
//   - Ingest writes a non-zero count of annotations.
//   - FeaturesMaterialized crosses the conservative ≥500 threshold —
//     atlas#26 regression guard. Pre-fix the count was 0 because the Go
//     AST scanner silently skipped `_test.go` and nutrition's
//     `@testreg` / `@atlas:feature` annotations all live in test files.
//     Post-fix the actual count clears 900; 500 is a deliberately loose
//     floor so churn in nutrition's annotation corpus doesn't flap the
//     threshold.
//   - Three spot-check feature IDs known to live exclusively in `_test.go`
//     files materialize a feature row AND a non-empty feature_symbols
//     link list — proving the end-to-end annotation→symbol→link path,
//     not just the aggregate count.
func TestIngest_NutritionDogfood(t *testing.T) {
	nutritionRoot := os.Getenv("NUTRITION_ROOT")
	if nutritionRoot == "" {
		nutritionRoot = "/home/alejandrososa/Documents/startup-projects/nutrition-v2-go"
	}
	if _, err := os.Stat(nutritionRoot); err != nil {
		t.Skipf("nutrition-v2-go not present at %s, skipping integration smoke", nutritionRoot)
	}

	ctx := context.Background()

	idx, err := codeindex.IndexProject(ctx, nutritionRoot, codeindex.Options{
		SkipTS:    true, // keep the smoke under a minute and avoid node/TS prereq.
		HashFiles: true,
	})
	if err != nil {
		t.Fatalf("IndexProject(%s): %v", nutritionRoot, err)
	}

	tmpDB := filepath.Join(t.TempDir(), "nutrition-smoke.db")
	s, err := Open(ctx, tmpDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	feats, err := s.Features().List(ctx, FeatureFilter{})
	if err != nil {
		t.Fatalf("Features.List: %v", err)
	}

	t.Logf("nutrition smoke: symbols=%d edges=%d annotations=%d features_materialized=%d feature_symbols_linked=%d features_in_db=%d orphan_skipped=%d",
		stats.SymbolsInserted, stats.EdgesInserted, stats.AnnotationsInserted,
		stats.FeaturesMaterialized, stats.FeatureSymbolsLinked,
		len(feats), stats.OrphanAnnotationsSkipped)

	if stats.AnnotationsInserted == 0 {
		t.Fatal("nutrition smoke wrote zero annotations - parser or ingest regressed")
	}
	if stats.FeaturesMaterialized == 0 {
		t.Fatal("nutrition smoke materialized zero features — atlas#26 regression: the Go scanner is skipping _test.go again")
	}
	// Exact-count + 2% tolerance band. Calibrated 2026-05-20 against
	// nutrition's then-current corpus (972 features). When the corpus
	// drifts more than 2% in either direction, the audit owner should
	// re-calibrate this band in the same commit that causes the drift —
	// this is a regression guard, not a moving target.
	//
	// Audit details: atlas-internal/docs/dogfood-findings/2026-05-20-annotation-gap.md
	// Refs sosalejandro/atlas#39 (Horizon 1 closure tracker, W2-D).
	const expected = 972
	const tolerance = expected / 50 // 2%
	if stats.FeaturesMaterialized < expected-tolerance || stats.FeaturesMaterialized > expected+tolerance {
		t.Fatalf("nutrition smoke materialized %d features; want %d ± %d (2%% tolerance)",
			stats.FeaturesMaterialized, expected, tolerance)
	}

	// Spot-check three feature IDs known to live on test functions in Go
	// `_test.go` files. Each must produce (a) a feature row in the
	// features table AND (b) a non-empty feature_symbols link list. The
	// aggregate count above can be satisfied by many annotations on a
	// few popular features; this is the structural check that arbitrary
	// test-file annotations are correctly resolved end-to-end.
	spotChecks := []shared.FeatureID{
		"plans-patient.export-pdf",
		"email-relay.delivery",
		"batch-sessions.update",
	}
	for _, fid := range spotChecks {
		feat, err := s.Features().Get(ctx, fid)
		if err != nil {
			t.Errorf("Features.Get(%q): %v — atlas#26 regression: this feature lives in _test.go and should have materialized",
				fid, err)
			continue
		}
		if feat.ID != fid {
			t.Errorf("Features.Get(%q) returned feature with ID=%q", fid, feat.ID)
		}
		links, err := s.FeatureSymbols().ListByFeature(ctx, fid)
		if err != nil {
			t.Errorf("FeatureSymbols.ListByFeature(%q): %v", fid, err)
			continue
		}
		if len(links) == 0 {
			t.Errorf("FeatureSymbols.ListByFeature(%q) returned 0 links; expected at least one annotation→symbol link",
				fid)
		}
	}
}
