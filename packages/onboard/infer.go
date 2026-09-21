package onboard

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sosalejandro/grunnr/packages/store"
)

// Infer derives the provisional capability map and the first-run findings
// from one scan's worth of evidence.
//
// It is a pure function over values, which is the structural half of the
// "inferred is not declared" guarantee: with no store handle in scope, no
// code path here can write the registry. See the package doc.
func Infer(in Input) Result {
	b := newBuilder(in)
	b.markDeclared()
	b.claimRoutes()
	b.claimTestClusters()
	b.claimDirectories()
	b.enrich()

	res := b.assemble()
	res.Findings = findings(in, res)
	res.Limits = limits(in, res, b.unresolvedRoutes)
	return res
}

// builder holds the claim state while the grouping stages run. Each stage
// claims symbols the earlier ones left, so the stages read in
// strongest-signal-first order and a symbol lands in exactly one proposal.
type builder struct {
	in   Input
	byID map[int64]store.SymbolRow

	prod  []store.SymbolRow
	tests []store.SymbolRow

	// claimed covers both declared symbols (claimed by their annotation)
	// and symbols an earlier stage took.
	claimed map[int64]bool

	caps  []*Capability
	capBy map[string]*Capability

	// declared is the subset of claimed that an annotation speaks for, kept
	// apart from claimed so the map's coverage denominator counts what a
	// proposal COULD have taken rather than what one did.
	declared map[int64]bool

	// testDirs are the directories that hold at least one test file -- the
	// weak "a test lives near this" evidence used when no coverage run has
	// been ingested.
	testDirs map[string]bool

	declaredSymbols  int
	unresolvedRoutes int
}

func newBuilder(in Input) *builder {
	b := &builder{
		in:       in,
		byID:     make(map[int64]store.SymbolRow, len(in.Symbols)),
		claimed:  map[int64]bool{},
		capBy:    map[string]*Capability{},
		testDirs: map[string]bool{},
		declared: map[int64]bool{},
	}
	for _, s := range in.Symbols {
		b.byID[s.ID] = s
		if excludedPath(s.FilePath) {
			continue
		}
		if isTestPath(s.FilePath) {
			b.tests = append(b.tests, s)
			b.testDirs[dirOf(s.FilePath)] = true
			continue
		}
		b.prod = append(b.prod, s)
	}
	sortSymbols(b.prod)
	sortSymbols(b.tests)
	return b
}

// markDeclared claims every symbol that already belongs to a human-written
// feature. Those symbols are then invisible to every grouping stage, which
// is how "existing annotations are adopted, never duplicated" is enforced:
// a proposal cannot contain a symbol somebody already spoke for.
func (b *builder) markDeclared() {
	for _, f := range b.in.Declared {
		for _, id := range f.SymbolIDs {
			if !b.claimed[id] {
				b.claimed[id] = true
				b.declared[id] = true
				b.declaredSymbols++
			}
		}
	}
}

// claimRoutes turns route registrations into capabilities. Routes go first
// because an HTTP surface is the capability list a newcomer can check
// against a system's own clients.
func (b *builder) claimRoutes() {
	for _, r := range b.in.Routes {
		sym, ok := b.byID[r.HandlerSymbolID]
		if r.HandlerSymbolID == 0 || !ok || b.claimed[sym.ID] {
			// Either grunnr could not resolve what serves the endpoint, or
			// an annotation already speaks for the handler. Both are real,
			// neither is a proposal: an unresolved handler is reported in
			// the limits section instead of as a group with nothing in it.
			if r.HandlerSymbolID == 0 || !ok {
				b.unresolvedRoutes++
			}
			continue
		}
		id := capabilityIDFromRoute(r.Method, r.Path)
		if !validID(id) {
			b.unresolvedRoutes++
			continue
		}
		c := b.capability(id, SourceRoute)
		c.Method, c.Path = displayMethod(r.Method), r.Path
		c.Title = c.Method + " " + r.Path
		c.Dir = dirOf(sym.FilePath)
		b.attach(c, sym)
		c.Evidence = append(c.Evidence, Evidence{
			Kind:   SourceRoute,
			Detail: fmt.Sprintf("route %s registered here", c.Title),
			File:   r.FilePath, Line: r.Line, Symbol: r.HandlerName,
		})
	}
}

