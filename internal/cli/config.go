package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the in-memory shape of `.atlas.yaml` plus a few computed
// fields. Every field is optional; zero-valued fields fall back to the
// defaults documented in docs/onboarding.md.
type Config struct {
	DBPath string       `yaml:"db_path"`
	Scan   ScanConfig   `yaml:"scan"`
	Audit  AuditConfig  `yaml:"audit"`
	Sprint SprintConfig `yaml:"sprint"`

	// repoRoot is the directory used as the anchor for DBPath. Computed
	// from `git rev-parse --show-toplevel`, falling back to cwd.
	repoRoot string
}

// ScanConfig mirrors the `scan:` block.
type ScanConfig struct {
	SkipDirs []string `yaml:"skip_dirs"`
	SkipTS   bool     `yaml:"skip_ts"`
}

// AuditConfig mirrors the `audit:` block.
type AuditConfig struct {
	FreshnessWindowDays      int `yaml:"freshness_window_days"`
	ContractDriftWindowDays  int `yaml:"contract_drift_window_days"`
}

// SprintConfig mirrors the `sprint:` block.
type SprintConfig struct {
	DefaultTopN int `yaml:"default_top_n"`
}

// defaultConfig returns the baseline configuration the CLI uses when no
// `.atlas.yaml` is present. Keep in sync with docs/onboarding.md.
func defaultConfig() Config {
	return Config{
		DBPath: filepath.Join(".atlas", "atlas.db"),
		Scan: ScanConfig{
			SkipDirs: []string{"vendor", "node_modules", "dist", "build"},
			SkipTS:   false,
		},
		Audit: AuditConfig{
			FreshnessWindowDays:     30,
			ContractDriftWindowDays: 30,
		},
		Sprint: SprintConfig{
			DefaultTopN: 10,
		},
	}
}

// loadConfig resolves the Atlas configuration from disk. The lookup order:
//
//  1. If --config (configPath) is non-empty, that file must exist; an
//     I/O error is fatal.
//  2. Otherwise look for `.atlas.yaml` at the repo root (the directory
//     `git rev-parse --show-toplevel` reports). If git isn't available
//     OR the binary is not run inside a repo, fall back to cwd.
//  3. If no config file exists, return the defaults — running without a
//     config is a supported workflow.
//
// The returned Config has its `repoRoot` field populated so per-verb
// commands can resolve relative DB paths consistently.
func loadConfig(configPath string) (Config, error) {
	cfg := defaultConfig()
	cfg.repoRoot = findRepoRoot()

	// Explicit --config: must exist.
	if configPath != "" {
		if err := readConfigFile(configPath, &cfg); err != nil {
			return Config{}, fmt.Errorf("load config %s: %w", configPath, err)
		}
		return cfg, nil
	}

	// Implicit: try `.atlas.yaml` at the repo root.
	candidate := filepath.Join(cfg.repoRoot, ".atlas.yaml")
	if err := readConfigFile(candidate, &cfg); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("load config %s: %w", candidate, err)
	}
	return cfg, nil
}

// readConfigFile parses path into cfg. Existing fields on cfg act as the
// defaults — yaml.Unmarshal only sets fields the file explicitly mentions.
func readConfigFile(path string, cfg *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err //nolint:wrapcheck // caller wraps with operation context.
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("yaml unmarshal %s: %w", path, err)
	}
	return nil
}

// findRepoRoot returns the absolute directory that should anchor every
// relative path resolution. Prefers `git rev-parse --show-toplevel`,
// falls back to cwd.
func findRepoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s
		}
	}
	cwd, err := os.Getwd()
	if err == nil {
		return cwd
	}
	return "."
}

// resolveDBPath returns the absolute on-disk path to the Atlas SQLite
// state file. The lookup order:
//
//  1. --db-path flag override (cliOverride), if non-empty.
//  2. Config DBPath, joined with repoRoot when it is relative.
//
// The parent directory is created on demand so callers don't have to
// guard against ENOENT separately.
func resolveDBPath(cfg Config, cliOverride string) (string, error) {
	path := cliOverride
	if path == "" {
		path = cfg.DBPath
	}
	if path == "" {
		path = filepath.Join(".atlas", "atlas.db")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.repoRoot, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("ensure db parent dir: %w", err)
	}
	return path, nil
}

// freshnessWindow returns the audit FreshnessWindow as a time.Duration.
func (c Config) freshnessWindow() time.Duration {
	d := c.Audit.FreshnessWindowDays
	if d <= 0 {
		d = 30
	}
	return time.Duration(d) * 24 * time.Hour
}

// contractDriftWindow returns the audit ContractDriftWindow as a time.Duration.
func (c Config) contractDriftWindow() time.Duration {
	d := c.Audit.ContractDriftWindowDays
	if d <= 0 {
		d = 30
	}
	return time.Duration(d) * 24 * time.Hour
}
