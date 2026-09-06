package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// findCmd returns the root subcommand registered under name (its canonical
// name, not an alias).
func findCmd(t *testing.T, name string) *cobra.Command {
	t.Helper()
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no subcommand named %q on the root command", name)
	return nil
}

// Every renamed verb keeps a working alias -- the acceptance criterion on
// issue #112, and the reason the rename is safe to make before #101 hardens
// a public API. A retired name that stops resolving is not a rename, it is
// a breaking change with a changelog entry.
func TestRenamedVerbs_OldNamesStillResolve(t *testing.T) {
	for old, canonical := range renamedVerbs {
		t.Run(old, func(t *testing.T) {
			root := NewRootCmd()
			cmd, _, err := root.Find([]string{old})
			if err != nil {
				t.Fatalf("root.Find(%q): %v", old, err)
			}
			if cmd.Name() != canonical {
				t.Fatalf("`atlas %s` resolved to %q, want %q", old, cmd.Name(), canonical)
			}
		})
	}
}

// Cobra resolves aliases only when they are declared on the command, so the
// map and the command tree have to agree in both directions. A verb listed
// in renamedVerbs whose replacement forgot aliasesFor() would pass the
// lookup test above only by accident of prefix matching.
func TestRenamedVerbs_AliasesAreDeclaredOnTheCommand(t *testing.T) {
	for old, canonical := range renamedVerbs {
		cmd := findCmd(t, canonical)
		var found bool
		for _, a := range cmd.Aliases {
			if a == old {
				found = true
			}
		}
		if !found {
			t.Errorf("`atlas %s` does not declare %q in its Aliases (has %v)",
				canonical, old, cmd.Aliases)
		}
	}
}

// The deprecation note is a one-liner on STDERR. On stderr because --json
// consumers pipe stdout into jq and a courtesy message in the middle of the
// envelope is a parse error, not a courtesy.
//
// Driven end to end rather than by calling noteIfRenamed directly: the name
// a command was invoked under is state cobra fills in during execution
// (Command.CalledAs), so a unit test that pokes the tree without running it
// would assert on a field nothing had set.
func TestRenamedVerbs_ChainAliasWarnsOnStderrOnly(t *testing.T) {
	fix := newChainFixture(t)
	fix.seedChain(t, "pkg.Root", "pkg.Mid", "pkg.Leaf")

	_, stderr, err := runChainCmd(t, fix, "pkg.Root")
	if err != nil {
		t.Fatalf("`atlas chain` failed: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "renamed") {
		t.Errorf("`atlas chain` printed a rename note it should not: %q", stderr)
	}

	root := NewRootCmd()
	var so, se bytes.Buffer
	root.SetOut(&so)
	root.SetErr(&se)
	root.SetArgs([]string{"trace", "--db-path", fix.dbPath, "pkg.Root"})
	if err := root.Execute(); err != nil {
		t.Fatalf("`atlas trace` (alias) failed: %v\nstderr:\n%s", err, se.String())
	}
	note := se.String()
	if !strings.Contains(note, "`atlas trace`") || !strings.Contains(note, "`atlas chain`") {
		t.Errorf("note should name both verbs, got %q", note)
	}
	if strings.Count(strings.TrimSpace(note), "\n") != 0 {
		t.Errorf("deprecation note must be one line, got %q", note)
	}
	if strings.Contains(so.String(), "renamed") {
		t.Errorf("rename note leaked into stdout: %q", so.String())
	}
}

// The renamed verbs must actually run under the old name, not merely
// resolve. `atlas audit` against an empty store is the cheapest end-to-end
// path through the alias.
func TestRenamedVerbs_AuditAliasRunsAndWarns(t *testing.T) {
	fix := newChainFixture(t)
	// Touch the store so the DB file exists with a current schema.
	_ = fix.openStore(t)

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"audit", "--db-path", fix.dbPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("`atlas audit` (alias) failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "renamed to `atlas health`") {
		t.Errorf("`atlas audit` printed no rename note; stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "renamed") {
		t.Errorf("rename note leaked into stdout: %q", stdout.String())
	}
}

func TestRenamedVerbs_HealthCanonicalNameIsSilent(t *testing.T) {
	fix := newChainFixture(t)
	_ = fix.openStore(t)

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"health", "--db-path", fix.dbPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("`atlas health` failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "renamed") {
		t.Errorf("`atlas health` printed a rename note it should not: %q", stderr.String())
	}
}

// The JSON envelope names the canonical verb. A consumer switching on
// `command` is exactly the consumer #112 wants to move before #101 freezes
// the field, so the alias must NOT report itself as a distinct command.
func TestRenamedVerbs_JSONEnvelopeUsesTheCanonicalVerb(t *testing.T) {
	fix := newChainFixture(t)
	_ = fix.openStore(t)

	for _, invoked := range []string{"health", "audit"} {
		root := NewRootCmd()
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{invoked, "--json", "--db-path", fix.dbPath})
		if err := root.Execute(); err != nil {
			t.Fatalf("`atlas %s --json` failed: %v\nstderr:\n%s", invoked, err, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"command": "health"`) {
			t.Errorf("`atlas %s --json` envelope does not name health: %s", invoked, stdout.String())
		}
	}
}
