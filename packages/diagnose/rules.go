package diagnose

import "regexp"

// SymptomRule maps a regex pattern over the symptom text to a layer hint,
// a preferred CheckOrder of symbol kinds, and a confidence multiplier the
// scorer folds into the kind-bonus for matching symbols.
//
// The set ported from legacy testreg lives in DefaultSymptomRules. Callers
// that want to extend or replace the rule set construct their own slice and
// pass it via DiagnoseOptions.Rules; the package treats rules as data, not
// behaviour.
type SymptomRule struct {
	// Pattern is a Go regexp source. Patterns SHOULD be (?i) case-insensitive
	// because symptom text comes from a heterogeneous mix of sources
	// (HTTP responses, panic strings, browser DevTools, CI logs).
	Pattern string

	// Layer is the broad-strokes category of failure. Currently advisory
	// (returned in Match.Reason); the scorer does NOT use it directly.
	Layer string

	// Description is the human-readable hint emitted when this rule matches.
	Description string

	// CheckOrder is the ordered list of symbol kinds to bias toward. A
	// candidate symbol whose kind appears earlier in this slice receives a
	// larger kind-bonus than one appearing later.
	CheckOrder []string

	// Confidence is the rule author's prior on how diagnostic the pattern
	// is. Folded into the kind-bonus as a linear multiplier; never used as
	// the final Match.Confidence on its own.
	Confidence float64
}

// matchedRule is the runtime form: a SymptomRule plus its precompiled
// pattern, so the scorer doesn't recompile per candidate.
type matchedRule struct {
	rule SymptomRule
	re   *regexp.Regexp
}

