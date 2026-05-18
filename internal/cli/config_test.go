package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultConfig_Shape confirms the baseline values match the
// docs/onboarding.md contract — these are what every fresh project gets
// before a `.atlas.yaml` is written.
func TestDefaultConfig_Shape(t *testing.T) {
	cfg := defaultConfig()
	if cfg.DBPath != filepath.Join(".atlas", "atlas.db") {
		t.Fatalf("DBPath default = %q", cfg.DBPath)
	}
	if cfg.Audit.FreshnessWindowDays != 30 {
		t.Fatalf("freshness default = %d", cfg.Audit.FreshnessWindowDays)
	}
	if cfg.Sprint.DefaultTopN != 10 {
		t.Fatalf("sprint default top = %d", cfg.Sprint.DefaultTopN)
	}
	want := []string{"vendor", "node_modules", "dist", "build"}
	if len(cfg.Scan.SkipDirs) != len(want) {
		t.Fatalf("skip dirs = %v", cfg.Scan.SkipDirs)
	}
}

// TestConfig_WindowAccessors validates the freshness/contractDrift
// accessors return the right time.Durations even when the underlying
// `_days` integer is zero (where the accessor must fall back to 30).
func TestConfig_WindowAccessors(t *testing.T) {
	cfg := defaultConfig()
	if got := cfg.freshnessWindow(); got != 30*24*time.Hour {
		t.Fatalf("freshness default = %v", got)
	}
	if got := cfg.contractDriftWindow(); got != 30*24*time.Hour {
		t.Fatalf("contract-drift default = %v", got)
	}
	cfg.Audit.FreshnessWindowDays = 7
	if got := cfg.freshnessWindow(); got != 7*24*time.Hour {
		t.Fatalf("freshness 7d = %v", got)
	}
}

// TestLoadConfig_ExplicitFile reads a hand-crafted .atlas.yaml from a
// tempdir via the --config seam. The result should pick up the explicit
// fields and leave the rest at defaults.
func TestLoadConfig_ExplicitFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.yaml")
	body := `
db_path: my-state.db
scan:
  skip_dirs: [foo, bar]
  skip_ts: true
audit:
  freshness_window_days: 7
sprint:
  default_top_n: 5
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DBPath != "my-state.db" {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
	if cfg.Audit.FreshnessWindowDays != 7 {
		t.Fatalf("freshness = %d", cfg.Audit.FreshnessWindowDays)
	}
	if cfg.Sprint.DefaultTopN != 5 {
		t.Fatalf("top_n = %d", cfg.Sprint.DefaultTopN)
	}
	if !cfg.Scan.SkipTS {
		t.Fatal("skip_ts should be true")
	}
	if len(cfg.Scan.SkipDirs) != 2 || cfg.Scan.SkipDirs[0] != "foo" {
		t.Fatalf("skip_dirs = %v", cfg.Scan.SkipDirs)
	}
	// untouched fields keep their defaults
	if cfg.Audit.ContractDriftWindowDays != 0 && cfg.Audit.ContractDriftWindowDays != 30 {
		t.Fatalf("contract-drift = %d", cfg.Audit.ContractDriftWindowDays)
	}
}

// TestLoadConfig_MissingExplicitFatal confirms an explicit --config that
// points at a non-existent file errors out (not a silent default).
func TestLoadConfig_MissingExplicitFatal(t *testing.T) {
	_, err := loadConfig("/non/existent/path/atlas.yaml")
	if err == nil {
		t.Fatal("expected error for missing explicit config")
	}
}

// TestResolveDBPath_Override checks the precedence rules:
//  1. flag override wins
//  2. otherwise config DBPath (joined with repoRoot when relative)
//  3. ensures the parent directory exists on return
func TestResolveDBPath_Override(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.repoRoot = dir

	// 1) flag override.
	override := filepath.Join(dir, "custom", "atlas.db")
	got, err := resolveDBPath(cfg, override)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != override {
		t.Fatalf("got %q want %q", got, override)
	}
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}

	// 2) config-relative.
	cfg2 := defaultConfig()
	cfg2.repoRoot = dir
	cfg2.DBPath = "relative/state.db"
	got2, err := resolveDBPath(cfg2, "")
	if err != nil {
		t.Fatalf("resolve2: %v", err)
	}
	want2 := filepath.Join(dir, "relative", "state.db")
	if got2 != want2 {
		t.Fatalf("got %q want %q", got2, want2)
	}
}
