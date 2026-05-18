package ports

import "github.com/sosalejandro/atlas/internal/domain"

// RegistryReader loads registry data from persistent storage.
type RegistryReader interface {
	// LoadAll reads all domain files from the given directory and returns a populated Registry.
	LoadAll(dir string) (*domain.Registry, error)

	// LoadDomain reads a single domain file by name from the given directory.
	LoadDomain(dir, domainName string) (*domain.DomainFile, error)
}

// RegistryWriter persists registry data to storage.
type RegistryWriter interface {
	// SaveDomain writes a single domain file to the given directory.
	SaveDomain(dir string, df *domain.DomainFile) error

	// SaveAll writes all domain files in the registry to the given directory.
	SaveAll(dir string, reg *domain.Registry) error
}
