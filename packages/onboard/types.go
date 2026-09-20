package onboard

import (
	"fmt"
	"strings"

	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/sqlops"
	"github.com/sosalejandro/atlas/packages/store"
)

// ProvisionalPrefix namespaces every inferred capability.
//
// It is not decoration. A feature id may not contain a colon
// (shared.ValidFeatureIDRe), so a provisional reference can never be
// mistaken for a declared one by any consumer, and can never be written into
// an annotation by accident: the prefix has to be deliberately stripped,
// which is what promotion does and nothing else does.
const ProvisionalPrefix = "provisional:"

// Source names where a proposal came from. It travels with the proposal
// because a grouping the reader cannot trace back to evidence is a grouping
// they can only take on faith, and this whole package is asking them not to.
type Source string

const (
	// SourceRoute is an HTTP route registration read out of the source.
	SourceRoute Source = "route"
	// SourceTestName is a cluster of test names that agree on a subject.
	SourceTestName Source = "test-name"
	// SourceDirectory is the fallback: code grouped where it lives.
	SourceDirectory Source = "directory"
	// SourceSQL is data-footprint evidence attached to a proposal that some
	// other source produced.
	SourceSQL Source = "sql"
	// SourceChurn is git-history evidence attached to a proposal.
	SourceChurn Source = "churn"
)

// TestEvidence grades how atlas knows a capability is exercised. The four
// values are not a scale of confidence in the code -- they are a scale of
// confidence in the CLAIM, and they are printed rather than collapsed
// because "a test file sits in this directory" and "a test executed this
// function" are different facts and only one of them is coverage.
type TestEvidence string

const (
	// TestEvidenceExecution means an ingested coverage run recorded one of
	// the capability's symbols executing. It and TestEvidenceNotExecuted are
	// the two values that are measurements; the other two are inferences
	// from file layout.
	TestEvidenceExecution TestEvidence = "execution"
	// TestEvidenceNotExecuted means an ingested coverage run MEASURED the
	// capability's symbols and recorded none of them executing. Like
	// execution, this is a measurement -- the negative one -- and it
	// outranks colocation: "the run reached this code and nothing ran it"
	// is a stronger and more useful statement than "a test file sits
	// nearby", and collapsing it into the weaker one turns a measured
	// negative into a vague positive.
	TestEvidenceNotExecuted TestEvidence = "measured-not-executed"
	// TestEvidenceColocated means test files sit alongside the capability's
	// code. It says a test exists near it, not that a test reaches it.
	TestEvidenceColocated TestEvidence = "colocated-tests"
	// TestEvidenceNone means neither -- no test file, no execution record.
	TestEvidenceNone TestEvidence = "none"
)

// Untested reports whether the evidence says nothing executes this
// capability. Both an absence of any signal and a measured non-execution
// qualify; the second is the stronger claim, and a finding that filtered on
// TestEvidenceNone alone would silently drop exactly the capabilities it has
// a measurement for.
func (t TestEvidence) Untested() bool {
	return t == TestEvidenceNone || t == TestEvidenceNotExecuted
}

// Route is one HTTP route registration atlas read statically. The CLI layer
// fills these from packages/contract; the type is restated here so the
// inference stays a pure function over plain values.
type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// HandlerSymbolID is the indexed symbol the registration points at, or
	// 0 when the handler could not be resolved. Such a route proposes no
	// capability -- there is nothing to group -- and is counted in the
	// limits section instead, because silence about an endpoint atlas could
	// not follow reads as "there is no such endpoint".
	HandlerSymbolID int64  `json:"handler_symbol_id,omitempty"`
	HandlerName     string `json:"handler,omitempty"`
	// FilePath and Line locate the registration, not the handler.
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
}

// DeclaredFeature is one feature a human actually declared, as it already
// exists in the store. Infer reads these only to stay out of their way.
type DeclaredFeature struct {
	ID        shared.FeatureID `json:"id"`
	Title     string           `json:"title"`
	SymbolIDs []int64          `json:"symbol_ids,omitempty"`
}

// ChurnLookup is the slice of churn.Report the inference needs. *churn.Report
// satisfies it; a test supplies a fake so history mining is not a
// prerequisite for testing the roll-up.
type ChurnLookup interface {
	ForFiles(paths []string) churn.FeatureChurn
}

