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
	limits    Limits
}

// catalog is the ordered tool list. Order is stable so tools/list is
// reproducible; clients cache it and a reshuffle looks like a changed server.
//
// The descriptions are written for the model that has to CHOOSE between them.
// "Find a feature" would not distinguish find_feature from feature_surface;
// each one therefore says what question it answers and what it does NOT.
func (ts *toolset) catalog() []tool {
	out := ts.featureTools()
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
					"Exact feature id, as it appears in an @atlas:feature annotation. Use find_feature to get one.",
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
					"Fully qualified symbol name exactly as atlas indexed it, usually <import path or module>.<Name>.",
					"pkg/checkout.Pay", "src/api/handlers.LoginHandler"),
			}, "qualified_name"),
			Handle: ts.symbolInfo,
		},
		{
			Name:  "callers",
			Title: "Incoming call edges",
			Description: "List the symbols that call a symbol, each with the file:line of the call site. " +
				"This is the blast radius of a signature change. The result is capped: if it carries a " +
				"`truncated` block, there are MORE callers than you were shown.",
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
				"reflection is missing, so treat the list as a lower bound rather than the full behaviour.",
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
