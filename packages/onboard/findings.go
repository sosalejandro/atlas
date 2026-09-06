package onboard

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/sqlops"
)

// hotChurnScore is the churn score above which a capability counts as under
// active change. churn's own curve puts 50 at its half-saturation constant
// -- five decay-weighted commits inside the window -- which is a module
// somebody is working on rather than one that merely exists. Below it the
// "changing weekly with nothing testing it" claim stops being true, and a
// finding that is not true is worse than no finding.
const hotChurnScore = 50.0

// maxFindingExamples caps how many citations one finding prints. The point
// of the first run is to hand the reader something they can check in a
// minute; twenty examples of the same shape is a list they will skim.
const maxFindingExamples = 5

// findings turns the inferred map into the part of the first run worth
// reading: things a person who has not read the whole codebase could not
// have known, ordered by how unlikely they are to already be known.
// defaultCoverageCommand is the fallback when the caller does not name the
// project's test command. It names the verb and marks the hole rather than
// guessing a toolchain.
const defaultCoverageCommand = "atlas cov sync --framework <framework> --input <report>"

// coverageCommand is what the report tells the reader to run to give atlas
// execution evidence.
func coverageCommand(in Input) string {
	if in.CoverageCommand == "" {
		return defaultCoverageCommand
	}
	return in.CoverageCommand
}

func findings(in Input, res Result) []Finding {
	var out []Finding
	out = appendIf(out, untestedRoutes(in, res))
	out = appendIf(out, sharedTableWrites(res))
	out = appendIf(out, soleTableOwners(res))
	out = appendIf(out, hotAndUntested(res))
	out = appendIf(out, sqlAdvisories(in))
	out = appendIf(out, deadCode(in))
	return out
}

func appendIf(out []Finding, f *Finding) []Finding {
	if f == nil {
		return out
	}
	return append(out, *f)
}

// untestedRoutes is first because it is the finding a newcomer is least
// able to reach on their own and most able to act on: the HTTP surface is
// public, and an endpoint nothing exercises is a promise nobody checks.
func untestedRoutes(in Input, res Result) *Finding {
	var ev []Evidence
	n := 0
	for _, c := range res.Capabilities {
		if c.Source != SourceRoute || c.TestEvidence != TestEvidenceNone {
			continue
		}
		n++
		if len(ev) < maxFindingExamples {
			ev = append(ev, Evidence{
				Kind: SourceRoute, Detail: c.Ref(),
				File: firstOr(c.Files, ""), Symbol: firstOr(c.SymbolNames, ""),
			})
		}
	}
	if n == 0 {
		return nil
	}
	return &Finding{
		Code: "untested-routes", Severity: SeverityHigh, Provisional: true,
		Title: fmt.Sprintf("%s no test reaching the handler",
			plural(n, "HTTP endpoint has", "HTTP endpoints have")),
		Detail: "Nothing atlas can see -- no ingested coverage run, no test file " +
			"beside the handler -- exercises these endpoints. They are the part of " +
			"the system other teams call directly.",
		Count: n, Evidence: ev,
		Next: coverageCommand(in),
	}
}

// sharedTableWrites is the coupling nobody wrote down. Two capabilities
// writing one table means a change to either can break the other, and
// nothing in the directory tree or the type system says so.
func sharedTableWrites(res Result) *Finding {
	writers := map[string][]string{}
	for _, c := range res.Capabilities {
		for _, t := range c.Writes {
			writers[t] = append(writers[t], c.Ref())
		}
	}
	tables := make([]string, 0, len(writers))
	for t, w := range writers {
		if len(w) >= 2 {
			tables = append(tables, t)
		}
	}
	if len(tables) == 0 {
		return nil
	}
	sort.Slice(tables, func(i, j int) bool {
		if len(writers[tables[i]]) != len(writers[tables[j]]) {
			return len(writers[tables[i]]) > len(writers[tables[j]])
		}
		return tables[i] < tables[j]
	})
	var ev []Evidence
	for _, t := range tables {
		if len(ev) >= maxFindingExamples {
			break
		}
		w := writers[t]
		sort.Strings(w)
		ev = append(ev, Evidence{
			Kind: SourceSQL,
			Detail: fmt.Sprintf("%s is written by %d capabilities: %s",
				t, len(w), strings.Join(w, ", ")),
		})
	}
	return &Finding{
		Code: "shared-table-writes", Severity: SeverityHigh, Provisional: true,
		Title: fmt.Sprintf("%s written from more than one capability",
			plural(len(tables), "table is", "tables are")),
		Detail: "A shared writer is coupling that no import graph shows: a schema or " +
			"invariant change in one capability lands in the other's rows.",
		Count: len(tables), Evidence: ev,
		Next: "atlas sql capabilities",
	}
}

// soleTableOwners is the inverse read of the same data, and it is the one
// that usually surprises: most tables turn out to have exactly one writer,
// which means the boundary the team argues about already exists in the code.
func soleTableOwners(res Result) *Finding {
	writers := map[string][]string{}
	for _, c := range res.Capabilities {
		for _, t := range c.Writes {
			writers[t] = append(writers[t], c.Ref())
		}
	}
	type pair struct{ table, owner string }
	var sole []pair
	for t, w := range writers {
		if len(w) == 1 {
			sole = append(sole, pair{t, w[0]})
		}
	}
	if len(sole) == 0 {
		return nil
	}
	sort.Slice(sole, func(i, j int) bool { return sole[i].table < sole[j].table })
	var ev []Evidence
	for _, p := range sole {
		if len(ev) >= maxFindingExamples {
			break
		}
		ev = append(ev, Evidence{Kind: SourceSQL, Detail: p.table + " is written only by " + p.owner})
	}
	return &Finding{
		Code: "sole-table-owners", Severity: SeverityInfo, Provisional: true,
		Title: fmt.Sprintf("%s exactly one writing capability",
			plural(len(sole), "table has", "tables have")),
		Detail: "Single-writer tables are an ownership boundary the code already " +
			"enforces. They are the cheapest capabilities to declare first.",
		Count: len(sole), Evidence: ev,
	}
}

