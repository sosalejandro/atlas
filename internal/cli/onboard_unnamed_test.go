package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/onboard"
)

// withUnnamedRoot adds two declarations at the repository ROOT, which is the
// shape grunnr refuses to name (#177): capabilityIDFromDir would call them
// "root.root", a name no author typed.
func withUnnamedRoot(t *testing.T, fix *onboardFixture) {
	t.Helper()
	body := `package demo

// Execute runs the demo.
func Execute() error { return nil }

// Version reports the build.
func Version() string { return "0.0.1" }
`
	if err := os.WriteFile(filepath.Join(fix.root, "demo.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The ordering half of #177, and the reason it is a blocker rather than a
// papercut: the section that volunteers what grunnr cannot see is the reason
// to trust the rest, and a reader who meets the map first has already judged
// the tool by its weakest entry.
func TestOnboard_LimitsComeBeforeTheMap(t *testing.T) {
	fix := newOnboardFixture(t)
	withUnnamedRoot(t, fix)
	stdout, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	found := strings.Index(stdout, "WHAT GRUNNR FOUND")
	limits := strings.Index(stdout, "WHAT GRUNNR CANNOT SEE")
	mapAt := strings.Index(stdout, "PROVISIONAL CAPABILITY MAP")
	next := strings.Index(stdout, "\nNEXT\n")
	for name, at := range map[string]int{
		"WHAT GRUNNR FOUND": found, "WHAT GRUNNR CANNOT SEE": limits,
		"PROVISIONAL CAPABILITY MAP": mapAt, "NEXT": next,
	} {
		if at < 0 {
			t.Fatalf("the report has no %q section:\n%s", name, stdout)
		}
	}
	if !(found < limits && limits < mapAt && mapAt < next) {
		t.Errorf("section order is FOUND=%d CANNOT SEE=%d MAP=%d NEXT=%d; the honesty section "+
			"must reach the reader before the proposals:\n%s", found, limits, mapAt, next, stdout)
	}
	// And the refusal itself is in that section, not under the map.
	if !strings.Contains(stdout[limits:mapAt], "could not name") {
		t.Errorf("the refusal is not stated above the map:\n%s", stdout[limits:mapAt])
	}
}

// The header is the line most readers judge the map by. "25 capabilities
// across 2 domains" was true of the old cobra run and still read as
// twenty-five useful things, eleven of which were words scraped off test
// names.
func TestOnboard_HeaderSplitsNamedFromRefused(t *testing.T) {
	fix := newOnboardFixture(t)
	withUnnamedRoot(t, fix)
	stdout, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	var header string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "PROVISIONAL ") && strings.Contains(line, "proposal") {
			header = line
		}
	}
	if header == "" {
		t.Fatalf("no PROVISIONAL header line in:\n%s", stdout)
	}
	for _, want := range []string{"named proposal", "unnamed grouping", "undeclared symbols"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q does not say %q", header, want)
		}
	}
	if strings.Contains(header, "capabilities across") {
		t.Errorf("header still counts refused groupings as capabilities: %q", header)
	}
}

// A refusal with no shape is a shrug. The map's third section has to carry
// the size, the file breakdown and the command that resolves it, or the
// reader is left with a grouping they cannot act on.
func TestOnboard_UnnamedSectionCarriesItsBreakdownAndItsCommand(t *testing.T) {
	fix := newOnboardFixture(t)
	withUnnamedRoot(t, fix)
	stdout, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	// Searched from the map heading: the limits section names this section
	// too ("listed as ... in the map below"), and matching that would pin
	// the wrong string.
	mapAt := strings.Index(stdout, "PROVISIONAL CAPABILITY MAP")
	if mapAt < 0 {
		t.Fatalf("no map in:\n%s", stdout)
	}
	rel := strings.Index(stdout[mapAt:], "groupings grunnr would not name")
	if rel < 0 {
		t.Fatalf("the map has no section for the groupings grunnr refused to name:\n%s", stdout)
	}
	at := mapAt + rel
	section := stdout[at:]
	for _, want := range []string{
		"provisional:unnamed:1",
		"--as <your.feature.id>",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the unnamed section does not carry %q:\n%s", want, section)
		}
	}
	// The breakdown is a COUNT beside a path, not merely the path: the
	// evidence line above it already cites demo.go as a position, so
	// matching the filename alone would pass with no breakdown printed at
	// all. demo.go holds Execute and Version.
	breakdown := false
	for _, line := range strings.Split(section, "\n") {
		if strings.Join(strings.Fields(line), " ") == "2 demo.go" {
			breakdown = true
		}
	}
	if !breakdown {
		t.Errorf("the unnamed section has no per-file breakdown (want a line \"2  demo.go\"):\n%s", section)
	}
	// It prints LAST: a reader meets what grunnr was willing to name first.
	if strings.Index(stdout, "from code structure and test names") > at {
		t.Error("the refused groupings print above the named proposals")
	}
	// And the map says what it is, so a list of ids under a heading is not
	// mistaken for a registry.
	if !strings.Contains(stdout, "Nothing here is in the registry") {
		t.Errorf("the map does not say what it is:\n%s", stdout)
	}
}

