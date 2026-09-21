package onboard

import (
	"os"
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/shared"
	"github.com/sosalejandro/grunnr/packages/store"
)

// The words that started #177, taken from the cobra run recorded in the
// issue: TestNoFileCompletions, TestGenBashCompletionFile and friends gave
// grunnr provisional:root.no and provisional:root.bash, over nine and six
// symbols respectively. The rule that refuses them is "the word has to head
// at least two declarations", and these are the exact declarations cobra has.
func TestNameEarned_RefusesTheModifiersCobraProduced(t *testing.T) {
	// Real cobra declarations, shortened to the ones that decide each case.
	completions := []string{"GenBashCompletion", "GenBashCompletionFile", "NoFileCompletions"}
	flags := []string{"HasFlags", "ParseFlags", "ResetFlags", "LocalFlags"}

	if nameEarned(completions, "bash") {
		t.Errorf("\"bash\" was earned over %v -- GenBashCompletionFile is a FILE, not a bash", completions)
	}
	if nameEarned(completions, "gen") {
		t.Errorf("\"gen\" was earned over %v -- \"gen\" is a modifier, never a subject", completions)
	}
	if nameEarned(completions, "no") {
		t.Errorf("\"no\" was earned over %v", completions)
	}
	// ... and what survives, which is the half that has to keep working.
	if !nameEarned(completions, "completion") {
		t.Errorf("\"completion\" was refused over %v; two of them ARE completions", completions)
	}
	if !nameEarned(flags, "flag") {
		t.Errorf("\"flag\" was refused over %v; four of them ARE flags", flags)
	}
	// One witness is a function, two is a convention. Usage heads exactly one
	// declaration in cobra, and the measured cost of this line is that
	// root.usage is no longer proposed.
	if nameEarned([]string{"Usage", "UsageString", "SetUsageFunc"}, "usage") {
		t.Error("\"usage\" was earned from a single head witness")
	}
}

// attests matches on WORD boundaries. The substring version is what let the
// cluster word "no" claim Normalize -- "Normalize" contains the letters n-o,
// so a proposal called root.no owned a function that normalises things.
func TestAttests_MatchesWordsNotSubstrings(t *testing.T) {
	cases := []struct {
		name, word string
		want       bool
	}{
		{"Normalize", "no", false},
		{"NoFileCompletions", "no", true},
		{"HasFlags", "flag", true},  // naive plural, and nothing beyond it
		{"Flagship", "flag", false}, // one word, and it is not "flag"
		{"GenBashCompletionFile", "bash", true},
		{"Completion", "complete", false}, // no stemmer: a stemmer is a guess
	}
	for _, c := range cases {
		if got := attests(c.name, c.word); got != c.want {
			t.Errorf("attests(%q, %q) = %v, want %v", c.name, c.word, got, c.want)
		}
	}
}

