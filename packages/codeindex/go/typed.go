package goscan

import (
	"context"
	"go/ast"
	"go/types"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/resolver"
	"github.com/sosalejandro/atlas/packages/shared"
)

// ResolutionReport is what the type-checked resolver managed to do on one
// scan (issue #87).
//
// It is on Result rather than in a log line because "which edges are
// typed" is not a diagnostic, it is part of the answer. A call graph over
// a repo where three packages failed to compile is a different artefact
// from one where none did, and a consumer that cannot tell them apart
// will report the same confidence for both.
type ResolutionReport struct {
	// Packages / TypeChecked / Degraded partition every package the load
	// returned. They are counts of PACKAGES, not of files or edges.
	Packages    int `json:"packages"`
	TypeChecked int `json:"type_checked"`
	Degraded    int `json:"degraded"`

	// DegradedPackages names the failures, capped, each with the first
	// type error reported for it. Paths in the message are made
	// repo-relative: an absolute path here would make two scans of the
	// same tree from different checkouts disagree.
	DegradedPackages []DegradedPackage `json:"degraded_packages,omitempty"`

	// IndexedFiles is how many of the files this scan INDEXED were
	// resolved with types. It is the number that matters to a reader:
	// Files on the load side counts everything the type checker saw,
	// including generated code the scanner deliberately skips.
	IndexedFiles      int `json:"indexed_files"`
	TypedIndexedFiles int `json:"typed_indexed_files"`

	// CallGraph reports whether class-hierarchy analysis ran. Without it
	// static calls still resolve exactly and interface dispatch resolves
	// to nothing.
	CallGraph   bool `json:"call_graph"`
	InvokeSites int  `json:"invoke_sites"`

	LoadDuration      time.Duration `json:"load_duration"`
	CallGraphDuration time.Duration `json:"call_graph_duration"`

	// Unavailable explains why NOTHING was type-checked, when that is
	// what happened: no go.mod, no toolchain, an unreadable tree. It is
	// empty on a successful load even when every package degraded, so a
	// reader can tell "atlas could not look" from "atlas looked and the
	// repo does not compile".
	Unavailable string `json:"unavailable,omitempty"`
}

// DegradedPackage is one package that could not be type-checked.
type DegradedPackage struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// initTypedResolution loads the tree and records the outcome.
//
// It never returns an error. Every failure mode here -- no toolchain, no
// module, a tree that does not compile -- degrades to the AST path and is
// reported on Result.Resolution instead, because a scan that refuses to
// run on a repo mid-refactor is a scan nobody can use at the moment they
// most need it.
func (c *scanContext) initTypedResolution(ctx context.Context) {
	if c.opts.SkipTypedResolution {
		return
	}
	prog, err := resolver.Load(ctx, c.backendAbs, resolver.Options{
		IncludeTests: !c.opts.SkipTests,
	})
	if err != nil {
		c.resolution = &ResolutionReport{Unavailable: err.Error()}
		c.deferWarning("typed resolution unavailable, falling back to name resolution: " + err.Error())
		return
	}
	c.typed = prog
	c.typedIDs = make(map[string]shared.SymbolID)

	st := prog.Status()
	report := &ResolutionReport{
		Packages:          st.Packages,
		TypeChecked:       st.TypeChecked,
		Degraded:          st.Degraded,
		CallGraph:         st.CallGraph,
		InvokeSites:       st.InvokeSites,
		LoadDuration:      st.LoadDuration,
		CallGraphDuration: st.CallGraphDuration,
	}
	for _, d := range st.DegradedList {
		report.DegradedPackages = append(report.DegradedPackages,
			DegradedPackage{Path: d.Path, Error: d.Error})
	}
	// A pattern that resolved to nothing is not a broken package, it is
	// a tree the go tool could not enumerate -- most often a directory
	// outside any module. When NOTHING type-checked, that is the whole
	// story and belongs in Unavailable; when some of the tree did load,
	// it is one workspace member missing and belongs in the warnings
	// beside every other partial-scan report.
	if len(st.LoadErrors) > 0 {
		if st.TypeChecked == 0 {
			report.Unavailable = st.LoadErrors[0]
		}
		for _, e := range st.LoadErrors {
			c.deferWarning("typed resolution: " + e)
		}
	}
	c.resolution = report
}

