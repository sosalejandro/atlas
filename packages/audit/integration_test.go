package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// realNutritionRoot is the canonical path the integration tests look for.
// When absent, the integration tests skip cleanly — this lets the rest of
// CI stay green on machines without the nutrition checkout.
const realNutritionRoot = "/home/alejandrososa/Documents/startup-projects/nutrition-v2-go"

// TestIntegration_AuditScoresAgainstNutritionCodebase exercises the full
// Index → Ingest → ScoreAll pipeline against the real nutrition codebase.
//
// Skips cleanly when:
//   - testing.Short() is set
//   - the nutrition checkout isn't present (CI runners / fresh clones)
//   - ATLAS_INTEGRATION isn't set (CI default — full integration tests
//     are gated behind an explicit opt-in env var because they index the
//     whole nutrition repo and add ~30s under -race)
//
// The assertion is intentionally gentle: at least one feature must score
// below 50 — the real codebase has incomplete features by design.
func TestIntegration_AuditScoresAgainstNutritionCodebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration in short mode")
	}
	if os.Getenv("ATLAS_INTEGRATION") == "" {
		t.Skip("set ATLAS_INTEGRATION=1 to run nutrition-codebase integration")
	}
	if _, err := os.Stat(realNutritionRoot); err != nil {
		t.Skipf("nutrition checkout missing at %s; skipping", realNutritionRoot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s := openTestStore(t)

	idx, err := codeindex.IndexProject(ctx, realNutritionRoot, codeindex.Options{
		SkipTS:                 true, // avoid Node dep — go-only audit signals
		HashFiles:              false,
		SkipPatternRecognizers: false,
	})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Atlas does not yet auto-materialise feature rows from @atlas:feature
	// annotations during Ingest (that resolver lands in a later phase). For
	// this integration test we project the annotated features manually:
	// any (file, @atlas:feature <id>) site gets a features row + a
	// feature_symbols link to every symbol declared in the same file.
	if err := materialiseFeaturesFromAnnotations(ctx, s); err != nil {
		t.Fatalf("materialise features: %v", err)
	}

	a := New(s, Options{
		// No GitBlame wired — the integration test deliberately exercises
		// the "freshness signal unavailable" path. Coverage + pattern +
		// contract signals (where present) are still produced.
	})
	scores, err := a.ScoreAll(ctx)
	if err != nil {
		t.Fatalf("ScoreAll: %v", err)
	}
	if len(scores) == 0 {
		t.Fatal("ScoreAll returned 0 features; expected the real codebase to have annotated features")
	}

	// Assertion: at least one feature must score below 50. The nutrition
	// codebase is mid-migration and has features without complete coverage
	// / patterns / contracts; if every score is >= 50 the algorithm is
	// being too generous (or the codebase suddenly got fully covered).
	belowFifty := 0
	worstFeatureID := scores[0].FeatureID
	worstScore := scores[0].Score
	for _, h := range scores {
		if h.Score < 50 {
			belowFifty++
		}
		if h.Score < worstScore {
			worstScore = h.Score
			worstFeatureID = h.FeatureID
		}
	}
	t.Logf("scored %d features; %d below 50; worst = %q (%.1f)",
		len(scores), belowFifty, worstFeatureID, worstScore)
	if belowFifty == 0 {
		t.Errorf("no features scored below 50; expected ≥1 (codebase has incomplete features by design)")
	}
}

// TestIntegration_AuditSnapshotPersistsAgainstNutritionCodebase confirms
// the full pipeline can round-trip a real-codebase snapshot through SQLite.
//
// Same skip rules as above (ATLAS_INTEGRATION=1 opt-in).
func TestIntegration_AuditSnapshotPersistsAgainstNutritionCodebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration in short mode")
	}
	if os.Getenv("ATLAS_INTEGRATION") == "" {
		t.Skip("set ATLAS_INTEGRATION=1 to run nutrition-codebase integration")
	}
	if _, err := os.Stat(realNutritionRoot); err != nil {
		t.Skipf("nutrition checkout missing at %s; skipping", realNutritionRoot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s := openTestStore(t)
	idx, err := codeindex.IndexProject(ctx, realNutritionRoot, codeindex.Options{
		SkipTS: true,
	})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := materialiseFeaturesFromAnnotations(ctx, s); err != nil {
		t.Fatalf("materialise features: %v", err)
	}

	a := New(s, Options{})
	scores, err := a.ScoreAll(ctx)
	if err != nil {
		t.Fatalf("ScoreAll: %v", err)
	}
	id, err := a.PersistSnapshot(ctx, scores)
	if err != nil {
		t.Fatalf("PersistSnapshot: %v", err)
	}
	loaded, err := a.LoadSnapshot(ctx, id)
	if err != nil {
		t.Fatalf("LoadSnapshot(%d): %v", id, err)
	}
	if len(loaded) != len(scores) {
		t.Errorf("LoadSnapshot len = %d, want %d", len(loaded), len(scores))
	}
	// Spot-check that the JSON round-trip preserved each FeatureID.
	loadedIDs := make(map[string]bool, len(loaded))
	for _, h := range loaded {
		loadedIDs[string(h.FeatureID)] = true
	}
	for _, h := range scores {
		if !loadedIDs[string(h.FeatureID)] {
			t.Errorf("snapshot dropped feature %q", h.FeatureID)
			break
		}
	}
}

// materialiseFeaturesFromAnnotations is a test-only stand-in for the
// (not-yet-shipped) annotation→feature resolver. It walks every symbol's
// file, reads each file's annotations, upserts one features row per id,
// and links symbols to features using a per-package association:
//
//   - Annotations on _test.go files (which the Go scanner doesn't emit
//     symbols for) get linked to every non-test symbol in the same
//     directory. This mirrors how @testreg annotations work in practice:
//     "this test file tests the package it lives in."
//   - Annotations on non-test files get linked to symbols declared in
//     that exact file.
//
// Keeps the integration test self-contained: no dependency on a future
// resolver, but real annotation + real symbol data drive real audit
// signals.
func materialiseFeaturesFromAnnotations(ctx context.Context, s *store.Store) error {
	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return err
	}
	// Index symbols by file AND by package directory for the test-file
	// fallback.
	symsByFile := make(map[string][]store.SymbolRow, len(syms))
	symsByDir := make(map[string][]store.SymbolRow, len(syms))
	for _, sym := range syms {
		symsByFile[sym.FilePath] = append(symsByFile[sym.FilePath], sym)
		dir := filepath.Dir(sym.FilePath)
		symsByDir[dir] = append(symsByDir[dir], sym)
	}

	// Walk EVERY annotated file — annotations on test files won't appear in
	// symsByFile but we still want them.
	allFiles := make(map[string]bool, len(symsByFile))
	for f := range symsByFile {
		allFiles[f] = true
	}
	// Also discover annotation-only files by listing the annotations table
	// through ListByFile probing — there's no global list endpoint, so we
	// take a pragmatic shortcut: query every file in symsByDir (which is a
	// superset of plausible parents) plus speculate `_test.go` variants of
	// each known symbol file.
	speculative := make(map[string]bool)
	for path := range symsByFile {
		dir, name := filepath.Split(path)
		// Drop trailing slash from dir for join consistency.
		dir = strings.TrimSuffix(dir, "/")
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if !strings.HasSuffix(base, "_test") {
			speculative[filepath.ToSlash(filepath.Join(dir, base+"_test"+ext))] = true
		}
	}
	for f := range speculative {
		allFiles[f] = true
	}

	featureToSymbols := make(map[shared.FeatureID]map[int64]bool)
	for f := range allFiles {
		rows, err := s.Annotations().ListByFile(ctx, f)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Kind != shared.AnnFeature && r.Kind != shared.AnnContract {
				continue
			}
			fields := strings.Fields(r.Value)
			if len(fields) == 0 {
				continue
			}
			// Pick up every id in the value — multi-id annotations
			// (`@testreg a.b c.d`) are real in nutrition-v2-go.
			for _, idRaw := range fields {
				if strings.HasPrefix(idRaw, "#") {
					continue // tags
				}
				fid := shared.FeatureID(strings.TrimSpace(idRaw))
				if fid == "" {
					continue
				}
				if featureToSymbols[fid] == nil {
					featureToSymbols[fid] = make(map[int64]bool)
				}
				// Same-file symbols.
				for _, sym := range symsByFile[r.FilePath] {
					featureToSymbols[fid][sym.ID] = true
				}
				// Same-directory non-test fallback (covers _test.go
				// annotation rows).
				dir := filepath.Dir(r.FilePath)
				for _, sym := range symsByDir[dir] {
					featureToSymbols[fid][sym.ID] = true
				}
			}
		}
	}

	for fid, symSet := range featureToSymbols {
		if err := s.Features().Upsert(ctx, store.Feature{
			ID: fid, Title: string(fid),
		}); err != nil {
			return err
		}
		for sid := range symSet {
			_ = s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
				FeatureID: fid, SymbolID: sid,
				Role: store.RoleImpl, Source: store.SourceInferred,
			})
		}
	}
	return nil
}

// Compile-time use of filepath in the file (keeps the import live when
// the helper text is the only consumer).
var _ = filepath.ToSlash