func TestHeadWord(t *testing.T) {
	cases := map[string]string{
		"GenBashCompletionFile": "file",
		"NoFileCompletions":     "completions",
		"HasFlags":              "flags",
		"Settle":                "settle",
		"":                      "",
	}
	for in, want := range cases {
		if got := headWord(in); got != want {
			t.Errorf("headWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// namableDir is the other half of the refusal: capabilityIDFromDir hands back
// the literal "root.root" for the repository root -- a name no author typed,
// and on cobra the label over 83 symbols.
func TestNamableDir(t *testing.T) {
	cases := map[string]bool{
		"":                 false, // the repository root
		"internal/app":     false, // layout, not subject
		"src":              false,
		"packages/store":   true,
		"internal/cli":     true,
		"services/billing": true,
	}
	for dir, want := range cases {
		if got := namableDir(dir); got != want {
			t.Errorf("namableDir(%q) = %v, want %v", dir, got, want)
		}
	}
}

// unnamedFixture is one repository with both halves of #177 in it: a
// directory whose name is a subject, and symbols in the repository root that
// nothing can name.
func unnamedFixture() Input {
	return Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			// The heaviest file sorts LAST alphabetically, so a breakdown
			// ordered by path alone and one ordered by size disagree -- an
			// ordering assertion over a fixture where they agree asserts
			// nothing.
			sym(1, "main.Execute", "main.go"),
			sym(2, "main.Init", "alpha.go"),
			sym(3, "main.run", "version.go"),
			sym(4, "main.Version", "version.go"),
			sym(5, "billing.Settle", "internal/billing/settle.go"),
			sym(6, "billing.Refund", "internal/billing/refund.go"),
		},
	}
}

// The repository root is not a capability called "root.root". It is a
// grouping grunnr cannot name, and the honest report of it is its SIZE and its
// FILES (#177).
func TestInfer_RepositoryRootBecomesASizedUnnamedGrouping(t *testing.T) {
	res := Infer(unnamedFixture())

	for _, c := range res.Capabilities {
		if c.ID == "root.root" {
			t.Fatalf("root.root is still proposed as a capability: %+v", c)
		}
	}
	var u *Capability
	for i := range res.Capabilities {
		if !res.Capabilities[i].Named {
			u = &res.Capabilities[i]
		}
	}
	if u == nil {
		t.Fatalf("the repository root produced no unnamed grouping; got %v", capIDs(res))
	}
	if u.Symbols != 4 {
		t.Errorf("unnamed grouping holds %d symbols, want 4 -- a refusal with no size is a shrug", u.Symbols)
	}
	if u.UnnamedIndex != 1 || u.Ref() != "provisional:unnamed:1" {
		t.Errorf("unnamed grouping is addressed as %q (index %d), want provisional:unnamed:1",
			u.Ref(), u.UnnamedIndex)
	}
	want := []FileCount{
		{Path: "version.go", Symbols: 2},
		{Path: "alpha.go", Symbols: 1},
		{Path: "main.go", Symbols: 1},
	}
	if len(u.FileCounts) != len(want) {
		t.Fatalf("file breakdown = %+v, want %+v", u.FileCounts, want)
	}
	for i, w := range want {
		if u.FileCounts[i] != w {
			t.Errorf("file breakdown[%d] = %+v, want %+v (symbols desc, then path asc)",
				i, u.FileCounts[i], w)
		}
	}
	// The stats have to carry the same numbers, because the limits section
	// computes its percentage from them rather than from a literal.
	if res.Stats.UnnamedGroupings != 1 || res.Stats.UnnamedSymbols != 4 {
		t.Errorf("stats say %d groupings / %d symbols, want 1 / 4",
			res.Stats.UnnamedGroupings, res.Stats.UnnamedSymbols)
	}
	if res.Stats.NamedCapabilities != 1 {
		t.Errorf("NamedCapabilities = %d, want 1 (internal.billing)", res.Stats.NamedCapabilities)
	}
	if res.Stats.ProvisionalCapabilities != len(res.Capabilities) {
		t.Errorf("ProvisionalCapabilities = %d but the map holds %d entries; a consumer reading "+
			"it as the length of the array is now wrong",
			res.Stats.ProvisionalCapabilities, len(res.Capabilities))
	}
	// An unnamed grouping has no domain: counting one would re-introduce the
	// name the refusal exists to withhold.
	if res.Stats.Domains != 1 {
		t.Errorf("Domains = %d, want 1 -- only named entries have a domain", res.Stats.Domains)
	}
}

// Every named proposal outranks every unnamed grouping. A reader who meets a
// refusal first has nothing to do with it; one who meets it last has already
// seen what grunnr WAS willing to name.
func TestInfer_UnnamedGroupingsSortLast(t *testing.T) {
	res := Infer(unnamedFixture())
	seenUnnamed := false
	for _, c := range res.Capabilities {
		if !c.Named {
			seenUnnamed = true
			continue
		}
		if seenUnnamed {
			t.Fatalf("named proposal %s sorts after an unnamed grouping; order was %v",
				c.ID, refsOf(res))
		}
	}
}

// The invariants every consumer of this type relies on. Both halves matter:
// a named entry must be promotable, and an unnamed one must be IMPOSSIBLE to
// promote -- by grammar, since shared.ValidFeatureIDRe rejects the colon in
// "unnamed:1", rather than by a check somebody can forget to write.
func TestCapability_NamedAndUnnamedInvariants(t *testing.T) {
	res := Infer(unnamedFixture())
	if len(res.Capabilities) < 2 {
		t.Fatalf("fixture produced %d entries, want a named one and an unnamed one", len(res.Capabilities))
	}
	for _, c := range res.Capabilities {
		if c.Anchor == nil {
			t.Fatalf("%s has no anchor; promote --as needs a target even for a grouping grunnr would not name", c.Ref())
		}
		if c.Named {
			if !validID(c.ID) {
				t.Errorf("named capability id %q is not promotable", c.ID)
			}
			if c.Anchor.Annotation != "@atlas:feature "+c.ID {
				t.Errorf("%s would write %q, not its own annotation", c.ID, c.Anchor.Annotation)
			}
			continue
		}
		if c.ID != "" || c.Domain != "" {
			t.Errorf("unnamed grouping carries id %q / domain %q; it has neither", c.ID, c.Domain)
		}
		if c.UnnamedIndex < 1 {
			t.Errorf("unnamed grouping has index %d, so it cannot be addressed", c.UnnamedIndex)
		}
		if c.Anchor.Annotation != "" {
			t.Errorf("unnamed grouping would write %q -- there is no id to write", c.Anchor.Annotation)
		}
		// Both forms, because promotion's one job is to STRIP the
		// "provisional:" prefix: a handle that is only unpromotable while it
		// still wears the prefix is not protected by the grammar at all.
		for _, form := range []string{c.Ref(), strings.TrimPrefix(c.Ref(), ProvisionalPrefix)} {
			if shared.IsValidFeatureID(shared.FeatureID(form)) {
				t.Errorf("%q is a valid feature id, so the grammar no longer stops it being promoted", form)
			}
		}
	}
}

// A cluster whose word the production code does not corroborate dissolves,
// and its symbols fall through to the next stage UNTOUCHED. Dropping them
// instead would make the refusal cost the reader a chunk of their repository.
func TestInfer_UnearnedClusterDissolvesWithoutLosingSymbols(t *testing.T) {
	in := Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			// "bash" heads nothing: GenBashCompletionFile is a FILE.
			sym(1, "cmd.GenBashCompletionFile", "internal/cmd/completion.go"),
			sym(2, "cmd.GenBashCompletion", "internal/cmd/completion.go"),
			sym(3, "cmd.TestBashSubdirs", "internal/cmd/completion_test.go"),
			sym(4, "cmd.TestBashCompletions", "internal/cmd/completion_test.go"),
		},
	}
	res := Infer(in)
	for _, c := range res.Capabilities {
		if c.ID == "cmd.bash" {
			t.Fatalf("cmd.bash was proposed from a test-name prefix: %+v", c)
		}
	}
	d := findCap(t, res, "internal.cmd")
	if d.Symbols != 2 {
		t.Errorf("the dissolved cluster's symbols landed in %s as %d, want 2 -- they must fall "+
			"through, not disappear", d.ID, d.Symbols)
	}
	if res.Stats.SymbolsProposed != res.Stats.UndeclaredSymbols {
		t.Errorf("%d of %d undeclared symbols are in the map; a dissolved cluster must not cost "+
			"the reader symbols", res.Stats.SymbolsProposed, res.Stats.UndeclaredSymbols)
	}
}

