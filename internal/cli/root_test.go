package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestNewRootCmd_WiresEverySubcommand ensures Phase 7's full verb set is
// reachable from the root command. If a refactor accidentally drops a
// subcommand the help output catches it before users do.
func TestNewRootCmd_WiresEverySubcommand(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	want := []string{
		"init", "scan", "trace", "cov", "audit",
		"sprint", "diff", "snapshot", "contract",
		"diagnose", "codebase", "migrate-annotations",
	}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, v := range want {
		if !got[v] {
			t.Errorf("missing subcommand %q (have: %v)", v, got)
		}
	}
}

// TestRootCmd_HelpExitsZero confirms `atlas --help` succeeds with
// non-empty output. Cobra surfaces an error for "no args" via
// SilenceUsage=true; the help flag is the well-trodden no-args path.
func TestRootCmd_HelpExitsZero(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help returned error: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Available Commands") {
		t.Fatalf("help output missing command list:\n%s", stdout.String())
	}
}

// TestCodebaseFind_DottedSuffix verifies the helper that powers
// `atlas codebase find` accepts a non-dotted leaf identifier even when
// the qualified name is dotted (e.g. "Login" ↔ "auth.AuthHandler.Login").
func TestCodebaseFind_DottedSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		qn, suffix string
		want       bool
	}{
		{"auth.AuthHandler.Login", "Login", true},
		{"auth.AuthHandler.Login", "Auth", false}, // partial token in the middle
		{"PatientService", "PatientService", true},
		{"NewPatientService", "PatientService", false}, // not at boundary
		{"", "Login", false},
	}
	for _, tc := range cases {
		if got := hasDottedSuffix(tc.qn, tc.suffix); got != tc.want {
			t.Errorf("hasDottedSuffix(%q, %q) = %v; want %v", tc.qn, tc.suffix, got, tc.want)
		}
	}
}
