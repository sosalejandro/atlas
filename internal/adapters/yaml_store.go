package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sosalejandro/testreg/internal/domain"
	"gopkg.in/yaml.v3"
)

// YAMLStore implements RegistryReader and RegistryWriter using YAML files on disk.
// Each domain is stored as a separate YAML file named <domain>.yaml.
type YAMLStore struct{}

// NewYAMLStore creates a new YAMLStore.
func NewYAMLStore() *YAMLStore {
	return &YAMLStore{}
}

// LoadAll reads all .yaml files from the given directory and returns a populated Registry.
func (s *YAMLStore) LoadAll(dir string) (*domain.Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &domain.Registry{}, nil
		}
		return nil, fmt.Errorf("reading registry directory %s: %w", dir, err)
	}

	reg := &domain.Registry{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		df, loadErr := s.loadFile(filePath)
		if loadErr != nil {
			return nil, fmt.Errorf("loading %s: %w", filePath, loadErr)
		}
		reg.Domains = append(reg.Domains, *df)
	}

	return reg, nil
}

// LoadDomain reads a single domain file by name from the given directory.
func (s *YAMLStore) LoadDomain(dir, domainName string) (*domain.DomainFile, error) {
	filePath := filepath.Join(dir, domainName+".yaml")
	return s.loadFile(filePath)
}

// SaveDomain writes a single domain file to the given directory.
// Uses file locking to prevent corruption from concurrent CLI calls.
func (s *YAMLStore) SaveDomain(dir string, df *domain.DomainFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	filePath := filepath.Join(dir, df.Domain+".yaml")
	return s.writeFile(filePath, df)
}

// SaveAll writes all domain files in the registry to the given directory.
func (s *YAMLStore) SaveAll(dir string, reg *domain.Registry) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	for i := range reg.Domains {
		if err := s.SaveDomain(dir, &reg.Domains[i]); err != nil {
			return err
		}
	}

	return nil
}

func (s *YAMLStore) loadFile(filePath string) (*domain.DomainFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}

	var df domain.DomainFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil, fmt.Errorf("parsing YAML in %s: %w", filePath, err)
	}

	// Validate loaded data
	if df.Domain == "" {
		return nil, fmt.Errorf("file %s: missing required 'domain' field", filePath)
	}
	for i, f := range df.Features {
		if f.ID == "" {
			return nil, fmt.Errorf("file %s: feature at index %d missing required 'id' field", filePath, i)
		}
		if err := f.Priority.Validate(); err != nil {
			return nil, fmt.Errorf("file %s: feature %q: %w", filePath, f.ID, err)
		}
	}

	return &df, nil
}

func (s *YAMLStore) writeFile(filePath string, df *domain.DomainFile) error {
	// Write to a temporary file first, then rename for atomicity
	tmpPath := filePath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("creating temp file %s: %w", tmpPath, err)
	}

	// Acquire an exclusive lock on the temp file to prevent concurrent writes
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("acquiring lock on %s: %w", tmpPath, err)
	}

	encoder := yaml.NewEncoder(f)
	encoder.SetIndent(2)

	if err := encoder.Encode(df); err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encoding YAML to %s: %w", filePath, err)
	}

	if err := encoder.Close(); err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("closing encoder for %s: %w", filePath, err)
	}

	// Release lock and close
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, filePath, err)
	}

	return nil
}