// An earned cluster SHOWS its corroboration. Without the witnesses printed,
// the reader is back to trusting a prefix, which is what #177 was about.
func TestInfer_EarnedClusterCitesItsHeadWitnesses(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "cmd.HasFlags", "flags.go"),
			sym(2, "cmd.ParseFlags", "flags.go"),
			sym(3, "cmd.ResetFlags", "flags.go"),
			sym(4, "cmd.LocalFlags", "flags.go"),
			// Singular in the test names, plural in the code: the naive
			// plural rule is the whole of wordEq, and this is the case it
			// exists for.
			sym(5, "cmd.TestFlagOne", "flags_test.go"),
			sym(6, "cmd.TestFlagTwo", "flags_test.go"),
		},
	})
	c := findCap(t, res, "root.flag")
	detail := c.Evidence[0].Detail
	for _, want := range []string{"HasFlags", "ParseFlags", "ResetFlags", "and 1 more"} {
		if !strings.Contains(detail, want) {
			t.Errorf("cluster evidence does not cite %q: %s", want, detail)
		}
	}
	// The repository root renders as words, not as an empty string: with
	// dir=="" the old line read `6 tests in  lead with "Root"` -- two spaces,
	// and a title of `flag in `.
	if !strings.Contains(detail, "the repository root") {
		t.Errorf("cluster evidence names the directory as %q: %s", "", detail)
	}
	if strings.Contains(c.Title, "  ") || strings.HasSuffix(c.Title, " ") {
		t.Errorf("cluster title %q has the empty-directory seam in it", c.Title)
	}
}