// hotAndUntested is the finding with the shortest half-life: code being
// changed right now that nothing verifies. It needs both signals -- churn
// alone ranks busy code, test evidence alone ranks old code -- which is why
// it degrades to nothing rather than to half of itself when git is absent.
func hotAndUntested(res Result) *Finding {
	type hot struct {
		cap   Capability
		score float64
	}
	var hits []hot
	for _, c := range res.Capabilities {
		if !c.Churn.Known() || c.Churn.Score < hotChurnScore || c.TestEvidence != TestEvidenceNone {
			continue
		}
		hits = append(hits, hot{c, c.Churn.Score})
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].cap.ID < hits[j].cap.ID
	})
	var ev []Evidence
	for _, h := range hits {
		if len(ev) >= maxFindingExamples {
			break
		}
		ev = append(ev, Evidence{
			Kind: SourceChurn,
			Detail: fmt.Sprintf("%s: churn %.0f, %d commits, last touched %s, no test evidence",
				h.cap.Ref(), h.score, h.cap.Churn.Commits, h.cap.Churn.LastCommit),
			File: h.cap.Churn.HotFile,
		})
	}
	return &Finding{
		Code: "changing-and-untested", Severity: SeverityHigh, Provisional: true,
		Title: fmt.Sprintf("%s under active change with no test reaching them",
			plural(len(hits), "capability is", "capabilities are")),
		Detail: "Ranked by git history inside the churn window. These are where a " +
			"regression is most likely and least likely to be caught.",
		Count: len(hits), Evidence: ev,
		Next: "atlas hotspots",
	}
}

// sqlAdvisories rolls the SQL checks up by code. It is a finding rather than
// a section because the individual advisories already have a home in
// `atlas sql advise`; what the first run owes the reader is the fact that
// the checks exist and fired at all.
func sqlAdvisories(in Input) *Finding {
	if len(in.Advisories) == 0 {
		return nil
	}
	byCode := map[string][]sqlops.Advisory{}
	for _, a := range in.Advisories {
		byCode[a.Code] = append(byCode[a.Code], a)
	}
	codes := make([]string, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		if len(byCode[codes[i]]) != len(byCode[codes[j]]) {
			return len(byCode[codes[i]]) > len(byCode[codes[j]])
		}
		return codes[i] < codes[j]
	})
	var ev []Evidence
	for _, c := range codes {
		if len(ev) >= maxFindingExamples {
			break
		}
		first := byCode[c][0]
		ev = append(ev, Evidence{
			Kind:   SourceSQL,
			Detail: fmt.Sprintf("%s x%d -- %s", c, len(byCode[c]), first.Message),
			File:   first.Position.Path, Line: first.Position.Line,
			Symbol: first.Symbol,
		})
	}
	return &Finding{
		Code: "sql-advisories", Severity: SeverityMedium,
		Title:  fmt.Sprintf("%d SQL advisories across %d checks", len(in.Advisories), len(codes)),
		Detail: "Unbounded reads, unstable pagination, filters no index serves.",
		Count:  len(in.Advisories), Evidence: ev,
		Next: "atlas sql advise",
	}
}

// deadCode is last because it is the finding most likely to be wrong.
// Reflection, plugin registries and entry points all look identical to dead
// code in a static graph, so this is a triage list and the wording has to
// keep saying so.
func deadCode(in Input) *Finding {
	// Count and cite the same set. Fixtures and tests exist to be
	// unreferenced, so counting them while showing only production symbols
	// prints a headline the evidence beneath it cannot account for.
	var (
		ev []Evidence
		n  int
	)
	for _, d := range in.Dead {
		if excludedPath(d.Symbol.FilePath) || isTestPath(d.Symbol.FilePath) {
			continue
		}
		n++
		if len(ev) >= maxFindingExamples {
			continue
		}
		ev = append(ev, Evidence{
			Kind: SourceDirectory, Detail: string(d.Symbol.QualifiedName),
			File: d.Symbol.FilePath, Line: d.Symbol.Line,
		})
	}
	if n == 0 {
		return nil
	}
	return &Finding{
		Code: "dead-code-candidates", Severity: SeverityInfo,
		Title: fmt.Sprintf("%s no incoming reference atlas can see",
			plural(n, "symbol has", "symbols have")),
		Detail: "A candidate list, not a verdict: dynamic dispatch, entry points and " +
			"plugin registries all look like this to a static graph.",
		Count: n, Evidence: ev,
		Next: "atlas codebase dead",
	}
}

// plural renders "1 table is" / "3 tables are". A finding is read once, by
// somebody deciding whether to keep reading, and "1 tables are" is the kind
// of seam that makes a reader discount the number beside it.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func firstOr(ss []string, fallback string) string {
	if len(ss) == 0 {
		return fallback
	}
	return ss[0]
}
