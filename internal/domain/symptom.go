package domain

import "regexp"

// SymptomRule maps error patterns to likely failure layers.
type SymptomRule struct {
	Pattern     string   // regex pattern to match symptom text
	Layer       string   // "backend-auth", "backend-routing", "backend-bug", "frontend", "infra", "data"
	Description string   // human-readable explanation
	CheckOrder  []string // node kinds to check first, e.g. ["service", "repository"]
}

// DefaultSymptomRules returns the built-in symptom matching rules.
// Rules are ordered from most specific to least specific to support
// first-match semantics in MatchSymptom.
func DefaultSymptomRules() []SymptomRule {
	return []SymptomRule{
		{
			Pattern:     `(?i)(401|unauthorized|unauthenticated)`,
			Layer:       "backend-auth",
			Description: "Authentication failure: request lacks valid credentials or session has expired",
			CheckOrder:  []string{"handler", "service", "external"},
		},
		{
			Pattern:     `(?i)(403|forbidden|permission denied|access denied)`,
			Layer:       "backend-auth",
			Description: "Authorization failure: authenticated user lacks required permissions",
			CheckOrder:  []string{"handler", "service"},
		},
		{
			Pattern:     `(?i)(404|not found|no route|route not matched)`,
			Layer:       "backend-routing",
			Description: "Routing failure: endpoint does not exist or resource was not found",
			CheckOrder:  []string{"endpoint", "handler", "repository"},
		},
		{
			Pattern:     `(?i)(500|internal server error|panic|runtime error|nil pointer)`,
			Layer:       "backend-bug",
			Description: "Server-side crash or unhandled error in business logic",
			CheckOrder:  []string{"service", "repository", "handler"},
		},
		{
			Pattern:     `(?i)(selector.*not found|element not found|no element|getBy.*failed|queryBy.*null)`,
			Layer:       "frontend",
			Description: "UI element missing: component not rendered or selector has changed",
			CheckOrder:  []string{"component", "hook"},
		},
		{
			Pattern:     `(?i)(timeout|timed? ?out|deadline exceeded|context deadline|ETIMEDOUT)`,
			Layer:       "infra",
			Description: "Operation exceeded time limit: network, database, or service latency",
			CheckOrder:  []string{"external", "repository", "service"},
		},
		{
			Pattern:     `(?i)(login failed|invalid credentials|wrong password|authentication error)`,
			Layer:       "backend-auth",
			Description: "Login flow failure: credentials rejected or auth service unavailable",
			CheckOrder:  []string{"service", "handler", "external"},
		},
		{
			Pattern:     `(?i)(connection refused|ECONNREFUSED|dial tcp|network unreachable|ENETUNREACH)`,
			Layer:       "infra",
			Description: "Service unreachable: target server is down or network path is broken",
			CheckOrder:  []string{"external", "repository"},
		},
		{
			Pattern:     `(?i)(empty response|no data|null response|empty body|content.length.*0)`,
			Layer:       "data",
			Description: "Response contained no data: query returned nothing or serialization failed",
			CheckOrder:  []string{"repository", "query", "service"},
		},
	}
}

// MatchSymptom finds the best matching rule for a symptom string.
// Returns nil if no rule matches.
func MatchSymptom(symptom string, rules []SymptomRule) *SymptomRule {
	for i := range rules {
		re, err := regexp.Compile(rules[i].Pattern)
		if err != nil {
			// Skip malformed patterns rather than crashing.
			continue
		}
		if re.MatchString(symptom) {
			return &rules[i]
		}
	}
	return nil
}
