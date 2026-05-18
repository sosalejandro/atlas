//go:build integration

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
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
// Acceptance bar — the wire-up:
//
//   - codeindex.IndexProject succeeds.
//   - Ingest succeeds and writes a non-zero count of annotations to the
//     state DB.
//   - At least one of {features_materialized, orphan_skipped} is non-zero,
//     because nutrition v2 carries feature annotations in test files and
//     production handlers — annotations of either kind exercise the
//     materialize branch.
//
// Note on the orphan count: nutrition v2 carries the bulk of its
// `@testreg`/`@atlas:feature` annotations in `_test.go` and `*.test.ts*`
// files. The Go AST scanner deliberately excludes `_test.go`
// (packages/codeindex/go/scanner.go) and the TS sub-scanner needs `node`
// + the `typescript` package on the host PATH. In CI environments where
// neither prerequisite is met, the materialize branch will produce ZERO
// features but a high orphan count — the pipeline is still healthy. The
// hard threshold "FeaturesMaterialized > N" only makes sense once the
// scanner ingests test-file symbols too (out of scope for this fix).
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
	if stats.FeaturesMaterialized == 0 && stats.OrphanAnnotationsSkipped == 0 {
		t.Fatal("nutrition smoke saw zero feature/contract annotations - extractFeatureIDsFromAnnotation or schemaAnnotationKinds regressed")
	}
}
