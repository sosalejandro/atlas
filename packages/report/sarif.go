package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SARIF wire constants. The $schema is what several ingests (VS Code's SARIF
// viewer among them) use to pick a validator; omitting it is accepted by GitHub
// but rejected elsewhere, so it is always emitted.
const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://json.schemastore.org/sarif-2.1.0.json"

	// sarifColumnKind must be declared when a run reports regions at all.
	// utf16CodeUnits is the SARIF default and what GitHub assumes; stating
	// it explicitly avoids a consumer defaulting to utf8CodeUnits and
	// shifting every column.
	sarifColumnKind = "utf16CodeUnits"
)

// Tool identifies the driver in the SARIF run. The CLI fills it from its build
// metadata; tests pin it so the golden file does not churn with the version.
type Tool struct {
	// Name is the driver name GitHub shows as the analysis tool.
	Name string

	// InformationURI is the tool's home page. GitHub links the alert's
	// "Tool" chip to it.
	InformationURI string

	// SemanticVersion is a semver string. A leading "v" is NOT semver and
	// some ingests reject the document over it, so RenderSARIF strips one
	// rather than trusting the caller — atlas's own Version constant
	// carries the prefix.
	SemanticVersion string
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool     `json:"tool"`
	Results    []sarifResult `json:"results"`
	ColumnKind string        `json:"columnKind"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	InformationURI  string      `json:"informationUri"`
	SemanticVersion string      `json:"semanticVersion"`
	Rules           []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	ShortDescription     sarifText      `json:"shortDescription"`
	FullDescription      sarifText      `json:"fullDescription"`
	HelpURI              string         `json:"helpUri,omitempty"`
	DefaultConfiguration sarifConfig    `json:"defaultConfiguration"`
	Properties           map[string]any `json:"properties,omitempty"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifConfig struct {
	Level string `json:"level"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
	Properties          map[string]any    `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           sarifRegion   `json:"region"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine,omitempty"`
}

// RenderSARIF writes a SARIF 2.1.0 log for `findings` to w.
//
// It returns an error rather than emitting a result whose rule is not in the
// catalog. That is the whole reason this is not a thin json.Marshal: GitHub
// drops a result with an undeclared ruleId without logging anything, so the
// upload succeeds, the check goes green, and the finding is simply absent. A
// build failure here is strictly better than a report that lies.
//
// Findings must already carry repo-relative paths (see NormalizePaths) — an
// absolute uri makes the finding vanish from the PR's Files view the same
// silent way.
func RenderSARIF(w io.Writer, tool Tool, findings []Finding) error {
	sorted := Sort(findings)

	rules, index, err := sarifRulesFor(sorted)
	if err != nil {
		return err
	}

	results := make([]sarifResult, 0, len(sorted))
	for _, f := range sorted {
		// sarifRulesFor has already refused any finding whose rule is not
		// in the catalog, so the lookup cannot miss here.
		rule, _ := LookupRule(f.RuleID)
		results = append(results, sarifResultFor(f, rule, index[f.RuleID]))
	}

	log := sarifLog{
		Schema:  sarifSchema,
		Version: sarifVersion,
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:            tool.Name,
				InformationURI:  tool.InformationURI,
				SemanticVersion: strings.TrimPrefix(tool.SemanticVersion, "v"),
				Rules:           rules,
			}},
			Results:    results,
			ColumnKind: sarifColumnKind,
		}},
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(log); err != nil {
		return fmt.Errorf("render sarif: %w", err)
	}
	return nil
}

// sarifRulesFor declares exactly the rules the findings reference, and returns
// each rule's index in the emitted array.
//
// Only referenced rules are declared: a driver advertising rules that produced
// no results reads, in GitHub's UI, as "these checks ran and passed", which is
// a claim atlas is not making — a rule is absent here when its producer was not
// asked to run at all.
func sarifRulesFor(findings []Finding) ([]sarifRule, map[string]int, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(findings))
	for _, f := range findings {
		if _, ok := LookupRule(f.RuleID); !ok {
			return nil, nil, fmt.Errorf(
				"render sarif: finding at %s:%d references rule %q which is not in the catalog; "+
					"GitHub would drop it silently", f.Path, f.Line, f.RuleID)
		}
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			ids = append(ids, f.RuleID)
		}
	}
	sort.Strings(ids)

	rules := make([]sarifRule, 0, len(ids))
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		r, _ := LookupRule(id)
		index[id] = i
		rules = append(rules, sarifRule{
			ID:                   r.ID,
			Name:                 r.Name,
			ShortDescription:     sarifText{Text: r.ShortDescription},
			FullDescription:      sarifText{Text: r.FullDescription},
			HelpURI:              r.HelpURI,
			DefaultConfiguration: sarifConfig{Level: string(r.DefaultLevel)},
			Properties:           ruleProperties(r),
		})
	}
	return rules, index, nil
}

// ruleProperties builds the rule's properties bag. Returns nil rather than an
// empty object when there is nothing to say, so the golden file does not carry
// `"properties": {}` noise.
func ruleProperties(r Rule) map[string]any {
	props := map[string]any{}
	if len(r.Tags) > 0 {
		props["tags"] = r.Tags
	}
	if r.SecuritySeverity != "" {
		props["security-severity"] = r.SecuritySeverity
	}
	if len(props) == 0 {
		return nil
	}
	return props
}

// sarifResultFor converts one finding into a result.
//
// Severity is encoded twice on purpose. `level` is what GitHub renders as the
// annotation's colour and what it gates on; properties.security-severity is
// the number it sorts the alert list by, and an alert without one sinks below
// every scored alert regardless of its level.
// `rule` is the finding's catalog entry, passed in rather than looked up again
// so the two halves of the encoding come from one place: `level` from the
// finding (falling back to the rule only when the producer had no opinion),
// security-severity from the rule.
func sarifResultFor(f Finding, rule Rule, ruleIndex int) sarifResult {
	level := f.Severity
	if level == "" {
		level = rule.DefaultLevel
	}

	start, end := f.span()

	var props map[string]any
	if rule.SecuritySeverity != "" {
		props = map[string]any{"security-severity": rule.SecuritySeverity}
	}

	return sarifResult{
		RuleID:    f.RuleID,
		RuleIndex: ruleIndex,
		Level:     string(level),
		Message:   sarifText{Text: f.Message},
		Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifact{URI: f.Path},
			Region:           sarifRegion{StartLine: start, EndLine: end},
		}}},
		PartialFingerprints: map[string]string{FingerprintKey: f.Fingerprint()},
		Properties:          props,
	}
}
