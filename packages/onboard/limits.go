package onboard

import (
	"fmt"
	"strings"
)

// limits states what atlas could not see on this run.
//
// The section is not a disclaimer. A report that lists only its findings
// reads as complete, and this one is a lower bound in four separate
// directions: queries it could not parse, routers it does not recognise,
// execution it was never given, and history git would not hand over. A
// reader who acts on the findings without knowing which of those applied is
// acting on a picture that looks finished and is not.
//
// Every limit that CAN be removed carries the command that removes it, so
// the honest statement doubles as the next step.
func limits(in Input, res Result, unresolvedRoutes int) []Limit {
	var out []Limit
	out = appendLimit(out, coverageLimit(in))
	out = appendLimit(out, sqlLimit(in, res))
	out = appendLimit(out, routeLimit(in, unresolvedRoutes))
	out = appendLimit(out, churnLimit(in, res))
	out = appendLimit(out, scanLimit(in))
	out = appendLimit(out, couldNotNameLimit(res))
	out = append(out, Limit{
		Code: "inference-is-not-declaration",
		// "below", not "above": this section prints BEFORE the map
		// (internal/cli/onboard.go printOnboard), and it said "above" from
		// the day it was written.
		//
		// The two counts are separate, and calling only the first half
		// "capabilities" is the point. An earlier version summed them --
		// "All 107 capabilities below are inferred" -- one bullet after the
		// header had said "105 named proposals + 2 unnamed groupings". That
		// re-inflated the exact number #177 exists to bring down, and it
		// called a grouping atlas had just refused to name a capability, in
		// the same breath as refusing it. A tool that contradicts itself
		// inside twenty lines is not read carefully after that.
		Detail: fmt.Sprintf("All %d inferred proposals below are atlas's guesses, not declarations: "+
			"it wrote none of them to the registry, and %d declared features already there were "+
			"adopted as they are.%s",
			res.Stats.NamedCapabilities, res.Stats.DeclaredFeatures,
			unnamedSuffix(res.Stats.UnnamedGroupings)),
		Fix: "atlas onboard promote --id <id> --apply",
	})
	return out
}

// unnamedSuffix names the refused groupings separately from the proposals,
// because they are a different kind of thing and summing them was how the
// report ended up calling a refusal a capability.
func unnamedSuffix(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " One further grouping is listed that atlas would not name."
	default:
		return fmt.Sprintf(" A further %d groupings are listed that atlas would not name.", n)
	}
}

// couldNotNameLimit is the refusal, stated where the reader meets it BEFORE
// the map (#177).
//
// It belongs in this section and not in a footnote under the proposals
// because it is the same kind of statement as the rest of them: a bound on
// what this run knows. A tool that silently labels 44% of a repository with
// words scraped off test names looks more capable than one that says it could
// not name them -- right up to the moment somebody reads the names.
func couldNotNameLimit(res Result) *Limit {
	st := res.Stats
	if st.UnnamedGroupings == 0 {
		return nil
	}
	// Computed from this run's own counters. A percentage in a report that is
	// not measured on the run printing it is the house rule this package
	// exists to demonstrate.
	pct := 0.0
	if st.UndeclaredSymbols > 0 {
		pct = 100 * float64(st.UnnamedSymbols) / float64(st.UndeclaredSymbols)
	}
	return &Limit{
		Code: "could-not-name",
		Detail: fmt.Sprintf(
			"Atlas could not name %d of %d undeclared symbols (%.0f%%). They are in %s it "+
				"refused to name rather than label with a word scraped off a test name or a "+
				"directory that says nothing -- listed as \"groupings atlas would not name\" in "+
				"the map below, with their file breakdown. Naming one is the single judgement "+
				"this tool will not make for you.",
			st.UnnamedSymbols, st.UndeclaredSymbols, pct,
			plural(st.UnnamedGroupings, "grouping", "groupings")),
		Fix: "atlas onboard promote --id unnamed:1 --as <your.feature.id>",
	}
}

func appendLimit(out []Limit, l *Limit) []Limit {
	if l == nil {
		return out
	}
	return append(out, *l)
}

// coverageLimit is the most consequential one: without an ingested run,
// every "untested" claim in the report rests on file layout rather than on
// anything having executed.
func coverageLimit(in Input) *Limit {
	if in.Coverage.Available {
		return nil
	}
	return &Limit{
		Code: "no-execution-evidence",
		Detail: "No coverage run has been ingested, so atlas cannot say what your tests " +
			"actually execute. Test evidence in this report means \"a test file sits in " +
			"the same directory\", which is a weaker claim than it looks.",
		Fix: coverageCommand(in),
	}
}