// claimTestClusters groups production code by the subject its tests agree
// on. Two tests are the threshold: one test leading with a word is a name,
// two are a convention, and proposing a capability from a single name
// produces one group per test.
func (b *builder) claimTestClusters() {
	type key struct{ dir, word string }
	clusters := map[key][]store.SymbolRow{}
	for _, t := range b.tests {
		word := testNameCluster(shortName(t.QualifiedName))
		if word == "" {
			continue
		}
		k := key{dirOf(t.FilePath), word}
		clusters[k] = append(clusters[k], t)
	}

	keys := make([]key, 0, len(clusters))
	for k, v := range clusters {
		if len(v) >= 2 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].dir != keys[j].dir {
			return keys[i].dir < keys[j].dir
		}
		return keys[i].word < keys[j].word
	})

	for _, k := range keys {
		_, dirName := capabilityIDFromDir(k.dir)
		id := dirName + "." + k.word
		if !validID(id) || b.capBy[id] != nil {
			continue
		}
		members := b.unclaimedMatching(k.dir, k.word)
		if len(members) == 0 {
			// The tests name something grunnr cannot point at. Showing the
			// group anyway would be a proposal with nothing behind it.
			continue
		}
		// The name has to be EARNED by the production code (#177): the word
		// must head at least two declarations in this directory. Without
		// this, cobra's TestNoFileCompletions and TestGenBashCompletionFile
		// produced provisional:root.no and provisional:root.bash. Measured on
		// cobra before and after: 19 of its 24 test-name proposals are
		// refused, and 5 survive -- flag, completion, command, help,
		// execute. When the word is not earned the CLUSTER DISSOLVES: no
		// capability, no claim, and the symbols fall through to the directory
		// stage untouched. The grouping was an artifact of the word, so the
		// word failing means it had no existence of its own.
		names := shortNames(members)
		if !nameEarned(names, k.word) {
			continue
		}
		witnesses := headWitnesses(names, k.word)
		c := b.capability(id, SourceTestName)
		c.Dir = k.dir
		c.Title = k.word + " in " + displayDir(k.dir)
		for _, m := range members {
			b.attach(c, m)
		}
		tests := clusters[k]
		c.Evidence = append(c.Evidence, Evidence{
			Kind: SourceTestName,
			// The corroboration is printed, not merely applied. A reader who
			// is told the name is the head of HasFlags, ParseFlags and
			// ResetFlags can check the claim in one grep; a reader told only
			// that eighteen tests lead with "Flag" is back to trusting a
			// prefix, which is what #177 was about.
			Detail: fmt.Sprintf("%d tests in %s lead with %q; the name is the head of %s",
				len(tests), displayDir(k.dir),
				strings.ToUpper(k.word[:1])+k.word[1:], witnessList(witnesses)),
			File: tests[0].FilePath, Line: tests[0].Line,
			Symbol: shortName(tests[0].QualifiedName),
		})
	}
}

// witnessList renders up to three head witnesses. Three is enough to show the
// convention; the rest are a count, because an evidence line nobody finishes
// reading cites nothing.
func witnessList(names []string) string {
	const maxShown = 3
	if len(names) <= maxShown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(names[:maxShown], ", "), len(names)-maxShown)
}

func shortNames(rows []store.SymbolRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, shortName(r.QualifiedName))
	}
	return out
}

