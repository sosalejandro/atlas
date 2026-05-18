package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var initDiscover bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Bootstrap the test registry with template domain files",
	Long: `Creates the registry directory and populates it with template YAML files
containing feature definitions. If files already exist, new features are
merged without overwriting manual edits. This operation is idempotent.

With --discover, testreg parses the project's router file to discover
actual HTTP routes, groups them by module into features, and generates
registry YAML with real API surfaces. No annotations needed.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		store := adapters.NewYAMLStore()
		dir := resolvedRegistryDir()

		if initDiscover {
			return runDiscoverInit(store, dir)
		}

		useCase := app.NewInitRegistryUseCase(store, store)
		if err := useCase.Execute(dir); err != nil {
			return fmt.Errorf("initializing registry: %w", err)
		}

		fmt.Printf("Registry initialized at %s\n", dir)
		fmt.Println("Edit the YAML files to add your project's features and coverage data.")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initDiscover, "discover", false,
		"Auto-discover features from router file and project structure (no annotations needed)")
	rootCmd.AddCommand(initCmd)
}

// runDiscoverInit parses the project's router file to discover routes,
// groups them into features by module/package, and generates registry YAML
// with real API surfaces.
func runDiscoverInit(store *adapters.YAMLStore, registryDir string) error {
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		return fmt.Errorf("creating registry directory: %w", err)
	}

	projectRoot := resolvedProjectRoot()

	// Load graph config for router file path.
	graphSection, err := adapters.LoadGraphConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Parse routes from router file (and any files it delegates to).
	parser := adapters.NewRouteParser()
	var allRoutes []adapters.RouteMapping

	if graphSection.RouterFile != "" {
		routerPath := graphSection.RouterFile
		if !filepath.IsAbs(routerPath) {
			routerPath = filepath.Join(projectRoot, routerPath)
		}
		routes, err := parser.ParseDir(routerPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: route parsing failed for %s: %v\n", routerPath, err)
		} else {
			allRoutes = append(allRoutes, routes...)
		}
	}

	// Also scan all handler files for routes (Echo pattern: routes defined in *.handler.go files).
	backendRoot := graphSection.BackendRoot
	if backendRoot == "" {
		backendRoot = "src"
	}
	backendAbs := filepath.Join(projectRoot, backendRoot)

	filepath.WalkDir(backendAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// Parse any Go file that might contain route registrations.
		if strings.Contains(name, "handler") || strings.Contains(name, "route") || name == "handlers.go" {
			routes, parseErr := parser.Parse(path)
			if parseErr == nil {
				allRoutes = append(allRoutes, routes...)
			}
		}
		return nil
	})

	if len(allRoutes) == 0 {
		fmt.Fprintln(os.Stderr, "No routes discovered. Check that router_file is set in .testreg.yaml")
		fmt.Fprintln(os.Stderr, "or that handler files follow naming conventions (*handler*.go, *route*.go).")
		// Fall back to template init.
		useCase := app.NewInitRegistryUseCase(store, store)
		return useCase.Execute(registryDir)
	}

	// Deduplicate routes by method+path.
	seen := make(map[string]bool)
	var uniqueRoutes []adapters.RouteMapping
	for _, r := range allRoutes {
		key := r.Method + " " + r.Path
		if !seen[key] {
			seen[key] = true
			uniqueRoutes = append(uniqueRoutes, r)
		}
	}

	// Group routes into domains by module directory.
	domains := groupRoutesIntoDomains(uniqueRoutes)

	// Build registry.
	registry := &domain.Registry{}
	for _, df := range domains {
		registry.Domains = append(registry.Domains, df)
	}

	// Save.
	if err := store.SaveAll(registryDir, registry); err != nil {
		return fmt.Errorf("writing registry: %w", err)
	}

	// Print summary.
	totalFeatures := 0
	for _, df := range domains {
		totalFeatures += len(df.Features)
	}
	fmt.Printf("Registry initialized at %s\n", registryDir)
	fmt.Printf("Discovered %d routes → %d features across %d domains\n",
		len(uniqueRoutes), totalFeatures, len(domains))

	for _, df := range domains {
		fmt.Printf("  %s: %d features\n", df.Domain, len(df.Features))
		for _, f := range df.Features {
			surfaces := ""
			for _, api := range f.Surfaces.API {
				surfaces += fmt.Sprintf(" %s %s", api.Method, api.Path)
			}
			fmt.Printf("    %s —%s\n", f.ID, surfaces)
		}
	}

	return nil
}

// groupRoutesIntoDomains groups discovered routes into domain files based on
// the handler file's directory structure.
func groupRoutesIntoDomains(routes []adapters.RouteMapping) []domain.DomainFile {
	// Extract domain name from handler file path.
	// "server/modules/auth/auth.handler.go" → "auth"
	// "server/handlers/health/health.go" → "health"
	// "server/modules/interactions/enroll/enroll.handler.go" → "enroll"
	type featureGroup struct {
		domain   string
		routes   []adapters.RouteMapping
	}

	domainMap := make(map[string]*featureGroup)
	var domainOrder []string

	for _, r := range routes {
		d := deriveDomainFromFile(r.File)
		if d == "" {
			d = deriveDomainFromPath(r.Path)
		}

		if _, exists := domainMap[d]; !exists {
			domainMap[d] = &featureGroup{domain: d}
			domainOrder = append(domainOrder, d)
		}
		domainMap[d].routes = append(domainMap[d].routes, r)
	}

	sort.Strings(domainOrder)

	var result []domain.DomainFile
	for _, d := range domainOrder {
		fg := domainMap[d]
		df := domain.DomainFile{
			Domain:      d,
			Description: fmt.Sprintf("Auto-discovered from route registrations"),
		}

		// Each route becomes a feature with its handler name as the feature name.
		for _, r := range fg.routes {
			featureName := r.Handler
			if featureName == "" || featureName == "<func>" || featureName == "<unknown>" {
				featureName = sanitizeForID(r.Path)
			}

			featureID := d + "." + sanitizeForID(featureName)
			priority := inferPriority(r.Path, r.Method)

			f := domain.Feature{
				ID:       featureID,
				Name:     humanizeName(featureName),
				Priority: priority,
				Surfaces: domain.Surfaces{
					API: []domain.APISurface{{
						Method: r.Method,
						Path:   r.Path,
					}},
				},
			}

			df.Features = append(df.Features, f)
		}

		result = append(result, df)
	}

	return result
}

// deriveDomainFromFile extracts a domain name from a handler file path.
func deriveDomainFromFile(filePath string) string {
	if filePath == "" {
		return ""
	}
	// Normalize path.
	p := filepath.ToSlash(filePath)
	parts := strings.Split(p, "/")

	// Walk backwards to find a meaningful directory name.
	// Skip: server, handlers, modules, interactions, src, internal, cmd, pkg
	skip := map[string]bool{
		"server": true, "handlers": true, "modules": true, "interactions": true,
		"src": true, "internal": true, "cmd": true, "pkg": true,
		"infrastructure": true, "http": true, "api": true,
	}

	for i := len(parts) - 2; i >= 0; i-- {
		dir := parts[i]
		if !skip[dir] && dir != "" && !strings.HasSuffix(dir, ".go") {
			return strings.ToLower(dir)
		}
	}

	return ""
}

// deriveDomainFromPath extracts a domain from the route path.
// "/api/auth/google/login" → "auth"
func deriveDomainFromPath(routePath string) string {
	parts := strings.Split(strings.Trim(routePath, "/"), "/")
	// Skip "api" and version prefixes.
	for _, p := range parts {
		if p == "api" || p == "v1" || p == "v2" || strings.HasPrefix(p, ":") {
			continue
		}
		return strings.ToLower(p)
	}
	if len(parts) > 0 {
		return parts[0]
	}
	return "root"
}

// sanitizeForID converts a handler/path name to a valid feature ID component.
func sanitizeForID(name string) string {
	// Remove common prefixes and suffixes.
	name = strings.TrimPrefix(name, "get")
	name = strings.TrimPrefix(name, "Get")
	name = strings.TrimPrefix(name, "create")
	name = strings.TrimPrefix(name, "Create")
	name = strings.TrimPrefix(name, "update")
	name = strings.TrimPrefix(name, "Update")
	name = strings.TrimPrefix(name, "delete")
	name = strings.TrimPrefix(name, "Delete")

	if name == "" {
		return "root"
	}

	// Convert camelCase to kebab-case.
	var result []byte
	for i, c := range name {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				result = append(result, '-')
			}
			result = append(result, byte(c+32)) // lowercase
		} else if c == '/' || c == ':' || c == '{' || c == '}' {
			if len(result) > 0 && result[len(result)-1] != '-' {
				result = append(result, '-')
			}
		} else {
			result = append(result, byte(c))
		}
	}
	s := strings.Trim(string(result), "-")
	if s == "" {
		return "root"
	}
	return s
}

// humanizeName converts a handler function name to a readable feature name.
func humanizeName(name string) string {
	if name == "" {
		return "Unknown"
	}
	// Insert spaces before uppercase letters.
	var result []byte
	for i, c := range name {
		if c >= 'A' && c <= 'Z' && i > 0 {
			result = append(result, ' ')
		}
		result = append(result, byte(c))
	}
	s := string(result)
	// Capitalize first letter.
	if len(s) > 0 && s[0] >= 'a' && s[0] <= 'z' {
		s = string(s[0]-32) + s[1:]
	}
	return s
}

// inferPriority assigns a default priority based on route patterns.
func inferPriority(path, method string) domain.Priority {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "auth") || strings.Contains(lower, "login") {
		return domain.PriorityCritical
	}
	if strings.Contains(lower, "health") || strings.Contains(lower, "admin") {
		return domain.PriorityCritical
	}
	if method == "POST" || method == "PUT" || method == "DELETE" {
		return domain.PriorityHigh
	}
	return domain.PriorityMedium
}