func sqlLimit(in Input, res Result) *Limit {
	if !in.SQLScanned {
		return &Limit{
			Code:   "sql-not-scanned",
			Detail: "The SQL inventory was not built, so no capability in this report has a data footprint.",
			Fix:    "atlas sql scan",
		}
	}
	if res.Stats.SQLUnresolved == 0 {
		return nil
	}
	pct := 100 * float64(res.Stats.SQLUnresolved) / float64(res.Stats.SQLOperations)
	return &Limit{
		Code: "sql-unresolved",
		Detail: fmt.Sprintf("%d of %d queries (%.0f%%) were assembled where atlas could not read them. "+
			"Every table set above is a lower bound: a table only those queries touch is missing from it.",
			res.Stats.SQLUnresolved, res.Stats.SQLOperations, pct),
		Fix: "atlas sql list --unresolved",
	}
}

// routeLimit covers the two ways the HTTP surface can be incomplete: a
// router atlas does not parse, and a registration whose handler it could not
// resolve. Both produce the same silence, and silence about an HTTP surface
// reads as "there isn't one".
func routeLimit(in Input, unresolved int) *Limit {
	if len(in.Routes) == 0 {
		return &Limit{
			Code: "no-routes-found",
			Detail: "Atlas found no HTTP route registrations. It reads chi, echo, gin, huma and " +
				"net/http registrations written as literal calls; a router configured from a " +
				"table, a code generator or another framework is invisible to it, so this may " +
				"mean \"no routes\" or may mean \"a router atlas does not read\".",
		}
	}
	if unresolved == 0 {
		return nil
	}
	return &Limit{
		Code: "unresolved-route-handlers",
		Detail: fmt.Sprintf("%d of %d route registrations point at a handler atlas could not resolve "+
			"to an indexed symbol, so no capability was proposed for them.",
			unresolved, len(in.Routes)),
		Fix: "atlas contract list",
	}
}

func churnLimit(in Input, res Result) *Limit {
	if in.Churn == nil {
		return &Limit{
			Code: "no-history",
			Detail: "Git history was not mined, so no capability in this report is ranked by how " +
				"much it is changing.",
			Fix: "atlas hotspots",
		}
	}
	// A shallow clone -- the normal CI checkout -- makes every score a lower
	// bound, and churn signals that by returning StatusUnknown for every
	// file. Read the answer off the capabilities that were actually scored:
	// probing with a synthetic path would report unknown for a healthy
	// repository too, because no synthetic path is a tracked file.
	for _, c := range res.Capabilities {
		if c.Churn.Known() {
			return nil
		}
	}
	return &Limit{
		Code: "shallow-history",
		Detail: "Git could not speak for the files in this repository (a shallow clone, or a " +
			"tree that is not a git checkout), so churn scores are neutral placeholders " +
			"rather than measurements.",
		Fix: "git fetch --unshallow",
	}
}

// scanLimit reports the two things the scan can tell you about its own
// completeness, and is careful not to conflate them.
//
// The exclusion ledger is a count of files that were deliberately not
// indexed, so their symbols really are absent from every capability above.
// A scanner warning is a different kind of thing: a name collision resolved
// by qualifying the id, a router shape the TS scanner did not recognise, a
// file that would not parse. Some of those cost atlas a symbol and some do
// not, and nothing here classifies them -- so the warning count is reported
// as a warning count. Presenting it as "files atlas could not read" would be
// a number this run does not have.
func scanLimit(in Input) *Limit {
	if in.FilesExcluded == 0 && len(in.ScannerWarnings) == 0 {
		return nil
	}
	var parts []string
	if in.FilesExcluded > 0 {
		parts = append(parts, fmt.Sprintf(
			"The scan deliberately excluded %d files (generated code and ignored packages); "+
				"their symbols are absent from every capability above.", in.FilesExcluded))
	}
	if n := len(in.ScannerWarnings); n > 0 {
		parts = append(parts, fmt.Sprintf(
			"The scan raised %s. A warning is a diagnostic, not a count of files atlas "+
				"could not read: some cost it a symbol and some do not, and it does not "+
				"tell them apart -- so read them rather than the number.",
			plural(n, "scanner warning", "scanner warnings")))
	}
	return &Limit{
		Code:   "scan-incomplete",
		Detail: strings.Join(parts, " "),
		Fix:    "atlas doctor",
	}
}