// unclaimedMatching returns the unclaimed production symbols in dir whose
// name carries the cluster word -- the symbols the tests are named after.
//
// The match is on WORD boundaries, not substrings (#177). Substring matching
// is how the cluster word "no" swallowed repro.Normalize: "Normalize" contains
// the letters n-o, so a proposal called root.no claimed a function that
// normalises things.
func (b *builder) unclaimedMatching(dir, word string) []store.SymbolRow {
	var out []store.SymbolRow
	for _, s := range b.prod {
		if b.claimed[s.ID] || dirOf(s.FilePath) != dir {
			continue
		}
		if attests(shortName(s.QualifiedName), word) {
			out = append(out, s)
		}
	}
	return out
}

// claimDirectories is the fallback: everything no stronger signal claimed,
// grouped where it lives. It is the weakest proposal in the report, but it
// is never wrong the way a guess can be wrong -- it states a fact about the
// tree and leaves the reader to decide whether that fact is a capability.
func (b *builder) claimDirectories() {
	dirs := map[string][]store.SymbolRow{}
	for _, s := range b.prod {
		if b.claimed[s.ID] {
			continue
		}
		d := dirOf(s.FilePath)
		dirs[d] = append(dirs[d], s)
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)

	for _, d := range names {
		// A directory whose own last segment says nothing about the subject
		// -- the repository root, or a layout segment like src/ or app/ --
		// names nothing (#177). capabilityIDFromDir would hand back the
		// literal "root.root", which on cobra sat over 83 symbols; on grunnr
		// itself the same shape produced "internal.app" over 67. Those become
		// sized, file-broken-down groupings grunnr declines to name.
		if !namableDir(d) {
			b.unnamedGrouping(d, dirs[d])
			continue
		}
		id, _ := capabilityIDFromDir(d)
		if !validID(id) {
			continue
		}
		c := b.capBy[id]
		if c == nil {
			c = b.capability(id, SourceDirectory)
			c.Dir = d
			c.Title = "code under " + displayDir(d)
			c.Evidence = append(c.Evidence, Evidence{
				Kind:   SourceDirectory,
				Detail: fmt.Sprintf("%d undeclared symbols share the directory %s", len(dirs[d]), displayDir(d)),
				File:   dirs[d][0].FilePath, Line: dirs[d][0].Line,
			})
		} else {
			// The directory-derived id collides with a capability an
			// earlier, stronger signal already proposed. Merging is the
			// right call -- two proposals under one id would be two things
			// the user cannot tell apart -- but a merge that leaves no
			// trace means the reader sees route-derived or test-derived
			// evidence above a symbol list that a directory sweep filled
			// in, with nothing saying so. Say so.
			c.Evidence = append(c.Evidence, Evidence{
				Kind: SourceDirectory,
				Detail: fmt.Sprintf(
					"merged in: %d further undeclared symbols under %s share this capability's id, "+
						"which was proposed from %s evidence",
					len(dirs[d]), displayDir(d), c.Source),
				File: dirs[d][0].FilePath, Line: dirs[d][0].Line,
			})
		}
		for _, s := range dirs[d] {
			b.attach(c, s)
		}
	}
}

// unnamedGrouping records the leftovers of one directory grunnr will not name
// (#177).
//
// It is the honest answer to a grouping that is real and has no name the code
// supports: the symbols are counted, the files are broken down, and the label
// is withheld. It is deliberately NOT a merge target and never reachable
// through capBy -- an unnamed grouping is by definition the leftovers of
// exactly one directory, and merging two of them would invent the very
// relationship the refusal says grunnr cannot see.
func (b *builder) unnamedGrouping(dir string, rows []store.SymbolRow) {
	c := &Capability{
		Provisional: true, Source: SourceDirectory, Dir: dir,
		Title: "unnamed grouping in " + displayDir(dir),
	}
	b.caps = append(b.caps, c)
	c.Evidence = append(c.Evidence, Evidence{
		Kind: SourceDirectory,
		Detail: fmt.Sprintf(
			"%d undeclared symbols in %s that no route, no directory name and no "+
				"corroborated test name covers. Grunnr has no honest name for this one.",
			len(rows), displayDir(dir)),
		File: rows[0].FilePath, Line: rows[0].Line,
	})
	for _, s := range rows {
		b.attach(c, s)
	}
}

