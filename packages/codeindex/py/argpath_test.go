package pyscan

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Windows portability for the Python scanner (issue #143). Nine tests in
// this package failed on the Windows CI leg; all but one of them failed
// for the same reason the TS scanner did — buildScannerArgs rejected the
// backslash in `C:\Users\RUNNER~1\...\scanner.py` as a shell
// metacharacter, so Scan never got as far as spawning Python and every
// end-to-end test (TestResolver_CrossModule, TestScanner_SampleProject,
// ...) failed downstream of one guard.
//
// The separator is a parameter here, not filepath.Separator, so both
// shapes are asserted on every host.

func TestSanitizeScannerPathArg_BothSeparators(t *testing.T) {
	t.Parallel()
	const winTemp = `C:\Users\RUNNER~1\AppData\Local\Temp\atlas-pyscan-9931\scanner.py`

	tests := []struct {
		name    string
		in      string
		sep     rune
		want    string
		wantErr bool
	}{
		{
			name: "posix path is unchanged",
			in:   "/tmp/atlas-pyscan-9931/scanner.py",
			sep:  '/',
			want: "/tmp/atlas-pyscan-9931/scanner.py",
		},
		{
			name: "windows temp path is accepted as forward slashes",
			in:   winTemp,
			sep:  '\\',
			want: "C:/Users/RUNNER~1/AppData/Local/Temp/atlas-pyscan-9931/scanner.py",
		},
		{
			name: "windows glob pattern",
			in:   `src\**\*.py`,
			sep:  '\\',
			want: "src/**/*.py",
		},
		{
			// The POSIX rule is unchanged: there '\' is not a separator, so
			// it stays a metacharacter and stays rejected.
			name:    "backslash on posix is still rejected",
			in:      winTemp,
			sep:     '/',
			wantErr: true,
		},
		{
			name:    "command separator rejected on windows too",
			in:      `C:\repo;calc.exe`,
			sep:     '\\',
			wantErr: true,
		},
		{
			name:    "command substitution rejected",
			in:      "/tmp/$(id)/scanner.py",
			sep:     '/',
			wantErr: true,
		},
		{
			name:    "quote rejected on windows (argv escaping hazard)",
			in:      `C:\repo\a"b`,
			sep:     '\\',
			wantErr: true,
		},
		{
			name:    "leading dash is flag injection",
			in:      "--root",
			sep:     '/',
			wantErr: true,
		},
		{
			name:    "newline rejected",
			in:      "/tmp/a\nb",
			sep:     '/',
			wantErr: true,
		},
		{
			name:    "NUL rejected",
			in:      "/tmp/a\x00b",
			sep:     '/',
			wantErr: true,
		},
		{
			name:    "empty rejected",
			in:      "",
			sep:     '/',
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sanitizeScannerPathArg(tt.in, tt.sep)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sanitizeScannerPathArg(%q, %q) err = %v, wantErr = %v",
					tt.in, string(tt.sep), err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("sanitizeScannerPathArg(%q, %q) = %q, want %q",
					tt.in, string(tt.sep), got, tt.want)
			}
		})
	}
}

// TestBuildScannerArgs_EmitsSlashPaths asserts the sanitiser is wired into
// the argv builder rather than sitting unused beside it.
func TestBuildScannerArgs_EmitsSlashPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	script := filepath.Join(root, "scanner.py")

	args, err := buildScannerArgs(script, root, Options{
		Include: []string{filepath.Join("src", "**", "*.py")},
		Exclude: []string{filepath.Join("build", "**")},
	})
	if err != nil {
		t.Fatalf("buildScannerArgs: %v", err)
	}
	for i, a := range args {
		if strings.Contains(a, `\`) {
			t.Errorf("args[%d] = %q still carries a backslash; "+
				"Python and the metacharacter guard both want forward slashes", i, a)
		}
	}
	if want := filepath.ToSlash(script); args[0] != want {
		t.Errorf("args[0] = %q, want the normalised script path %q", args[0], want)
	}
}

// TestNewPythonCommand_IsArgvNotShell pins the invariant that makes a path
// separator harmless in an argument: the scanner is spawned argv-style and
// never assembled into a shell command line, so nothing downstream can
// reinterpret a character the guard let through.
func TestNewPythonCommand_IsArgvNotShell(t *testing.T) {
	t.Parallel()
	bin, err := exec.LookPath("go") // any real absolute binary will do
	if err != nil {
		t.Skipf("no probe binary on PATH: %v", err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatalf("abs probe binary: %v", err)
	}

	args := []string{"/tmp/atlas/scanner.py", "--root", "/tmp/atlas"}
	cmd, err := newPythonCommand(context.Background(), bin, args)
	if err != nil {
		t.Fatalf("newPythonCommand: %v", err)
	}
	if cmd.Path != bin {
		t.Errorf("cmd.Path = %q, want the resolved binary %q", cmd.Path, bin)
	}
	if len(cmd.Args) != len(args)+1 || cmd.Args[0] != bin {
		t.Fatalf("cmd.Args = %q; want the binary followed by argv verbatim", cmd.Args)
	}
	for i, a := range args {
		if cmd.Args[i+1] != a {
			t.Errorf("cmd.Args[%d] = %q, want %q (argv must pass through untouched)",
				i+1, cmd.Args[i+1], a)
		}
	}
	for _, forbidden := range []string{"sh", "bash", "cmd.exe", "powershell", "-c", "/c"} {
		for _, a := range cmd.Args {
			if strings.EqualFold(filepath.Base(a), forbidden) {
				t.Fatalf("argv contains shell wrapper %q: %q", forbidden, cmd.Args)
			}
		}
	}
}

// TestValidatePythonBin_PlatformAbsoluteForms states what "absolute"
// means on each host instead of assuming a leading "/".
//
// The old table asserted `/usr/bin/python3` is accepted, full stop. On
// Windows filepath.IsAbs rejects it (no volume), so the test failed —
// and it failed on a *correct* implementation: exec.LookPath there returns
// `C:\...\python.exe`, which the strict validator must accept. The
// host-dependence belongs in the expectations, not hidden in the one form
// the author happened to run on, so both forms are listed with the
// platform that makes each absolute.
func TestValidatePythonBin_PlatformAbsoluteForms(t *testing.T) {
	t.Parallel()
	windows := runtime.GOOS == "windows"
	cases := []struct {
		s       string
		wantErr bool
		why     string
	}{
		{"/usr/bin/python3", windows, "POSIX absolute: rooted, but has no volume on Windows"},
		{`C:\Python312\python.exe`, !windows, "Windows absolute: a drive letter is not a root on POSIX"},
		{"C:/Python312/python.exe", !windows, "Windows absolute, forward-slash form"},
		{"python3", true, "relative — the post-LookPath invariant rejects it everywhere"},
		{`.\python.exe`, true, "explicitly relative"},
		{"", true, "empty"},
		{"py;ls", true, "shell metacharacter"},
		{"py\nthon3", true, "newline"},
	}
	for _, tc := range cases {
		err := validatePythonBin(tc.s)
		if (err != nil) != tc.wantErr {
			t.Errorf("validatePythonBin(%q) err = %v; wantErr = %v (%s)",
				tc.s, err, tc.wantErr, tc.why)
		}
	}
}