// deferWarning holds a typed-resolution complaint until the walk has
// decided whether this scan contains any Go at all.
//
// The load runs before the walk, so it fires on a TypeScript-only or
// Python-only repository too, where it fails with "no module here" --
// correctly, and irrelevantly. Warning about it there would put a Go
// diagnostic on every scan of a repo with no Go in it, which is how a
// warnings list stops being read.
func (c *scanContext) deferWarning(msg string) {
	c.pendingTypedWarnings = append(c.pendingTypedWarnings, msg)
}

// typedSyntax returns the type-checked syntax tree for a file, if this
// scan has one.
//
// The scanner adopts the resolver's tree rather than parsing the file a
// second time. That is not an optimisation: types.Info is keyed by
// ast.Node pointers, so a freshly parsed tree — structurally identical,
// different pointers — resolves nothing at all.
func (c *scanContext) typedSyntax(absPath string) (*ast.File, bool) {
	if c.typed == nil {
		return nil, false
	}
	return c.typed.Syntax(absPath)
}

// recordTypedID remembers which SymbolID a type-checked declaration was
// registered under, so call resolution can go object → id without ever
// rendering or matching a name.
func (c *scanContext) recordTypedID(file *ast.File, fn *ast.FuncDecl, id shared.SymbolID) {
	if c.typed == nil || file == nil {
		return
	}
	obj := c.typed.Declared(file, fn)
	if obj == nil {
		return
	}
	c.typedIDs[resolver.ObjectKey(obj)] = id
}

// resolveCallTyped answers one call site using types, and only types.
//
// It returns (nil, true) — resolved, nothing to emit — for a call the
// type checker understands but atlas has no symbol for: a conversion, a
// builtin, a call into a dependency, a call into a generated file the
// exclusion ledger dropped. Falling back to the name heuristic there
// would be the worst of both worlds: the type checker has already said
// there is no indexed callee, and a substring match would invent one.
//
// The second return reports whether the typed path handled this site at
// all. False means the file's package did not type-check, and the caller
// runs the AST ladder.
func (c *scanContext) resolveCallTyped(caller *funcInfo, call *ast.CallExpr) ([]callResolution, bool) {
	if !caller.typed || c.typed == nil {
		return nil, false
	}
	r := c.typed.ResolveCall(caller.file, call)
	if !r.Resolved {
		return nil, true
	}

	ids := c.indexedTargets(r.Targets)
	if len(ids) == 0 {
		return nil, true
	}
	// Ambiguity is a property of the CALL SITE, not of each edge: CHA
	// found more than one implementation and cannot say which runs, so
	// every edge out of the site inherits the doubt. A single-candidate
	// interface call is not ambiguous — there was nothing to choose
	// between — which is why this is len(ids) > 1 and not r.ViaInterface.
	ambiguous := len(ids) > 1
	out := make([]callResolution, 0, len(ids))
	for _, id := range ids {
		out = append(out, callResolution{ID: id, Ambiguous: ambiguous, Tier: graph.TierTyped})
	}
	return out, true
}

// indexedTargets maps resolved callee objects onto the ids this scan
// registered, dropping everything it did not index, and returns them in
// lexical order so a multi-target call site emits its edges the same way
// on every run.
func (c *scanContext) indexedTargets(targets []*types.Func) []shared.SymbolID {
	seen := make(map[shared.SymbolID]bool, len(targets))
	ids := make([]shared.SymbolID, 0, len(targets))
	for _, t := range targets {
		id, ok := c.typedIDs[resolver.ObjectKey(t)]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// finishResolutionReport fills in the per-file counts, which are only
// known once the walk has decided which files it indexes.
func (c *scanContext) finishResolutionReport() {
	if c.resolution == nil {
		return
	}
	if c.indexedFiles == 0 {
		// No Go was indexed, so there was nothing for the type checker
		// to resolve and nothing worth reporting about it. Dropping the
		// report keeps a TypeScript-only scan from carrying a Go
		// diagnostic it cannot act on.
		c.resolution = nil
		c.pendingTypedWarnings = nil
		return
	}
	c.resolution.IndexedFiles = c.indexedFiles
	c.resolution.TypedIndexedFiles = c.typedIndexedFiles
	if c.typed != nil && c.typedIndexedFiles == 0 {
		c.deferWarning("typed resolution loaded no package covering the scanned files; " +
			"every call edge is name-resolved or syntactic")
	}
	c.warnings = append(c.warnings, c.pendingTypedWarnings...)
	c.pendingTypedWarnings = nil
}