// capability returns (creating if needed) the proposal with this id.
func (b *builder) capability(id string, src Source) *Capability {
	if c := b.capBy[id]; c != nil {
		return c
	}
	domain := id
	if i := strings.Index(id, "."); i > 0 {
		domain = id[:i]
	}
	c := &Capability{ID: id, Domain: domain, Provisional: true, Named: true, Source: src}
	b.capBy[id] = c
	b.caps = append(b.caps, c)
	return c
}

// attach records one symbol as a member of a proposal and marks it claimed.
func (b *builder) attach(c *Capability, s store.SymbolRow) {
	b.claimed[s.ID] = true
	c.SymbolIDs = append(c.SymbolIDs, s.ID)
	c.SymbolNames = append(c.SymbolNames, string(s.QualifiedName))
	c.Symbols++
}

// enrich adds the facts a directory listing cannot show: the data
// footprint, the test evidence, the git history, and the anchor a promotion
// would write to.
func (b *builder) enrich() {
	attr := b.newAttributor()
	for _, c := range b.caps {
		c.Files = b.filesOf(c)
		if !c.Named {
			// Only the unnamed ones get the breakdown, and they get it
			// because it is all they have: "118 symbols somewhere in the
			// repository root" is not actionable, "61 of them in command.go"
			// is. Derived here rather than at creation so the counts are
			// against final membership and cannot drift from SymbolIDs.
			c.FileCounts = b.fileCountsOf(c)
		}
		b.attachSQL(c, attr)
		b.attachTestEvidence(c)
		b.attachChurn(c)
		b.attachAnchor(c)
	}
}

