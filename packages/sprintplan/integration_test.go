package sprintplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

const realNutritionRoot = "/home/alejandrososa/Documents/startup-projects/nutrition-v2-go"

// TestIntegration_SprintplanRanksTopFiveAgainstNutrition exercises the full
// Index → Ingest → Rank pipeline against the real nutrition codebase.
//
// Skips cleanly when:
//   - testing.Short() is set
//   - the nutrition checkout isn't present
//   - ATLAS_INTEGRATION isn't set (CI default; full integration runs are
//     opt-in because they index the whole repo and balloon under -race)
//
// The assertion is intentionally observational: the top-5 must each carry
// at least one Reason and a non-empty Cost label.
func TestIntegration_SprintplanRanksTopFiveAgainstNutrition(t *testing.T) {
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

	a := audit.New(s, audit.Options{})
	p := New(s, a, Options{})
	top, err := p.TopN(ctx, 5)
	if err != nil {
		t.Fatalf("TopN: %v", err)
	}
	if len(top) == 0 {
		t.Fatal("TopN(5) returned 0 items; expected ≥1 from the real codebase")
	}
	for i, item := range top {
		if item.Cost == "" {
			t.Errorf("top[%d] cost empty: %+v", i, item)
		}
		if len(item.Reasons) == 0 {
			t.Errorf("top[%d] reasons empty: %+v", i, item)
		}
		t.Logf("top[%d]: id=%q prio=%.1f cost=%s reasons=%v",
			i, item.FeatureID, item.Priority, item.Cost, item.Reasons)
	}
	// The top item's priority should NOT be zero — at least one feature
	// in the real codebase has gap-worthy signals.
	if top[0].Priority == 0 {
		t.Errorf("top[0].Priority = 0 — expected at least one non-trivial backlog item")
	}
	_ = time.Now()
}

// materialiseFeaturesFromAnnotations is the same helper used by the audit
// integration test. We duplicate it here rather than exporting it across
// packages — both copies do the test-only stand-in work for the
// not-yet-shipped annotation→feature resolver.
func materialiseFeaturesFromAnnotations(ctx context.Context, s *store.Store) error {
	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return err
	}
	symsByFile := make(map[string][]store.SymbolRow, len(syms))
	symsByDir := make(map[string][]store.SymbolRow, len(syms))
	for _, sym := range syms {
		symsByFile[sym.FilePath] = append(symsByFile[sym.FilePath], sym)
		dir := filepath.Dir(sym.FilePath)
		symsByDir[dir] = append(symsByDir[dir], sym)
	}
	allFiles := make(map[string]bool, len(symsByFile))
	for f := range symsByFile {
		allFiles[f] = true
	}
	for path := range symsByFile {
		dir, name := filepath.Split(path)
		dir = strings.TrimSuffix(dir, "/")
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if !strings.HasSuffix(base, "_test") {
			allFiles[filepath.ToSlash(filepath.Join(dir, base+"_test"+ext))] = true
		}
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
			for _, idRaw := range fields {
				if strings.HasPrefix(idRaw, "#") {
					continue
				}
				fid := shared.FeatureID(strings.TrimSpace(idRaw))
				if fid == "" {
					continue
				}
				if featureToSymbols[fid] == nil {
					featureToSymbols[fid] = make(map[int64]bool)
				}
				for _, sym := range symsByFile[r.FilePath] {
					featureToSymbols[fid][sym.ID] = true
				}
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