// CoverageEvidence is what an ingested coverage run says about which symbols
// tests actually executed.
//
// Available is separate from a non-empty set on purpose: "no coverage has
// ever been ingested" and "coverage was ingested and reached nothing" are
// opposite findings, and a caller that cannot tell them apart will report
// the second when it means the first.
type CoverageEvidence struct {
	Available bool
	Executed  map[int64]bool

	// Measured is every symbol the run reported on, executed or not. It is
	// what separates "the run reached this code and nothing ran it" from
	// "the run never looked at this code" -- a Go coverprofile ingested into
	// a Go+TS repository measures half the tree, and reporting the other
	// half as not executed would be an assertion about something nothing
	// measured.
	//
	// A nil Measured therefore means "scope unknown": no symbol is treated
	// as measured, and the negative is never claimed. Executed always
	// implies measured, so a caller that fills only Executed still gets the
	// positive.
	Measured map[int64]bool
}

// measured reports whether the coverage run said anything at all about this
// symbol.
func (c CoverageEvidence) measured(id int64) bool {
	return c.Executed[id] || c.Measured[id]
}

// Input is everything the inference reads. Every field is optional except
// Symbols: each missing signal degrades one part of the report into a
// stated limit rather than into silence.
type Input struct {
	Root       string
	Symbols    []store.SymbolRow
	Routes     []Route
	Declared   []DeclaredFeature
	SQLOps     []store.SQLOperationRecord
	Advisories []sqlops.Advisory
	Dead       []store.DeadCodeCandidate
	Coverage   CoverageEvidence
	Churn      ChurnLookup

	// ScannerWarnings and FilesExcluded are carried through into the limits
	// section, and they are two different facts.
	//
	// ScannerWarnings is a diagnostic list, NOT a count of files atlas could
	// not read: most of its entries on a real repository are notices about
	// symbols that were indexed anyway (a name collision resolved by
	// qualifying the id, a router shape a sub-scanner did not recognise).
	// Nothing here classifies them, so the limits section reports them as
	// warnings and sends the reader to `atlas doctor` rather than turning
	// the count into a number about unread files.
	//
	// FilesExcluded is the size of the scanner's EXCLUSION LEDGER --
	// generated files and ignored packages it declined to index. It is not
	// the incremental scan's skip count, which counts unchanged files that
	// are fully indexed already; reporting that as "atlas could not see
	// these" turns a warm cache into a scary number.
	ScannerWarnings []string
	FilesExcluded   int

	// SQLScanned records whether the SQL inventory pass ran at all, so an
	// empty SQLOps can be reported as "not scanned" rather than as "no
	// queries", which are opposite statements about a codebase.
	SQLScanned bool

	// CoverageCommand is what the report tells the reader to run to give
	// atlas execution evidence. It is supplied by the caller because the
	// answer is project-shaped -- the Go path and a JS path are different
	// commands -- and a report that prints a command the reader's project
	// cannot run has spent the one instruction it had.
	//
	// Empty falls back to a placeholder that names the verb without
	// pretending to know the test command.
	CoverageCommand string
}

