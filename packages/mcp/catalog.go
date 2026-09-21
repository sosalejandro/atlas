package mcp

// toolset binds the read-only index to the tools that expose it.
//
// Every field is a read interface (see index.go). There is no path from a tool
// handler to a write, which is the property that makes this surface safe to
// hand an agent that will occasionally call things it invented.
type toolset struct {
	graph     GraphIndex
	coverage  CoverageIndex
	scorer    Scorer
	freshness FreshnessFunc
	doctor    DoctorFunc
	limits    Limits
}

// catalog is the ordered tool list. Order is stable so tools/list is
// reproducible; clients cache it and a reshuffle looks like a changed server.
//
// The descriptions are written for the model that has to CHOOSE between them.
// "Find a feature" would not distinguish find_feature from feature_surface;
// each one therefore says what question it answers and what it does NOT.
func (ts *toolset) catalog() []tool {
	// Health first, and not for neatness: it is the question that should
	// precede every other one. A coverage number from a three-commit-stale
	// index is about code that no longer exists, and an agent that reads the
	// catalogue top-down should meet "is this trustworthy?" before it meets
	// anything that returns a number.
	out := ts.healthTools()
	out = append(out, ts.featureTools()...)
	out = append(out, ts.graphTools()...)
	return append(out, ts.coverageTools()...)
}

// featureTools answer "what is this capability, and what implements it".
func (ts *toolset) featureTools() []tool {
	return []tool{
		{
			Name:  "find_feature",
			Title: "Find features",
			Description: "Search the indexed features (capabilities) by id or title, case-insensitively. " +
				"Start here when you know what a capability is called but not where it lives. " +
				"Returns ids you then pass to feature_surface or coverage_for. Does NOT return code.",
			InputSchema: objectSchema(map[string]any{
				"query": stringProp(
					"Substring matched case-insensitively against both the feature id and its title.",
					"checkout", "payment", "auth.login"),
				"limit": limitProp(ts.limits.MaxFeatures, "features"),
			}, "query"),
			Handle: ts.findFeature,
		},
		{
			Name:  "feature_surface",
			Title: "Feature implementation surface",
			Description: "List the symbols that implement a feature, with the provenance of the derivation. " +
				"ALWAYS read `surface_source`: `dynamic` is execution evidence and can be trusted, `static` is a " +
				"call-edge walk and is a lower bound, `package-anchor` is whole-package granularity, and " +
				"`direct-links` is only what a human annotated. A surface read without its source will be over-trusted.",
			InputSchema: objectSchema(map[string]any{
				"feature_id": stringProp(
					"Exact feature id, as it appears in an @grunnr:feature annotation. Use find_feature to get one.",
					"checkout.pay"),
				"limit": limitProp(ts.limits.MaxSymbols, "symbols"),
			}, "feature_id"),
			Handle: ts.featureSurface,
		},
	}
}

