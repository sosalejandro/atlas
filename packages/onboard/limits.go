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
	out = append(out, Limit{
		Code: "inference-is-not-declaration",
		Detail: fmt.Sprintf("All %d capabilities above are inferred. Atlas did not write any of "+
			"them to the registry; %d declared features already in the registry were adopted as they are.",
			res.Stats.ProvisionalCapabilities, res.Stats.DeclaredFeatures),
		Fix: "atlas onboard promote --id <id> --apply",
	})
	return out
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
