package redact

import "testing"

func TestExports_EveryEntryIsUsable(t *testing.T) {
	if len(Exports()) == 0 {
		t.Fatal("the export catalogue is empty")
	}
	valid := map[Class]bool{
		ClassPath: true, ClassIdentifier: true, ClassSourceText: true,
		ClassUserText: true, ClassEnum: true, ClassDigest: true,
	}
	for _, e := range Exports() {
		if e.Surface == "" {
			t.Errorf("export %+v has no surface name", e)
		}
		if e.Destination == "" {
			t.Errorf("export %q has no destination", e.Surface)
		}
		if e.Note == "" {
			t.Errorf("export %q has no note; the report prints it", e.Surface)
		}
		for _, c := range e.Classes {
			if !valid[c] {
				t.Errorf("export %q claims unknown class %q", e.Surface, c)
			}
		}
	}
}

func TestExportsFor_MatchesWholeVerbSegments(t *testing.T) {
	if got := ExportsFor(""); len(got) != len(Exports()) {
		t.Errorf("ExportsFor(\"\") returned %d entries, want all %d", len(got), len(Exports()))
	}
	got := ExportsFor("report")
	if len(got) == 0 {
		t.Fatalf("ExportsFor(%q) found nothing", "report")
	}
	for _, e := range got {
		if e.Verb[:len("report ")] != "report " {
			t.Errorf("ExportsFor(\"report\") returned %q", e.Verb)
		}
	}
	if got := ExportsFor("cov r"); len(got) != 0 {
		t.Errorf("ExportsFor(%q) matched mid-segment: %+v", "cov r", got)
	}
	if got := ExportsFor("nosuchverb"); len(got) != 0 {
		t.Errorf("ExportsFor on an unknown verb returned %+v", got)
	}
}

// TestExports_DoNotClaimNetworkDestinations pins the central claim of
// docs/security.md into a test. If a future export ever writes to a URL,
// this fails and the statement has to be rewritten before it ships.
func TestExports_DoNotClaimNetworkDestinations(t *testing.T) {
	for _, e := range Exports() {
		for _, marker := range []string{"http://", "https://", "upload", "api.", "://"} {
			if containsFold(e.Destination, marker) {
				t.Errorf("export %q has destination %q, which looks like egress",
					e.Surface, e.Destination)
			}
		}
	}
}

// exportFor returns the catalogue entry for one exact verb.
func exportFor(t *testing.T, verb string) Export {
	t.Helper()
	for _, e := range Exports() {
		if e.Verb == verb {
			return e
		}
	}
	t.Fatalf("the export catalogue has no entry for %q", verb)
	return Export{}
}

// TestExports_MCPDeclaresTheSourceTextItReturns.
//
// packages/mcp returns features.Title from find_feature, feature_surface and
// feature_health, and this package's own registry classifies features.title
// as source-text: the title is lifted out of the annotation comment, not
// typed by the operator. Declaring only structural classes here understated
// the ONE surface where indexed content routinely reaches a third-party
// model provider, which is the direction of error this catalogue exists to
// prevent.
func TestExports_MCPDeclaresTheSourceTextItReturns(t *testing.T) {
	var titleIsSourceText bool
	for _, c := range Columns() {
		if c.Table == "features" && c.Name == "title" {
			titleIsSourceText = c.Class == ClassSourceText
		}
	}
	if !titleIsSourceText {
		t.Fatal("features.title is no longer classified source-text; " +
			"revisit the mcp entry before changing this test")
	}
	e := exportFor(t, "mcp")
	var declared bool
	for _, c := range e.Classes {
		if c == ClassSourceText {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the mcp export declares %v; it returns features.title, which is %q",
			e.Classes, ClassSourceText)
	}
	if !containsFold(e.Note, "features.title") {
		t.Error("the mcp note does not name features.title, so `source-text` reads as " +
			"worse than it is -- no doc comment, signature or query text reaches an MCP tool")
	}
}

// TestExports_EveryRepositoryWriterIsListed.
//
// `atlas cov shim init` writes a generated source file into the working tree
// via runner.InitDir, and `atlas onboard promote --apply` writes an
// annotation into a source file, so "migrate-annotations is the only command
// that writes to the working tree" was false twice over. A data-handling
// statement that under-reports what a tool writes is a liability, and this
// check is what keeps the correction from rotting back.
func TestExports_EveryRepositoryWriterIsListed(t *testing.T) {
	for _, verb := range []string{"cov shim init", "onboard promote", "migrate-annotations"} {
		e := exportFor(t, verb)
		if !containsFold(e.Destination, "repository") {
			t.Errorf("the %q entry does not say it writes into the repository: %q",
				verb, e.Destination)
		}
	}
	if cls := exportFor(t, "cov shim init").Classes; len(cls) != 0 {
		t.Errorf("cov shim init classes = %v; the generated file carries no indexed content", cls)
	}
	// The promoted annotation carries an id atlas inferred, so unlike the
	// other two this write does put something derived into your source.
	promote := exportFor(t, "onboard promote")
	if len(promote.Classes) != 1 || promote.Classes[0] != ClassIdentifier {
		t.Errorf("onboard promote classes = %v, want just %q", promote.Classes, ClassIdentifier)
	}

	migrate := exportFor(t, "migrate-annotations")
	if containsFold(migrate.Note, "the only command that writes") {
		t.Errorf("the migrate-annotations note still claims to be the only writer: %q",
			migrate.Note)
	}
	for _, other := range []string{"cov shim init", "onboard promote"} {
		if !containsFold(migrate.Note, other) {
			t.Errorf("the migrate-annotations note does not name %q as another writer: %q",
				other, migrate.Note)
		}
	}
}

func containsFold(hay, needle string) bool {
	if len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if equalFold(hay[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
