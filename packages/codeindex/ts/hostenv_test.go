package tsscan

import (
	"runtime"
	"testing"
)

// Two more instances of the class issue #143 is about: a comparison
// whose correct answer depends on the host, written as if the host were
// always Linux. Neither one fails a test on Windows today — they are
// silent wrong answers, which is why they were still here after the
// first pass at #143 fixed the loud ones.
//
// `fold` is a parameter for the reason it is a parameter everywhere else
// in this package: a table that read runtime.GOOS would assert the POSIX
// answer on the host CI gates hardest on and the Windows answer nowhere.

func TestTakeEnv_MatchesTheNameByTheHostsRules(t *testing.T) {
	t.Parallel()
	env := []string{
		"PATH=/usr/bin",
		"Node_Path=/opt/nm",
		"NODE_PATH_EXTRA=/nope",
	}

	t.Run("posix: the environment block is case sensitive", func(t *testing.T) {
		t.Parallel()
		rest, got, ok := takeEnv(append([]string(nil), env...), "NODE_PATH", false)
		if ok {
			t.Errorf("takeEnv matched %q as NODE_PATH; on POSIX they are two variables", "Node_Path")
		}
		if got != "" {
			t.Errorf("value = %q, want empty", got)
		}
		if len(rest) != len(env) {
			t.Errorf("rest = %q; nothing should have been removed", rest)
		}
	})

	t.Run("windows: the environment block is case insensitive", func(t *testing.T) {
		t.Parallel()
		rest, got, ok := takeEnv(append([]string(nil), env...), "NODE_PATH", true)
		if !ok {
			t.Fatalf("takeEnv did not match %q as NODE_PATH; on Windows it is the same variable", "Node_Path")
		}
		if got != "/opt/nm" {
			t.Errorf("value = %q, want %q", got, "/opt/nm")
		}
		// The prefix match must not eat a longer name that merely starts
		// the same way, or NODE_PATH_EXTRA disappears from the child env.
		want := []string{"PATH=/usr/bin", "NODE_PATH_EXTRA=/nope"}
		if len(rest) != len(want) {
			t.Fatalf("rest = %q, want %q", rest, want)
		}
		for i := range want {
			if rest[i] != want[i] {
				t.Errorf("rest[%d] = %q, want %q", i, rest[i], want[i])
			}
		}
	})

	t.Run("an entry with no '=' is not a variable", func(t *testing.T) {
		t.Parallel()
		rest, _, ok := takeEnv([]string{"NODE_PATH"}, "NODE_PATH", true)
		if ok || len(rest) != 1 {
			t.Errorf("takeEnv(%q) = %q, %v; a bare name assigns nothing", "NODE_PATH", rest, ok)
		}
	})

	t.Run("an empty assignment is still an assignment", func(t *testing.T) {
		t.Parallel()
		_, got, ok := takeEnv([]string{"NODE_PATH="}, "NODE_PATH", false)
		if !ok || got != "" {
			t.Errorf(`takeEnv("NODE_PATH=") = %q, %v; want "", true`, got, ok)
		}
	})
}

func TestBaseNameIs_FollowsTheFilesystemsCaseRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    string
		fold bool
		want bool
	}{
		{"posix exact", "/repo/node_modules", false, true},
		{"posix, different case is a different directory", "/repo/Node_Modules", false, false},
		{"windows, native separators", `C:\repo\node_modules`, true, true},
		{"windows, case folded", `C:\repo\Node_Modules`, true, true},
		{"windows, slash-spelled root", "C:/repo/node_modules", true, true},
		{"windows, trailing separator", `C:\repo\node_modules\`, true, true},
		{"not the directory at all", "/repo/node_modules/typescript", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := baseNameIs(tt.p, "node_modules", tt.fold); got != tt.want {
				t.Errorf("baseNameIs(%q, node_modules, fold=%v) = %v, want %v",
					tt.p, tt.fold, got, tt.want)
			}
		})
	}
}

// hostFoldsPathCase is what production passes for `fold`. Pinning it
// keeps the seam the tables above test through from drifting away from
// the code path that runs.
func TestHostFoldsPathCase_MatchesTheHost(t *testing.T) {
	t.Parallel()
	if got, want := hostFoldsPathCase(), runtime.GOOS == "windows"; got != want {
		t.Errorf("hostFoldsPathCase() = %v on %s, want %v", got, runtime.GOOS, want)
	}
}