// Renaming is the whole `promote --as` mechanism, and the only path by which
// an unnamed grouping ever acquires an id.
func TestCapability_Rename(t *testing.T) {
	res := Infer(unnamedFixture())
	var u Capability
	for _, c := range res.Capabilities {
		if !c.Named {
			u = c
		}
	}
	got, err := u.Rename("billing.checkout")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if !got.Named || got.ID != "billing.checkout" || got.Domain != "billing" {
		t.Errorf("Rename produced named=%v id=%q domain=%q", got.Named, got.ID, got.Domain)
	}
	if got.Anchor.Annotation != "@atlas:feature billing.checkout" {
		t.Errorf("Rename would write %q", got.Anchor.Annotation)
	}
	if got.Ref() != "provisional:billing.checkout" {
		t.Errorf("renamed Ref() = %q", got.Ref())
	}
	// Anchor is a POINTER, so a rename that wrote through it instead of
	// copying would reach back into the map the caller is still holding --
	// and the report the user is reading would acquire an annotation for a
	// grouping grunnr refused to name. The value fields are safe by Go's
	// value receiver; this one is not, which is why it is the one asserted.
	if u.Anchor.Annotation != "" {
		t.Errorf("Rename wrote %q through the anchor of the grouping it was called on",
			u.Anchor.Annotation)
	}
	if _, err := u.Rename("Checkout"); err == nil {
		t.Error("Rename accepted an id the annotation grammar rejects")
	}
	if _, err := u.Rename("checkout"); err == nil {
		t.Error("Rename accepted a single-segment id")
	}
}

// Promoting a grouping grunnr would not name is a SKIP, not an error: --all
// has to keep working and report this per entry rather than aborting.
func TestPromote_UnnamedGroupingIsSkippedNotWritten(t *testing.T) {
	root := t.TempDir()
	rel := "main.go"
	abs := writeFile(t, root, rel, "package main\n\nfunc Execute() {}\n")
	before, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}

	unnamed := Capability{
		Provisional: true, UnnamedIndex: 2, Symbols: 3,
		Anchor: &Anchor{FilePath: rel, Line: 3, Qualified: "main.Execute"},
	}
	named := capWithAnchor("orders.alpha", rel, 3)

	res, err := PromoteAll(root, []Capability{unnamed, named}, true)
	if err != nil {
		t.Fatalf("PromoteAll: %v", err)
	}
	if res[0].Applied {
		t.Error("an unnamed grouping was written into the user's source")
	}
	if res[0].Skipped == "" {
		t.Error("promoting an unnamed grouping neither wrote nor said why")
	}
	if !strings.Contains(res[0].Skipped, "--as") {
		t.Errorf("the skip does not say how to proceed: %q", res[0].Skipped)
	}
	if res[0].ID != "provisional:unnamed:2" {
		t.Errorf("skip is reported against %q, not the handle the report printed", res[0].ID)
	}
	// The batch is not aborted: the named capability beside it still lands.
	if !res[1].Applied {
		t.Errorf("a named capability in the same batch was not promoted: %+v", res[1])
	}
	after, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(after), "@atlas:feature"); n != 1 {
		t.Errorf("file carries %d annotations, want exactly the named one:\n%s\n(before:\n%s)",
			n, after, before)
	}
}

// Users paste back the handle they were shown, in either of the two forms the
// report prints it in.
func TestDocument_FindResolvesUnnamedHandles(t *testing.T) {
	doc := Document{Capabilities: []Capability{
		{ID: "orders.place", Provisional: true, Named: true},
		{Provisional: true, UnnamedIndex: 1, Symbols: 118},
		{Provisional: true, UnnamedIndex: 2, Symbols: 9},
	}}
	for _, id := range []string{"unnamed:2", ProvisionalPrefix + "unnamed:2"} {
		c, ok := doc.Find(id)
		if !ok {
			t.Fatalf("Find(%q) missed the grouping the report printed", id)
		}
		if c.UnnamedIndex != 2 || c.Symbols != 9 {
			t.Errorf("Find(%q) returned index %d (%d symbols), want 2 (9 symbols)",
				id, c.UnnamedIndex, c.Symbols)
		}
	}
	if _, ok := doc.Find("unnamed:7"); ok {
		t.Error("Find invented an unnamed grouping")
	}
}