// Evidence is one citation behind a proposal or a finding.
type Evidence struct {
	Kind   Source `json:"kind"`
	Detail string `json:"detail"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Symbol string `json:"symbol,omitempty"`
}

// Anchor is the symbol a promotion would annotate: the declaration whose doc
// comment gets the @atlas:feature line. Promotion seeds membership at one
// symbol rather than spraying the whole group, because an annotation is a
// claim a human is making and a hundred of them written at once is not.
type Anchor struct {
	SymbolID   int64  `json:"symbol_id"`
	Qualified  string `json:"qualified_name"`
	FilePath   string `json:"file_path"`
	Line       int    `json:"line"`
	Reasoning  string `json:"reasoning"`
	Annotation string `json:"annotation"`
}

// ChurnFacts is the git-history read-out for one capability, decomposed so
// the number can be argued with rather than merely believed.
type ChurnFacts struct {
	Score      float64 `json:"score"`
	Status     string  `json:"status"`
	HotFile    string  `json:"hot_file,omitempty"`
	Commits    int     `json:"commits"`
	Authors    int     `json:"authors"`
	LastCommit string  `json:"last_commit,omitempty"`
}

// Known reports whether the churn score is a measurement rather than the
// neutral value churn returns when git cannot speak for the files.
func (c *ChurnFacts) Known() bool { return c != nil && c.Status == churn.StatusKnown }

// Capability is one PROVISIONAL grouping. Nothing here is a declaration; see
// the package doc for why that distinction is load-bearing.
type Capability struct {
	// ID is the id this capability WOULD take if promoted. It is a valid
	// feature id whenever Named is true and empty otherwise, and it is never
	// used to address the capability -- use Ref for that.
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Title  string `json:"title"`
	// Named is false when atlas REFUSED to name this grouping (#177). A
	// false here is a finding, not a failure: the symbols are real, their
	// count is real, their file breakdown is real, and the one thing atlas
	// will not do is label them with a word it cannot point at in the code.
	// Naming one is the single judgement this tool leaves to the reader.
	Named bool `json:"named"`
	// UnnamedIndex is the 1-based handle an unnamed grouping is addressed
	// by ("unnamed:1"). It is assigned after the display sort, so it is
	// stable across runs on an unchanged tree.
	UnnamedIndex int `json:"unnamed_index,omitempty"`
	// FileCounts is the per-file breakdown, set ONLY on unnamed groupings.
	// A named proposal has a name to stand on; an unnamed one has nothing
	// but its size, and a size with no shape is a number the reader cannot
	// act on. Sorted symbols descending, then path ascending.
	FileCounts []FileCount `json:"file_counts,omitempty"`
	// Provisional is always true. It is serialised rather than implied so
	// that no consumer of the JSON can lose the distinction by reading a
	// field it does not know about.
	Provisional bool   `json:"provisional"`
	Source      Source `json:"source"`

	Dir     string   `json:"dir,omitempty"`
	Files   []string `json:"files,omitempty"`
	Symbols int      `json:"symbols"`
	// SymbolIDs are the store ids behind the proposal -- the citation that
	// makes it checkable.
	SymbolIDs   []int64  `json:"symbol_ids,omitempty"`
	SymbolNames []string `json:"symbol_names,omitempty"`

	// Route fields, set only when Source is SourceRoute.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`

	Evidence []Evidence `json:"evidence,omitempty"`
	Anchor   *Anchor    `json:"anchor,omitempty"`

	// Reads and Writes are the tables the capability's queries touch. They
	// are a LOWER BOUND whenever SQLUnresolved is non-zero: some of its
	// queries were assembled where atlas could not read them, and a table
	// only those touch is missing here.
	Reads         []string `json:"reads,omitempty"`
	Writes        []string `json:"writes,omitempty"`
	SQLOperations int      `json:"sql_operations,omitempty"`
	SQLUnresolved int      `json:"sql_unresolved,omitempty"`

	TestEvidence TestEvidence `json:"test_evidence"`
	Churn        *ChurnFacts  `json:"churn,omitempty"`
}

// FileCount is one line of an unnamed grouping's file breakdown.
type FileCount struct {
	Path    string `json:"path"`
	Symbols int    `json:"symbols"`
}

// Ref is how a provisional capability is addressed anywhere a user or
// another tool might see it. The namespace is the guarantee: a colon cannot
// appear in a declared feature id, so a Ref can never be confused for one.
//
// An unnamed grouping (#177) is addressed as "provisional:unnamed:1". That
// form contains a colon too, and shared.ValidFeatureIDRe
// (packages/shared/types.go:22) rejects a colon -- so an unnamed grouping is
// unpromotable BY GRAMMAR rather than by a check somebody can forget to
// write, which is the same guarantee ProvisionalPrefix already leans on.
func (c Capability) Ref() string {
	if c.Named {
		return ProvisionalPrefix + c.ID
	}
	return fmt.Sprintf("%sunnamed:%d", ProvisionalPrefix, c.UnnamedIndex)
}

