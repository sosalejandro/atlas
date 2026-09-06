package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runResolveCmd drives the real command tree so the test exercises flag
// wiring and the JSON envelope, not just the render helpers.
func runResolveCmd(t *testing.T, args ...string) (string, string) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("atlas %s: %v\nstderr: %s", strings.Join(args, " "), err, errBuf.String())
	}
	return out.String(), errBuf.String()
}

// The point of the verb is the histogram, and the point of the histogram
// is that it is DIFFABLE: every tier present every time, in a fixed
// order, even at zero. A column that appears only when non-zero cannot be
// compared between two runs, which is the only thing anyone does with it.
func TestResolve_JSONCarriesEveryTierEvenAtZero(t *testing.T) {
	stdout, _ := runResolveCmd(t, "resolve", "--json",
		"--root", "../../packages/codeindex/go/testdata/goldencorpus")

	var env struct {
		Command string `json:"command"`
		Result  struct {
			Tiers       map[string]int `json:"tiers"`
			TypeChecked int            `json:"type_checked"`
			Degraded    int            `json:"degraded"`
			Edges       int            `json:"edges"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.Command != "resolve" {
		t.Errorf("command = %q, want resolve", env.Command)
	}
	for _, tier := range []string{"typed", "name_resolved", "syntactic", "imported"} {
		if _, ok := env.Result.Tiers[tier]; !ok {
			t.Errorf("tier %q missing from the histogram: %v", tier, env.Result.Tiers)
		}
	}
	if env.Result.Tiers["typed"] == 0 {
		t.Errorf("no typed edges for a fixture module that compiles: %v", env.Result.Tiers)
	}
	if env.Result.Degraded != 0 {
		t.Errorf("degraded = %d for a fixture that compiles", env.Result.Degraded)
	}
	total := 0
	for _, n := range env.Result.Tiers {
		total += n
	}
	if total != env.Result.Edges {
		t.Errorf("tiers sum to %d but the scan reported %d edges; some edge carries a tier "+
			"outside the closed vocabulary", total, env.Result.Edges)
	}
}

// --ast is the review instrument: two histograms and the delta between
// them. The number that must never move the wrong way is the syntactic
// count, so the comparison has to actually contain both sides.
func TestResolve_ASTComparisonShowsBothSides(t *testing.T) {
	stdout, _ := runResolveCmd(t, "resolve", "--ast", "--json",
		"--root", "../../packages/codeindex/go/testdata/goldencorpus")

	var env struct {
		Result struct {
			Tiers    map[string]int `json:"tiers"`
			ASTTiers map[string]int `json:"ast_tiers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.Result.ASTTiers == nil {
		t.Fatal("--ast produced no fallback histogram")
	}
	if env.Result.ASTTiers["typed"] != 0 {
		t.Errorf("the --ast column claims %d typed edges", env.Result.ASTTiers["typed"])
	}
	if env.Result.Tiers["syntactic"] > env.Result.ASTTiers["syntactic"] {
		t.Errorf("typed resolution INCREASED the guesses: %d -> %d",
			env.Result.ASTTiers["syntactic"], env.Result.Tiers["syntactic"])
	}
}

// A tree that does not compile must produce a report, not an error, and
// the report must name the package rather than leaving the operator to
// guess which one degraded.
func TestResolve_NamesTheDegradedPackage(t *testing.T) {
	stdout, _ := runResolveCmd(t, "resolve",
		"--root", "../../packages/codeindex/go/testdata/brokencorpus")

	if !strings.Contains(stdout, "1 degraded") {
		t.Errorf("output does not report the degraded package:\n%s", stdout)
	}
	if !strings.Contains(stdout, "broken") {
		t.Errorf("output does not name the degraded package:\n%s", stdout)
	}
	// The scan still happened: the package that compiles is resolved.
	if !strings.Contains(stdout, "typed") {
		t.Errorf("output has no tier table:\n%s", stdout)
	}
}
