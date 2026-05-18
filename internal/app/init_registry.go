package app

import (
	"fmt"
	"os"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// InitRegistryUseCase bootstraps the registry directory with template domain files.
type InitRegistryUseCase struct {
	reader ports.RegistryReader
	writer ports.RegistryWriter
}

// NewInitRegistryUseCase creates a new InitRegistryUseCase.
func NewInitRegistryUseCase(reader ports.RegistryReader, writer ports.RegistryWriter) *InitRegistryUseCase {
	return &InitRegistryUseCase{reader: reader, writer: writer}
}

// Execute creates the registry directory and writes template domain files.
// If files already exist, it merges new features without overwriting manual edits.
func (uc *InitRegistryUseCase) Execute(registryDir string) error {
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		return fmt.Errorf("creating registry directory %s: %w", registryDir, err)
	}

	template := buildTemplateRegistry()

	// Check for existing registry
	existing, loadErr := uc.reader.LoadAll(registryDir)
	if loadErr != nil || existing == nil || len(existing.Domains) == 0 {
		// Fresh init — write everything
		if err := uc.writer.SaveAll(registryDir, template); err != nil {
			return fmt.Errorf("writing template registry: %w", err)
		}
		return nil
	}

	// Merge: add new domains and features that don't exist yet
	merged := mergeRegistries(existing, template)
	if err := uc.writer.SaveAll(registryDir, merged); err != nil {
		return fmt.Errorf("writing merged registry: %w", err)
	}

	return nil
}

// mergeRegistries adds domains and features from template into existing,
// without overwriting any existing feature data.
func mergeRegistries(existing, template *domain.Registry) *domain.Registry {
	existingDomains := make(map[string]*domain.DomainFile)
	for i := range existing.Domains {
		existingDomains[existing.Domains[i].Domain] = &existing.Domains[i]
	}

	for _, td := range template.Domains {
		ed, found := existingDomains[td.Domain]
		if !found {
			// New domain — add it whole
			existing.Domains = append(existing.Domains, td)
			continue
		}

		// Merge features: add missing ones
		existingFeatures := make(map[string]bool)
		for _, f := range ed.Features {
			existingFeatures[f.ID] = true
		}
		for _, tf := range td.Features {
			if !existingFeatures[tf.ID] {
				ed.Features = append(ed.Features, tf)
			}
		}
	}

	return existing
}

// buildTemplateRegistry returns a starter registry with example domains.
// A real deployment would populate this with all 183 features from the inventory.
func buildTemplateRegistry() *domain.Registry {
	return &domain.Registry{
		Domains: []domain.DomainFile{
			{
				Domain:      "auth",
				Description: "Authentication and authorization features",
				Features: []domain.Feature{
					{
						ID:          "auth.login",
						Name:        "User Login",
						Description: "Email/password authentication with JWT tokens",
						Roles:       []string{"patient", "nutritionist", "admin"},
						Priority:    domain.PriorityCritical,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/login", Component: "LoginPage"},
							Mobile: &domain.MobileSurface{Screen: "LoginScreen"},
							API:    []domain.APISurface{{Method: "POST", Path: "/api/v1/auth/login"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
					{
						ID:          "auth.register",
						Name:        "User Registration",
						Description: "New account creation with email verification",
						Roles:       []string{"patient", "nutritionist"},
						Priority:    domain.PriorityCritical,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/register", Component: "RegisterPage"},
							Mobile: &domain.MobileSurface{Screen: "RegisterScreen"},
							API:    []domain.APISurface{{Method: "POST", Path: "/api/v1/auth/register"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
					{
						ID:          "auth.logout",
						Name:        "User Logout",
						Description: "Session termination and token revocation",
						Roles:       []string{"patient", "nutritionist", "admin"},
						Priority:    domain.PriorityHigh,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/", Component: "NavBar"},
							Mobile: &domain.MobileSurface{Screen: "SettingsScreen"},
							API:    []domain.APISurface{{Method: "POST", Path: "/api/v1/auth/logout"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
					{
						ID:          "auth.password-reset",
						Name:        "Password Reset",
						Description: "Forgot password flow with email reset link",
						Roles:       []string{"patient", "nutritionist", "admin"},
						Priority:    domain.PriorityHigh,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/forgot-password", Component: "ForgotPasswordPage"},
							Mobile: &domain.MobileSurface{Screen: "ForgotPasswordScreen"},
							API: []domain.APISurface{
								{Method: "POST", Path: "/api/v1/auth/forgot-password"},
								{Method: "POST", Path: "/api/v1/auth/reset-password"},
							},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
				},
			},
			{
				Domain:      "meals",
				Description: "Meal planning, logging, and nutrition tracking",
				Features: []domain.Feature{
					{
						ID:          "meals.log",
						Name:        "Log Meal",
						Description: "Record a meal with food items and nutritional data",
						Roles:       []string{"patient"},
						Priority:    domain.PriorityCritical,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/meals/new", Component: "MealLogPage"},
							Mobile: &domain.MobileSurface{Screen: "MealLogScreen"},
							API:    []domain.APISurface{{Method: "POST", Path: "/api/v1/meals"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
					{
						ID:          "meals.history",
						Name:        "Meal History",
						Description: "View past meals with date filtering and search",
						Roles:       []string{"patient", "nutritionist"},
						Priority:    domain.PriorityHigh,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/meals", Component: "MealHistoryPage"},
							Mobile: &domain.MobileSurface{Screen: "MealHistoryScreen"},
							API:    []domain.APISurface{{Method: "GET", Path: "/api/v1/meals"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
				},
			},
			{
				Domain:      "profile",
				Description: "User profile management and settings",
				Features: []domain.Feature{
					{
						ID:          "profile.view",
						Name:        "View Profile",
						Description: "Display user profile information and health data",
						Roles:       []string{"patient", "nutritionist"},
						Priority:    domain.PriorityMedium,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/profile", Component: "ProfilePage"},
							Mobile: &domain.MobileSurface{Screen: "ProfileScreen"},
							API:    []domain.APISurface{{Method: "GET", Path: "/api/v1/profile"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
					{
						ID:          "profile.edit",
						Name:        "Edit Profile",
						Description: "Update personal information, preferences, and health goals",
						Roles:       []string{"patient", "nutritionist"},
						Priority:    domain.PriorityMedium,
						Surfaces: domain.Surfaces{
							Web:    &domain.WebSurface{Route: "/profile/edit", Component: "EditProfilePage"},
							Mobile: &domain.MobileSurface{Screen: "EditProfileScreen"},
							API:    []domain.APISurface{{Method: "PUT", Path: "/api/v1/profile"}},
						},
						Coverage: domain.Coverage{
							Unit: domain.UnitCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
								Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
								Mobile:  &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							Integration: domain.IntegrationCoverage{
								Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
							},
							E2E: domain.E2ECoverage{
								Web:    &domain.E2ECoverageEntry{Status: domain.StatusMissing},
								Mobile: &domain.E2ECoverageEntry{Status: domain.StatusMissing},
							},
						},
					},
				},
			},
		},
	}
}
