//go:build integration

package maestro_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/coverage/maestro"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// TestIngest_MaestroFixture is the end-to-end smoke test that exercises
// the full pipeline (maestro parser → coverage.Ingest → store) against
// a committed fixture under packages/coverage/testdata/maestro.json.
// The maestro parser accepts summary JSON ({"started_at":"...","flows":[...]})
// or JUnit XML; this fixture uses the summary JSON format.
// Build-tagged `integration`.
func TestIngest_MaestroFixture(t *testing.T) {
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "atlas.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	// Pre-seed features that the parser will derive from flow file names
	// (flows/auth/login.yaml → "login", flows/auth/register.yaml → "register").
	// Without this the FK constraint on coverage_results.feature_id rejects the insert.
	for _, fid := range []shared.FeatureID{"login", "register"} {
		if err := s.Features().Upsert(ctx, store.Feature{
			ID:    fid,
			Title: string(fid),
			Kind:  store.FeatureKindFeature,
		}); err != nil {
			t.Fatalf("Features.Upsert %q: %v", fid, err)
		}
	}

	fixturePath := filepath.Join("..", "testdata", "maestro.json")
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture %q: %v", fixturePath, err)
	}
	defer f.Close()

	// Wrap the maestro.Parse function into the coverage.Parser interface.
	runID, err := coverage.Ingest(ctx, s, coverage.ParseFunc(maestro.Parse), f, coverage.IngestOptions{})
	if err != nil {
		t.Fatalf("coverage.Ingest: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("Ingest returned runID=%d; expected positive", runID)
	}

	runs, err := s.Coverage().ListRuns(ctx, store.FrameworkMaestro)
	if err != nil {
		t.Fatalf("Coverage.ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns returned %d rows; want 1", len(runs))
	}
	got := runs[0]
	if got.Framework != store.FrameworkMaestro {
		t.Errorf("CoverageRun.Framework = %q; want %q", got.Framework, store.FrameworkMaestro)
	}

	// SummaryJSON holds pass/fail counts — unmarshal and assert.
	var summary struct {
		Pass int `json:"pass"`
		Fail int `json:"fail"`
	}
	if err := json.Unmarshal([]byte(got.SummaryJSON), &summary); err != nil {
		t.Fatalf("unmarshal SummaryJSON %q: %v", got.SummaryJSON, err)
	}
	if summary.Pass != 1 || summary.Fail != 1 {
		t.Errorf("SummaryJSON pass/fail = %d/%d; want 1/1", summary.Pass, summary.Fail)
	}

	results, err := s.Coverage().ListResults(ctx, runID)
	if err != nil {
		t.Fatalf("Coverage.ListResults: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("ListResults returned %d rows; want 2", len(results))
	}
}