// The #118 seam: an agent (or a person) reads provisional:unnamed:1 plus its
// file breakdown, picks a name, and promote --as writes THAT name.
func TestOnboardPromote_AsNamesAnUnnamedGrouping(t *testing.T) {
	fix := newOnboardFixture(t)
	withUnnamedRoot(t, fix)
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}

	// Without --as the grouping is refused rather than promoted, and the
	// refusal says how to proceed.
	out := runPromote(t, fix, "--id", "unnamed:1")
	if !strings.Contains(out, "unnamed grouping") || !strings.Contains(out, "--as") {
		t.Errorf("promoting an unnamed grouping did not explain itself:\n%s", out)
	}
	body, err := os.ReadFile(filepath.Join(fix.root, "demo.go"))
	if err != nil {
		t.Fatal(err)
	}
	if marker, wrote := anyFeatureAnnotation(string(body)); wrote {
		t.Fatalf("a dry run wrote %s into the source:\n%s", marker, body)
	}

	out = runPromote(t, fix, "--id", "unnamed:1", "--as", "demo.lifecycle", "--apply")
	body, err = os.ReadFile(filepath.Join(fix.root, "demo.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "// @grunnr:feature demo.lifecycle") {
		t.Errorf("promote --as wrote nothing the scanner will read:\n%s\ncommand output:\n%s", body, out)
	}
}

// --as is refused on a proposal grunnr DID name: the user is reading a map
// that says provisional:internal.billing, and silently writing a different id
// for it would make the report in front of them wrong.
func TestOnboardPromote_AsIsRefusedOnNamedProposalsAndOnBadIDs(t *testing.T) {
	fix := newOnboardFixture(t)
	withUnnamedRoot(t, fix)
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	doc, err := onboard.Load(fix.root)
	if err != nil {
		t.Fatal(err)
	}
	var named string
	for _, c := range doc.Capabilities {
		if c.Named {
			named = c.ID
			break
		}
	}
	if named == "" {
		t.Fatal("the fixture produced no named proposal to test the refusal against")
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"named proposal", []string{"--id", named, "--as", "demo.lifecycle"}, "named proposal"},
		{"invalid id", []string{"--id", "unnamed:1", "--as", "Lifecycle"}, "not a valid feature id"},
		{"single segment", []string{"--id", "unnamed:1", "--as", "lifecycle"}, "not a valid feature id"},
		{"with --all", []string{"--all", "--as", "demo.lifecycle"}, "exactly one --id"},
	}
	for _, c := range cases {
		err := promoteErr(t, fix, c.args...)
		if err == nil {
			t.Errorf("%s: promote --as succeeded", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not say %q", c.name, err, c.want)
		}
	}
	body, err := os.ReadFile(filepath.Join(fix.root, "demo.go"))
	if err != nil {
		t.Fatal(err)
	}
	if marker, wrote := anyFeatureAnnotation(string(body)); wrote {
		t.Errorf("a refused promote --as wrote %s into the source:\n%s", marker, body)
	}
}

func runPromote(t *testing.T, fix *onboardFixture, args ...string) string {
	t.Helper()
	out, err := promoteOut(t, fix, args...)
	if err != nil {
		t.Fatalf("promote %v: %v\n%s", args, err, out)
	}
	return out
}

func promoteErr(t *testing.T, fix *onboardFixture, args ...string) error {
	t.Helper()
	_, err := promoteOut(t, fix, args...)
	return err
}

func promoteOut(t *testing.T, fix *onboardFixture, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	full := append([]string{"onboard", "promote", "--root", fix.root, "--db-path", fix.dbPath}, args...)
	root.SetArgs(full)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// anyFeatureAnnotation reports whether src carries a feature annotation in
// ANY spelling the parser accepts, and which one.
//
// The negative assertions above -- "a dry run wrote nothing", "a refused
// promote wrote nothing" -- are only meaningful if they cover the whole
// grammar. Naming a single prefix makes them pass whenever promote emits the
// other one, which is precisely the change that has just been made.
func anyFeatureAnnotation(src string) (string, bool) {
	for _, m := range []string{"@grunnr:feature", "@atlas:feature", "@testreg"} {
		if strings.Contains(src, m) {
			return m, true
		}
	}
	return "", false
}