// graphTools answer "where is this symbol, and what is connected to it".
func (ts *toolset) graphTools() []tool {
	return []tool{
		{
			Name:  "symbol_info",
			Title: "Symbol declaration and ownership",
			Description: "Look up one symbol by fully qualified name: kind, file, line span, package, the features " +
				"it is annotated for, and how many DISTINCT symbols call it and it calls. Use it to locate a symbol " +
				"before reading or editing it, and to see at a glance how connected it is. The counts are of " +
				"symbols, not of call sites, so they are usually smaller than the row count of callers/callees.",
			InputSchema: objectSchema(map[string]any{
				"qualified_name": stringProp(
					"Fully qualified symbol name exactly as grunnr indexed it, usually <import path or module>.<Name>.",
					"pkg/checkout.Pay", "src/api/handlers.LoginHandler"),
			}, "qualified_name"),
			Handle: ts.symbolInfo,
		},
		{
			Name:  "callers",
			Title: "Incoming call edges",
			Description: "List the symbols that call a symbol, each with the file:line of the call site. " +
				"This is the blast radius of a signature change. The result is capped: if it carries a " +
				"`truncated` block, there are MORE callers than you were shown. " +
				"Every row carries `resolution_tier`, which is how grunnr established that edge, and you " +
				"must read it before acting on the row: `typed` and `name_resolved` bound a name to a " +
				"declaration grunnr indexed, but `syntactic` is a GUESS from the shape of the source -- " +
				"the caller it names may not exist, or may be the wrong one of several with the same " +
				"name. `ambiguous: true` means more than one candidate matched and grunnr picked one. " +
				"Editing a syntactic or ambiguous caller without checking the file first is how you " +
				"change code nobody asked you to touch. The `provenance` block totals this for the rows " +
				"returned.",
			InputSchema: objectSchema(map[string]any{
				"qualified_name": stringProp("Fully qualified name of the symbol being called.", "pkg/checkout.Pay"),
				"limit":          limitProp(ts.limits.MaxEdges, "call edges"),
			}, "qualified_name"),
			Handle: ts.callers,
		},
		{
			Name:  "callees",
			Title: "Outgoing call edges",
			Description: "List the symbols a symbol calls, each with the file:line of the call site. " +
				"Only statically resolved calls appear: anything reached through an interface, a DI container or " +
				"reflection is missing, so treat the list as a lower bound rather than the full behaviour. " +
				"Every row carries `resolution_tier`, which is how grunnr established that edge, and you " +
				"must read it before acting on the row: `typed` and `name_resolved` bound a name to a " +
				"declaration grunnr indexed, but `syntactic` is a GUESS from the shape of the source -- " +
				"the callee it names may not exist, or may be the wrong one of several with the same " +
				"name. `ambiguous: true` means more than one candidate matched and grunnr picked one. " +
				"The `provenance` block totals this for the rows returned. A list that is a lower bound " +
				"AND partly guessed is not a basis for concluding what this symbol does.",
			InputSchema: objectSchema(map[string]any{
				"qualified_name": stringProp("Fully qualified name of the calling symbol.", "pkg/checkout.Pay"),
				"limit":          limitProp(ts.limits.MaxEdges, "call edges"),
			}, "qualified_name"),
			Handle: ts.callees,
		},
	}
}

// coverageTools answer "what verifies this, and how well".
func (ts *toolset) coverageTools() []tool {
	return []tool{
		{
			Name:  "tests_covering",
			Title: "Tests that executed a symbol",
			Description: "Name the tests that actually executed a symbol on the current coverage frontier, with the " +
				"statements each one covered. This is recorded execution, not a name-matching guess. Requires a " +
				"per-test coverage ingest; the answer says so explicitly when one is missing.",
			InputSchema: objectSchema(map[string]any{
				"qualified_name": stringProp("Fully qualified name of the symbol to find tests for.", "pkg/checkout.Pay"),
				"limit":          limitProp(ts.limits.MaxTests, "tests"),
			}, "qualified_name"),
			Handle: ts.testsCovering,
		},
		{
			Name:  "coverage_for",
			Title: "Feature health on the current frontier",
			Description: "Report a feature's health score and its component signals from the current coverage " +
				"frontier, together with the surface the coverage was measured over and which runs produced it. " +
				"Returns a structured `no_data` when no coverage has been ingested rather than a score of zero.",
			InputSchema: objectSchema(map[string]any{
				"feature_id": stringProp("Exact feature id. Use find_feature to get one.", "checkout.pay"),
			}, "feature_id"),
			Handle: ts.coverageFor,
		},
	}
}

// healthTools answer "is the index trustworthy right now".
func (ts *toolset) healthTools() []tool {
	return []tool{
		{
			Name:  "doctor",
			Title: "Is the index trustworthy right now",
			Description: "Ask this FIRST, before any tool that returns a number. It reports whether " +
				"grunnr's picture of the repo is still true: a stale index answers confidently about " +
				"code that no longer exists, and nothing in a coverage or call-graph result reveals " +
				"that on its own. " +
				"Each finding carries a `severity` (ok / n/a / warn / fail) and, where grunnr knows a " +
				"command that addresses it, a `fixes` entry with an `argv` you can run and a " +
				"`mutates_index` flag saying whether it writes. " +
				"A finding with NO `fixes` is not mechanically fixable -- running a scan at it will " +
				"not help, and the `remediation` prose says what a person has to decide. " +
				"Apply only fixes grunnr offered here; do not invent commands. Then re-run this tool, " +
				"and STOP after two rounds that do not reduce the finding count -- a diagnosis that " +
				"does not move is telling you the remedy is not one grunnr has.",
			InputSchema: objectSchema(map[string]any{}),
			Handle:      ts.doctorReport,
		},
	}
}
