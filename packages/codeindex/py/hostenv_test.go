package pyscan

import (
	"runtime"
	"strings"
	"testing"
)

// `fold` is a parameter for the reason it is a parameter everywhere else
// in this package (issue #143): a table that read runtime.GOOS would
// assert the POSIX answer on the host CI gates hardest on and the
// Windows answer nowhere.

func TestTakeEnv_MatchesTheNameByTheHostsRules(t *testing.T) {
	t.Parallel()
	names := []string{"PYTHONIOENCODING", "PYTHONDONTWRITEBYTECODE"}
	env := []string{
		"PATH=/usr/bin",
		"PythonIOEncoding=latin-1",
		"PYTHONDONTWRITEBYTECODE=0",
		"PYTHONIOENCODING_EXTRA=keepme",
		"NOT_AN_ASSIGNMENT",
	}

	t.Run("posix: only the exact spelling is the same variable", func(t *testing.T) {
		t.Parallel()
		got := takeEnv(append([]string(nil), env...), names, false)
		want := []string{
			"PATH=/usr/bin",
			"PythonIOEncoding=latin-1",
			"PYTHONIOENCODING_EXTRA=keepme",
			"NOT_AN_ASSIGNMENT",
		}
		assertEnv(t, got, want)
	})

	t.Run("windows: case does not distinguish two variables", func(t *testing.T) {
		t.Parallel()
		got := takeEnv(append([]string(nil), env...), names, true)
		want := []string{
			"PATH=/usr/bin",
			// A longer name that merely starts the same way must survive,
			// or the child loses a variable nobody asked to remove.
			"PYTHONIOENCODING_EXTRA=keepme",
			"NOT_AN_ASSIGNMENT",
		}
		assertEnv(t, got, want)
	})
}

// TestBuildScannerEnv_SetsExactlyOneEncoding is the wiring assertion: the
// stripping helper is only worth anything if buildScannerEnv calls it,
// and on Windows a missed strip leaves the child with two assignments to
// one variable and the answer decided by os/exec rather than here.
func TestBuildScannerEnv_SetsExactlyOneEncoding(t *testing.T) {
	t.Setenv("PYTHONIOENCODING", "latin-1")
	t.Setenv("PYTHONDONTWRITEBYTECODE", "0")

	env := buildScannerEnv()
	counts := map[string]int{}
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		name := kv[:eq]
		if hostFoldsEnvCase() {
			name = strings.ToUpper(name)
		}
		counts[name]++
	}
	for _, name := range []string{"PYTHONIOENCODING", "PYTHONDONTWRITEBYTECODE"} {
		if counts[name] != 1 {
			t.Errorf("%s appears %d times in the child env, want exactly 1", name, counts[name])
		}
	}
	if !containsEnv(env, "PYTHONIOENCODING=utf-8") {
		t.Errorf("child env does not set PYTHONIOENCODING=utf-8: %q", env)
	}
	if !containsEnv(env, "PYTHONDONTWRITEBYTECODE=1") {
		t.Errorf("child env does not set PYTHONDONTWRITEBYTECODE=1: %q", env)
	}
}

func TestHostFoldsEnvCase_MatchesTheHost(t *testing.T) {
	t.Parallel()
	if got, want := hostFoldsEnvCase(), runtime.GOOS == "windows"; got != want {
		t.Errorf("hostFoldsEnvCase() = %v on %s, want %v", got, runtime.GOOS, want)
	}
}

func assertEnv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("env = %q (%d), want %q (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func containsEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}
