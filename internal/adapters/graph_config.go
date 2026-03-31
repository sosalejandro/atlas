package adapters

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sosalejandro/testreg/internal/ports"
	"gopkg.in/yaml.v3"
)

// GraphConfigFile represents the .testreg.yaml configuration file.
type GraphConfigFile struct {
	Graph GraphSection `yaml:"graph"`
}

// GraphSection holds the graph-specific configuration values.
type GraphSection struct {
	BackendRoot     string   `yaml:"backend_root"`
	RouterFile      string   `yaml:"router_file"`
	WireFile        string   `yaml:"wire_file"`
	SQLCConfig      string   `yaml:"sqlc_config"`
	FrontendRoots   []string `yaml:"frontend_roots"`
	IgnorePackages  []string `yaml:"ignore_packages"`
	IgnoreFunctions []string `yaml:"ignore_functions"`
	CacheDir        string   `yaml:"cache_dir"`
	MaxDepth        int      `yaml:"max_depth"`
}

// configFileName is the expected configuration file name.
const configFileName = ".testreg.yaml"

// LoadGraphConfig reads .testreg.yaml from projectRoot and returns the graph
// configuration section. If the file does not exist, it returns a GraphSection
// populated with sensible defaults. Any parse or I/O error (other than file
// not found) is returned as-is.
func LoadGraphConfig(projectRoot string) (*GraphSection, error) {
	cfgPath := filepath.Join(projectRoot, configFileName)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			defaults := defaultGraphSection()
			return &defaults, nil
		}
		return nil, fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	var file GraphConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", cfgPath, err)
	}

	section := applyDefaults(file.Graph)
	return &section, nil
}

// ToPortsConfig converts a GraphSection to the ports.GraphConfig type used by
// the application layer and domain services.
func (s *GraphSection) ToPortsConfig() ports.GraphConfig {
	return ports.GraphConfig{
		BackendRoot:     s.BackendRoot,
		RouterFile:      s.RouterFile,
		WireFile:        s.WireFile,
		SQLCConfig:      s.SQLCConfig,
		FrontendRoots:   s.FrontendRoots,
		IgnorePackages:  s.IgnorePackages,
		IgnoreFunctions: s.IgnoreFunctions,
		CacheDir:        s.CacheDir,
		MaxDepth:        s.MaxDepth,
	}
}

// defaultGraphSection returns a GraphSection with production-ready defaults.
func defaultGraphSection() GraphSection {
	return GraphSection{
		BackendRoot: "src",
		MaxDepth:    10,
		CacheDir:    ".testreg-cache",
	}
}

// applyDefaults fills zero-value fields in a parsed GraphSection with defaults.
func applyDefaults(s GraphSection) GraphSection {
	if s.BackendRoot == "" {
		s.BackendRoot = "src"
	}
	if s.MaxDepth <= 0 {
		s.MaxDepth = 10
	}
	if s.CacheDir == "" {
		s.CacheDir = ".testreg-cache"
	}
	return s
}