func (b *builder) filesOf(c *Capability) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range c.SymbolIDs {
		f := b.byID[id].FilePath
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// fileCountsOf breaks a grouping down by file, heaviest first.
func (b *builder) fileCountsOf(c *Capability) []FileCount {
	n := map[string]int{}
	for _, id := range c.SymbolIDs {
		if f := b.byID[id].FilePath; f != "" {
			n[f]++
		}
	}
	out := make([]FileCount, 0, len(n))
	for f, k := range n {
		out = append(out, FileCount{Path: f, Symbols: k})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbols != out[j].Symbols {
			return out[i].Symbols > out[j].Symbols
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// attributor answers "which capability issues this query?".
//
// Three locators in descending order of precision, because a query grunnr
// cannot place is a table set that silently disappears from the map, and the
// data footprint is the one column a directory listing could never give the
// reader:
//
//  1. the symbol the operation was linked to;
//  2. the file it was read from;
//  3. the nearest ancestor directory that some capability owns.
//
// Step 3 exists for sqlc: the merged inventory reports those queries at the
// .sql file that defines them, and a .sql file holds no indexed symbols, so
// steps 1 and 2 both come up empty for the whole data layer of any project
// that uses one.
type attributor struct {
	bySymbol map[int64]*Capability
	byFile   map[string]*Capability
	byDir    map[string]*Capability
}

func (b *builder) newAttributor() *attributor {
	a := &attributor{
		bySymbol: map[int64]*Capability{},
		byFile:   map[string]*Capability{},
		byDir:    map[string]*Capability{},
	}
	// "Most symbols in this file/directory wins" is the least arbitrary
	// tiebreak available when several capabilities share one; the id
	// comparison keeps the choice stable across runs.
	fileCounts := map[string]map[*Capability]int{}
	dirCounts := map[string]map[*Capability]int{}
	bump := func(m map[string]map[*Capability]int, key string, c *Capability) {
		if m[key] == nil {
			m[key] = map[*Capability]int{}
		}
		m[key][c]++
	}
	for _, c := range b.caps {
		for _, id := range c.SymbolIDs {
			a.bySymbol[id] = c
			f := b.byID[id].FilePath
			bump(fileCounts, f, c)
			bump(dirCounts, dirOf(f), c)
		}
	}
	for f, m := range fileCounts {
		a.byFile[f] = pickOwner(m)
	}
	for d, m := range dirCounts {
		a.byDir[d] = pickOwner(m)
	}
	return a
}

func pickOwner(m map[*Capability]int) *Capability {
	var best *Capability
	bestN := 0
	for c, n := range m {
		if n > bestN || (n == bestN && best != nil && c.ID < best.ID) {
			best, bestN = c, n
		}
	}
	return best
}

// owner resolves one operation to the capability that should carry it, or
// nil when nothing in the map is near enough to claim it.
func (a *attributor) owner(op store.SQLOperationRecord) *Capability {
	if op.SymbolID != nil {
		if c := a.bySymbol[*op.SymbolID]; c != nil {
			return c
		}
	}
	if c := a.byFile[op.FilePath]; c != nil {
		return c
	}
	for dir := dirOf(op.FilePath); ; dir = dirOf(dir) {
		if c := a.byDir[dir]; c != nil {
			return c
		}
		if dir == "" {
			return nil
		}
	}
}

func (b *builder) attachSQL(c *Capability, attr *attributor) {
	reads, writes := map[string]bool{}, map[string]bool{}
	for _, op := range b.in.SQLOps {
		if attr.owner(op) != c {
			continue
		}
		c.SQLOperations++
		if !op.Resolved {
			c.SQLUnresolved++
			continue
		}
		for _, t := range op.Tables {
			if strings.EqualFold(t.Access, "write") {
				writes[t.Table] = true
				continue
			}
			reads[t.Table] = true
		}
	}
	c.Reads, c.Writes = sortedKeys(reads), sortedKeys(writes)
}

// attachTestEvidence grades how grunnr knows this capability is exercised.
//
// A coverage run is consulted for BOTH answers it can give. Reading it only
// for a positive -- and falling through to colocation when it says nothing
// executed -- would replace a measurement with a weaker guess in exactly the
// case where the measurement exists: "an ingested run recorded none of these
// symbols executing" is the more useful finding than "a test file sits in
// this directory", and it is the one the reader can act on.
func (b *builder) attachTestEvidence(c *Capability) {
	if b.in.Coverage.Available {
		measured := false
		for _, id := range c.SymbolIDs {
			if b.in.Coverage.Executed[id] {
				c.TestEvidence = TestEvidenceExecution
				return
			}
			measured = measured || b.in.Coverage.measured(id)
		}
		if measured {
			c.TestEvidence = TestEvidenceNotExecuted
			return
		}
	}
	for _, f := range c.Files {
		if b.testDirs[dirOf(f)] {
			c.TestEvidence = TestEvidenceColocated
			return
		}
	}
	c.TestEvidence = TestEvidenceNone
}

func (b *builder) attachChurn(c *Capability) {
	if b.in.Churn == nil || len(c.Files) == 0 {
		return
	}
	fc := b.in.Churn.ForFiles(c.Files)
	facts := &ChurnFacts{
		Score: fc.Score, Status: fc.Status, HotFile: fc.HotFile,
		Commits: fc.Commits, Authors: fc.Authors,
	}
	if !fc.LastCommit.IsZero() {
		facts.LastCommit = fc.LastCommit.UTC().Format("2006-01-02")
	}
	c.Churn = facts
	if facts.Known() && facts.HotFile != "" {
		c.Evidence = append(c.Evidence, Evidence{
			Kind: SourceChurn,
			Detail: fmt.Sprintf("%d commits by %d author(s) in the churn window, hottest file",
				facts.Commits, facts.Authors),
			File: facts.HotFile,
		})
	}
}

// attachAnchor picks the declaration a promotion would annotate.
//
// The preference order is "the exported symbol whose name matches the
// capability", then "the first exported symbol", then "the first symbol at
// all". Each step is stated in Reasoning because the user is about to let
// grunnr edit their source, and an edit whose target they cannot predict is
// one they should refuse.
func (b *builder) attachAnchor(c *Capability) {
	if len(c.SymbolIDs) == 0 {
		return
	}
	rows := make([]store.SymbolRow, 0, len(c.SymbolIDs))
	for _, id := range c.SymbolIDs {
		rows = append(rows, b.byID[id])
	}
	sortSymbols(rows)

	// An unnamed grouping (#177) has no name half to prefer a match against,
	// so only the exported-first step applies -- and it carries no annotation
	// text, because there is no id to write. The anchor is still a fact about
	// location, and `promote --as` needs a target to write the user's own id
	// above.
	if !c.Named {
		pick, why := rows[0], "first declaration in the group (grunnr did not name this grouping)"
		if s, ok := firstMatch(rows, isExported); ok {
			pick, why = s, "first exported declaration in the group (grunnr did not name this grouping)"
		}
		c.Anchor = &Anchor{
			SymbolID: pick.ID, Qualified: string(pick.QualifiedName),
			FilePath: pick.FilePath, Line: pick.Line,
			Reasoning: why, Annotation: "",
		}
		return
	}

	name := c.ID
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	pick, why := rows[0], "first declaration in the group"
	if s, ok := firstMatch(rows, func(r store.SymbolRow) bool {
		return isExported(r) && strings.Contains(strings.ToLower(shortName(r.QualifiedName)), name)
	}); ok {
		pick, why = s, "exported declaration whose name matches the capability"
	} else if s, ok := firstMatch(rows, isExported); ok {
		pick, why = s, "first exported declaration in the group"
	}
	c.Anchor = &Anchor{
		SymbolID: pick.ID, Qualified: string(pick.QualifiedName),
		FilePath: pick.FilePath, Line: pick.Line,
		Reasoning: why, Annotation: "@grunnr:feature " + c.ID,
	}
}

// assemble drops the empty proposals, orders the rest, and totals the run.
func (b *builder) assemble() Result {
	res := Result{Root: b.in.Root, Provisional: true}
	domains := map[string]bool{}
	for _, c := range b.caps {
		if c.Symbols == 0 {
			continue
		}
		if c.Named {
			domains[c.Domain] = true
			res.Stats.NamedCapabilities++
		} else {
			res.Stats.UnnamedGroupings++
			res.Stats.UnnamedSymbols += c.Symbols
		}
		res.Stats.SymbolsProposed += c.Symbols
		res.Capabilities = append(res.Capabilities, *c)
	}
	sortCapabilities(res.Capabilities)
	// Numbered AFTER the sort, so "unnamed:1" means the same grouping on the
	// next run over an unchanged tree. A handle a user pastes into
	// `promote --as` that silently points somewhere else next run is worse
	// than no handle.
	n := 0
	for i := range res.Capabilities {
		if !res.Capabilities[i].Named {
			n++
			res.Capabilities[i].UnnamedIndex = n
		}
	}

	for _, s := range b.prod {
		if !b.declared[s.ID] {
			res.Stats.UndeclaredSymbols++
		}
	}
	res.Stats.ProductionSymbols = len(b.prod)
	res.Stats.TestSymbols = len(b.tests)
	res.Stats.DeclaredFeatures = len(b.in.Declared)
	res.Stats.DeclaredSymbols = b.declaredSymbols
	res.Stats.ProvisionalCapabilities = len(res.Capabilities)
	res.Stats.Domains = len(domains)
	res.Stats.Routes = len(b.in.Routes)
	res.Stats.SQLOperations = len(b.in.SQLOps)
	for _, op := range b.in.SQLOps {
		if !op.Resolved {
			res.Stats.SQLUnresolved++
		}
	}
	return res
}

// sourceRank orders proposals by how legible their evidence is to somebody
// who has never seen the codebase.
func sourceRank(s Source) int {
	switch s {
	case SourceRoute:
		return 0
	case SourceTestName:
		return 1
	default:
		return 2
	}
}

func sortCapabilities(caps []Capability) {
	sort.Slice(caps, func(i, j int) bool {
		a, z := caps[i], caps[j]
		// Every named proposal outranks every unnamed grouping (#177). The
		// map is read top-down and a reader who meets an unnamed grouping
		// first learns nothing they can act on; a reader who meets it last
		// has already seen what grunnr WAS willing to name and reads the
		// refusal as the boundary it is.
		if a.Named != z.Named {
			return a.Named
		}
		if !a.Named {
			if a.Symbols != z.Symbols {
				return a.Symbols > z.Symbols
			}
			return a.Dir < z.Dir
		}
		// Size before signal type, once both are named.
		//
		// Ranking by signal alone put `provisional:root.up` -- two symbols,
		// from a test cluster -- above `database.postgres` with sixteen, on
		// golang-migrate. A reader meets the map top-down, so the first row
		// is the tool's claim about what this repository is FOR; a two-symbol
		// verb there reads exactly like the `root.no` this issue removed,
		// only with a passport. Signal strength still breaks ties, because
		// between two proposals of equal size a route beats a directory.
		if a.Symbols != z.Symbols {
			return a.Symbols > z.Symbols
		}
		if ra, rz := sourceRank(a.Source), sourceRank(z.Source); ra != rz {
			return ra < rz
		}
		return a.ID < z.ID
	})
}

func sortSymbols(rows []store.SymbolRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].FilePath != rows[j].FilePath {
			return rows[i].FilePath < rows[j].FilePath
		}
		if rows[i].Line != rows[j].Line {
			return rows[i].Line < rows[j].Line
		}
		return rows[i].QualifiedName < rows[j].QualifiedName
	})
}

func firstMatch(rows []store.SymbolRow, pred func(store.SymbolRow) bool) (store.SymbolRow, bool) {
	for _, r := range rows {
		if pred(r) {
			return r, true
		}
	}
	return store.SymbolRow{}, false
}

// isExported approximates "part of this package's surface" across the
// languages grunnr indexes: Go exports by capitalisation, and TS/Python
// conventions put a leading underscore on the private ones.
func isExported(r store.SymbolRow) bool {
	n := shortName(r.QualifiedName)
	if n == "" || strings.HasPrefix(n, "_") {
		return false
	}
	c := n[0]
	return c >= 'A' && c <= 'Z'
}

func shortName[T ~string](qn T) string {
	s := string(qn)
	if i := strings.LastIndex(s, "."); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	if i := strings.LastIndex(s, "::"); i >= 0 && i+2 < len(s) {
		return s[i+2:]
	}
	return s
}

func dirOf(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// displayMethod renders a route's method. A registration with no method --
// the stdlib mux shape -- serves every one of them, and "ANY" says that
// where a blank column would read as a rendering bug.
func displayMethod(m string) string {
	if m = strings.ToUpper(strings.TrimSpace(m)); m == "" {
		return "ANY"
	}
	return m
}

// displayDir renders the repository root as something a reader recognises
// rather than as an empty string.
func displayDir(d string) string {
	if d == "" {
		return "the repository root"
	}
	return d + "/"
}

// excludedPaths are trees whose symbols are not this project's capabilities:
// fixtures exist to be scanned, dependencies belong to somebody else.
var excludedPaths = []string{"testdata/", "vendor/", "node_modules/", ".git/"}

func excludedPath(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range excludedPaths {
		if strings.HasPrefix(p, seg) || strings.Contains(p, "/"+seg) {
			return true
		}
	}
	return false
}

// isTestPath recognises the test-file conventions of the languages grunnr
// indexes. Getting this wrong in either direction is visible: a missed test
// file becomes a capability, and a production file mistaken for a test
// disappears from the map entirely.
func isTestPath(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_"),
		strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".spec.ts"), strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".spec.js"),
		base == "conftest.py":
		return true
	}
	return strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/") ||
		strings.HasPrefix(p, "e2e/") || strings.Contains(p, "/__tests__/")
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