// The refusal has to reach the reader BEFORE the map, and the limits section
// is where it goes (#177). With a percentage computed from this run's own
// counters, because a number nobody measured is the one thing this report may
// not contain.
func TestLimits_CouldNotNameIsStatedWithItsMeasuredSize(t *testing.T) {
	res := Infer(unnamedFixture())
	var l *Limit
	for i := range res.Limits {
		if res.Limits[i].Code == "could-not-name" {
			l = &res.Limits[i]
		}
	}
	if l == nil {
		t.Fatalf("no could-not-name limit; the refusal is only visible under the map: %+v", res.Limits)
	}
	// 4 of 6 undeclared symbols are in the grouping grunnr would not name.
	for _, want := range []string{"4 of 6", "67%", "1 grouping"} {
		if !strings.Contains(l.Detail, want) {
			t.Errorf("could-not-name limit does not say %q: %s", want, l.Detail)
		}
	}
	if !strings.Contains(l.Fix, "--as") {
		t.Errorf("could-not-name limit does not say how to name one: %q", l.Fix)
	}

	// A repository where grunnr can name everything must not be told it
	// refused to name something.
	clean := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "billing.Settle", "internal/billing/settle.go")},
	})
	for _, c := range clean.Limits {
		if c.Code == "could-not-name" {
			t.Errorf("a fully-named run still reports a refusal: %s", c.Detail)
		}
	}
}

// The limits section prints BEFORE the map (internal/cli/onboard.go), and
// this sentence said "above" from the day it was written.
//
// It also used to SUM the named proposals and the refused groupings into one
// count of "capabilities". That was changed, and this test with it: the
// header says "N named proposals + M unnamed groupings", and a bullet one
// line later saying "All N+M capabilities below" both contradicted it and
// called a grouping grunnr had just refused to name a capability. Summing
// them was chosen deliberately, to avoid under-reporting the map -- the
// right fix for that is to state BOTH numbers, which is what is asserted
// now, rather than to merge them under the wrong word.
func TestLimits_InferenceIsNotDeclarationCountsProposalsAndGroupingsSeparately(t *testing.T) {
	res := Infer(unnamedFixture())
	var l *Limit
	for i := range res.Limits {
		if res.Limits[i].Code == "inference-is-not-declaration" {
			l = &res.Limits[i]
		}
	}
	if l == nil {
		t.Fatal("the inference-is-not-declaration limit is gone; it is the one that says nothing was written")
	}
	if strings.Contains(l.Detail, "capabilities above") {
		t.Errorf("limit points the reader at a map that is printed below it: %s", l.Detail)
	}
	// The fixture is 1 named proposal + 1 refused grouping. Both must appear,
	// and only the first may be called a proposal.
	if !strings.Contains(l.Detail, "All 1 inferred proposals below") {
		t.Errorf("limit does not state the proposal count: %s", l.Detail)
	}
	if !strings.Contains(l.Detail, "One further grouping") {
		t.Errorf("limit drops the refused grouping, under-reporting the map: %s", l.Detail)
	}
	if strings.Contains(l.Detail, "All 2 capabilities") {
		t.Errorf("limit calls a refused grouping a capability, one line after "+
			"the header refused to name it: %s", l.Detail)
	}
}

func refsOf(res Result) []string {
	out := make([]string, 0, len(res.Capabilities))
	for _, c := range res.Capabilities {
		out = append(out, c.Ref())
	}
	return out
}

// TestUnclaimedMatching_RejectsSubstringsAtTheCallSite is the guard the issue
// is actually about, placed where the bug manifests.
//
// TestAttests_MatchesWordsNotSubstrings above exercises `attests` in
// isolation and is worth having, but it cannot fail when unclaimedMatching
// stops calling it. That was verified rather than assumed: reverting the call
// site to strings.Contains passes the entire suite with that test present, so
// the substring bug #177 names -- the cluster word "no" swallowing Normalize
// -- was unguarded at the only line where it ever appeared.
//
// This is the fourth test in a week found to be incapable of failing. The
// pattern in all four is the same: the assertion sits one layer away from the
// code that can break, so it keeps passing while the behaviour regresses.
func TestUnclaimedMatching_RejectsSubstringsAtTheCallSite(t *testing.T) {
	b := &builder{
		claimed: map[int64]bool{},
		prod: []store.SymbolRow{
			{ID: 1, QualifiedName: "repro.Normalize", FilePath: "repro/norm.go"},
			{ID: 2, QualifiedName: "repro.NoFileCompletions", FilePath: "repro/comp.go"},
		},
	}

	got := b.unclaimedMatching("repro", "no")

	var names []string
	for _, s := range got {
		names = append(names, string(s.QualifiedName))
	}
	if len(got) != 1 || names[0] != "repro.NoFileCompletions" {
		t.Errorf("unclaimedMatching(%q) = %v, want only repro.NoFileCompletions.\n"+
			"Normalize contains the letters n-o; a substring match claims it for a "+
			"capability called \"no\", which is the defect #177 exists to remove.", "no", names)
	}
}
