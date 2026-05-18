package patterns_test

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
)

// TestIntegration_NutritionMeasurements runs the recognisers against the
// real nutrition-v2-go measurements BC if it is present on the host. The
// test is intentionally NON-fatal when the path is missing — Atlas CI does
// not check out the nutrition repo, so the integration check would always
// fail otherwise.
//
// Calibration contract (per the Phase 6f spec):
//   - at least 1 outbox-append match
//   - at least 1 event-recorder-embed match
//   - at least 1 canonical-service match
//
// Measurements is the smallest BC and only carries a single aggregate, so
// 1-match minimums are the right floor here. The full-tree calibration
// (across all BCs) is recorded in the PR description.
func TestIntegration_NutritionMeasurements(t *testing.T) {
	root := nutritionMeasurementsPath()
	if root == "" {
		t.Skip("nutrition-v2-go measurements BC not present; skipping integration calibration")
	}

	var inputs []patterns.FileInput
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			t.Logf("parse %s: %v", path, perr)
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		inputs = append(inputs, patterns.FileInput{
			File:    file,
			FSet:    fset,
			RelPath: filepath.ToSlash(rel),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(inputs) == 0 {
		t.Fatalf("no .go files under %s", root)
	}

	matches, err := patterns.MatchAllFiles(context.Background(), patterns.Config{}, inputs)
	if err != nil {
		t.Fatalf("MatchAllFiles: %v", err)
	}

	counts := map[string]int{}
	for _, m := range matches {
		counts[m.Pattern]++
	}
	t.Logf("calibration counts in measurements BC: %+v", counts)

	for _, p := range patterns.KnownPatterns {
		if counts[p] < 1 {
			t.Errorf("recogniser %q reported 0 matches against measurements BC — recogniser likely broken", p)
		}
	}
}

// nutritionMeasurementsPath returns the absolute path to the
// nutrition-v2-go measurements BC if it is reachable from the developer
// machine, otherwise "". Probes a few well-known locations relative to
// the developer's HOME (matching the project_worktree_port_allocations
// convention).
func nutritionMeasurementsPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(home, "Documents/startup-projects/nutrition-v2-go/src/contexts/measurements"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}