// DefaultSymptomRules returns the built-in rule registry. Patterns and
// confidences are ported 1:1 from legacy testreg
// (internal/domain/symptom.go) so the migration's "audit parity" gate
// stays honest.
//
// Ordering does NOT matter — MatchSymptom sorts by Confidence descending
// before returning. The list is kept in approximate confidence order
// purely for readability.
//
//nolint:funlen // this is a static table, not a logical function;
// splitting it into helper builders adds boilerplate without making the
// rules easier to read or audit.
func DefaultSymptomRules() []SymptomRule {
	return []SymptomRule{
		// --- High confidence: specific error patterns ---
		{
			Pattern:     `(?i)(unique constraint|duplicate key|duplicate entry|violates unique)`,
			Layer:       "data",
			Description: "Duplicate record: insert or update violates a uniqueness constraint",
			CheckOrder:  []string{"repository", "query", "service"},
			Confidence:  0.95,
		},
		{
			Pattern:     `(?i)(foreign key.*violat|referential integrity|fk constraint)`,
			Layer:       "data",
			Description: "Referential integrity violation: referenced record does not exist or is being deleted",
			CheckOrder:  []string{"repository", "query", "service"},
			Confidence:  0.95,
		},
		{
			Pattern:     `(?i)(deadlock detected|deadlock found)`,
			Layer:       "data",
			Description: "Database deadlock: concurrent transactions competing for the same rows",
			CheckOrder:  []string{"repository", "service"},
			Confidence:  0.95,
		},
		{
			Pattern:     `(?i)(sql:\s*no rows|no rows in result|record not found)`,
			Layer:       "data",
			Description: "Query returned no rows: expected record does not exist",
			CheckOrder:  []string{"repository", "query", "service"},
			Confidence:  0.90,
		},
		{
			Pattern:     `(?i)(json:\s*cannot unmarshal|json.*unmarshal|invalid character.*looking for)`,
			Layer:       "backend-bug",
			Description: "JSON deserialization failure: response or request body does not match expected structure",
			CheckOrder:  []string{"handler", "service", "external"},
			Confidence:  0.90,
		},
		{
			Pattern:     `(?i)(login failed|invalid credentials|wrong password|authentication error)`,
			Layer:       "backend-auth",
			Description: "Login flow failure: credentials rejected or auth service unavailable",
			CheckOrder:  []string{"service", "handler", "external"},
			Confidence:  0.90,
		},
		{
			Pattern:     `(?i)(connection refused|ECONNREFUSED|dial tcp|network unreachable|ENETUNREACH)`,
			Layer:       "infra",
			Description: "Service unreachable: target server is down or network path is broken",
			CheckOrder:  []string{"external", "repository"},
			Confidence:  0.90,
		},
		{
			Pattern:     `(?i)(CORS|origin not allowed|cross-origin|access-control-allow-origin)`,
			Layer:       "infra",
			Description: "Cross-origin request blocked: CORS policy misconfiguration",
			CheckOrder:  []string{"handler", "external"},
			Confidence:  0.90,
		},
		{
			Pattern:     `(?i)(selector.*not found|element not found|no element|getBy.*failed|queryBy.*null)`,
			Layer:       "frontend",
			Description: "UI element missing: component not rendered or selector has changed",
			CheckOrder:  []string{"component", "hook"},
			Confidence:  0.85,
		},
		{
			Pattern:     `(?i)(TypeError|Cannot read propert|undefined is not|is not a function)`,
			Layer:       "frontend",
			Description: "JavaScript runtime error: accessing property on undefined/null or calling non-function",
			CheckOrder:  []string{"component", "hook", "service"},
			Confidence:  0.85,
		},
		{
			Pattern:     `(?i)(hydration.*mismatch|hydration.*failed|text content does not match)`,
			Layer:       "frontend",
			Description: "SSR hydration mismatch: server-rendered HTML differs from client render",
			CheckOrder:  []string{"component", "hook"},
			Confidence:  0.85,
		},
		{
			Pattern:     `(?i)(tls|certificate|x509|cert.*expir|ssl)`,
			Layer:       "infra",
			Description: "TLS/certificate error: invalid, expired, or untrusted certificate",
			CheckOrder:  []string{"external"},
			Confidence:  0.85,
		},

		// --- Medium-high confidence ---
		{
			Pattern:     `(?i)(no route|route not matched|route not found|endpoint.*not found)`,
			Layer:       "backend-routing",
			Description: "Route not registered: the HTTP endpoint does not exist in the router",
			CheckOrder:  []string{"endpoint", "handler"},
			Confidence:  0.85,
		},
		{
			Pattern:     `(?i)(409|conflict)`,
			Layer:       "data",
			Description: "Resource conflict: duplicate resource or optimistic locking failure",
			CheckOrder:  []string{"repository", "service", "handler"},
			Confidence:  0.80,
		},
		{
			Pattern:     `(?i)(422|unprocessable|validation failed|invalid.*field|missing.*required)`,
			Layer:       "backend-bug",
			Description: "Validation failure: request data does not meet business rules",
			CheckOrder:  []string{"handler", "service"},
			Confidence:  0.80,
		},
		{
			Pattern:     `(?i)(429|rate.?limit|too many requests|throttl)`,
			Layer:       "infra",
			Description: "Rate limit exceeded: too many requests to the service or upstream",
			CheckOrder:  []string{"handler", "external"},
			Confidence:  0.80,
		},
		{
			Pattern:     `(?i)(502|bad gateway)`,
			Layer:       "infra",
			Description: "Upstream service error: reverse proxy received an invalid response",
			CheckOrder:  []string{"external", "handler"},
			Confidence:  0.80,
		},
		{
			Pattern:     `(?i)(503|service unavailable)`,
			Layer:       "infra",
			Description: "Service unavailable: server overloaded, in maintenance, or dependency down",
			CheckOrder:  []string{"external", "service"},
			Confidence:  0.80,
		},
		{
			Pattern:     `(?i)(context canceled|request canceled|client disconnected)`,
			Layer:       "infra",
			Description: "Request canceled: client disconnected or upstream context was canceled",
			CheckOrder:  []string{"service", "external"},
			Confidence:  0.75,
		},
		{
			Pattern:     `(?i)(EOF|unexpected EOF|broken pipe|connection reset)`,
			Layer:       "infra",
			Description: "Connection dropped: remote end closed the connection mid-transfer",
			CheckOrder:  []string{"external", "repository", "service"},
			Confidence:  0.75,
		},

		// --- Medium confidence ---
		{
			Pattern:     `(?i)(401|unauthorized|unauthenticated)`,
			Layer:       "backend-auth",
			Description: "Authentication failure: request lacks valid credentials or session has expired",
			CheckOrder:  []string{"handler", "service", "external"},
			Confidence:  0.70,
		},
		{
			Pattern:     `(?i)(403|forbidden|permission denied|access denied)`,
			Layer:       "backend-auth",
			Description: "Authorization failure: authenticated user lacks required permissions",
			CheckOrder:  []string{"handler", "service"},
			Confidence:  0.70,
		},
		{
			Pattern:     `(?i)(404|not found)`,
			Layer:       "backend-routing",
			Description: "Not found: endpoint does not exist or requested resource is missing",
			CheckOrder:  []string{"endpoint", "handler", "repository"},
			Confidence:  0.55,
		},
		{
			Pattern:     `(?i)(timeout|timed? ?out|deadline exceeded|context deadline|ETIMEDOUT)`,
			Layer:       "infra",
			Description: "Operation exceeded time limit: network, database, or service latency",
			CheckOrder:  []string{"external", "repository", "service"},
			Confidence:  0.60,
		},

		// --- Lower confidence ---
		{
			Pattern:     `(?i)(500|internal server error|panic|runtime error|nil pointer)`,
			Layer:       "backend-bug",
			Description: "Server-side crash or unhandled error in business logic",
			CheckOrder:  []string{"service", "repository", "handler"},
			Confidence:  0.50,
		},
		{
			Pattern:     `(?i)(empty response|no data|null response|empty body|content.length.*0)`,
			Layer:       "data",
			Description: "Response contained no data: query returned nothing or serialization failed",
			CheckOrder:  []string{"repository", "query", "service"},
			Confidence:  0.50,
		},
	}
}

// matchRules compiles and evaluates rules against the symptom, returning
// the matching set sorted by descending Confidence. Rules whose Pattern
// fails to compile are silently skipped — this is a registry lookup, not
// a hot path; a malformed user-supplied rule should not poison the entire
// diagnose call.
func matchRules(symptom string, rules []SymptomRule) []matchedRule {
	out := make([]matchedRule, 0, len(rules))
	for i := range rules {
		re, err := regexp.Compile(rules[i].Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(symptom) {
			out = append(out, matchedRule{rule: rules[i], re: re})
		}
	}
	// Insertion sort: the list is tiny (≤ #rules) and the sort.Slice import
	// is not worth pulling for this.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].rule.Confidence > out[j-1].rule.Confidence; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