// Rename is the whole `promote --as` mechanism: it turns an unnamed grouping
// into a named capability carrying the id the USER chose.
//
// It is a method on the value rather than an edit in the promote path
// because promotion's insertion code should keep knowing exactly one thing --
// write this capability's annotation above its anchor -- and a rename that
// reached into it would be a second way to decide what gets written.
func (c Capability) Rename(id string) (Capability, error) {
	if !shared.IsValidFeatureID(shared.FeatureID(id)) {
		return Capability{}, fmt.Errorf(
			"onboard: %q is not a valid feature id (want two or more dot-separated "+
				"lowercase segments, e.g. billing.checkout)", id)
	}
	out := c
	out.Named = true
	out.ID = id
	out.Domain = id
	if i := strings.Index(id, "."); i > 0 {
		out.Domain = id[:i]
	}
	if c.Anchor != nil {
		anchor := *c.Anchor
		anchor.Annotation = "@atlas:feature " + id
		out.Anchor = &anchor
	}
	return out, nil
}

// Severity orders findings for display. There are three levels because the
// only decision the reader makes from this list is what to look at first.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityInfo   Severity = "info"
)

// Finding is one thing worth reading -- the part of the first run that
// justifies the next command.
type Finding struct {
	Code     string     `json:"code"`
	Severity Severity   `json:"severity"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail"`
	Count    int        `json:"count,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	// Provisional marks findings computed over inferred groupings rather
	// than declared ones. A finding about provisional.store is a finding
	// about a grouping atlas guessed, and the reader is owed that.
	Provisional bool `json:"provisional"`
	// Next is the command that acts on the finding, or "" when the finding
	// is something to know rather than something to run.
	Next string `json:"next,omitempty"`
}

// Limit is one thing atlas cannot see. The section exists because a report
// that lists only what it found reads as complete, and this one is not.
type Limit struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	// Fix is the command or change that removes the limit, when there is
	// one. Limits with no fix are properties of static analysis.
	Fix string `json:"fix,omitempty"`
}

// Stats are the counters the report leads with.
type Stats struct {
	ProductionSymbols int `json:"production_symbols"`
	TestSymbols       int `json:"test_symbols"`
	DeclaredFeatures  int `json:"declared_features"`
	DeclaredSymbols   int `json:"declared_symbols"`
	// ProvisionalCapabilities is every entry in the map -- named proposals
	// PLUS the groupings atlas refused to name (#177). It keeps its name and
	// its meaning of "things in the map", so a consumer that was reading it
	// as the length of the capabilities array stays right; the split is in
	// NamedCapabilities and UnnamedGroupings below.
	ProvisionalCapabilities int `json:"provisional_capabilities"`
	// NamedCapabilities is the entries atlas was willing to name.
	NamedCapabilities int `json:"named_capabilities"`
	// UnnamedGroupings is the entries it refused to name, and
	// UnnamedSymbols is how many symbols are inside them. The second number
	// is the one that matters: a refusal without a size is a shrug, and the
	// whole point of #177 is that atlas reports the size of what it cannot
	// name rather than inventing a label for it.
	UnnamedGroupings int `json:"unnamed_groupings"`
	UnnamedSymbols   int `json:"unnamed_symbols"`
	// Domains counts NAMED entries only: an unnamed grouping has no domain
	// to count, and folding it into a "root" domain would re-introduce the
	// name the refusal exists to withhold.
	Domains       int `json:"domains"`
	Routes        int `json:"routes"`
	SQLOperations int `json:"sql_operations"`
	SQLUnresolved int `json:"sql_unresolved"`
	// UndeclaredSymbols is how many production symbols no annotation speaks
	// for -- the denominator the map's coverage fraction is against.
	//
	// It is NOT ProductionSymbols minus DeclaredSymbols: a declared feature
	// can link test symbols and symbols under excluded trees, neither of
	// which is in ProductionSymbols, and subtracting them produces a
	// denominator smaller than the numerator.
	UndeclaredSymbols int `json:"undeclared_symbols"`
	// SymbolsProposed is how many of those ended up inside some proposal.
	SymbolsProposed int `json:"symbols_proposed"`
}

// Result is the whole first-run read-out.
type Result struct {
	Root string `json:"root"`
	// Provisional marks the entire document. Redundant with the per-
	// capability flag, and deliberately so: a consumer that renders only
	// the header still cannot present this as declared state.
	Provisional  bool         `json:"provisional"`
	Stats        Stats        `json:"stats"`
	Capabilities []Capability `json:"provisional_capabilities"`
	Findings     []Finding    `json:"findings"`
	Limits       []Limit      `json:"limits"`
}
