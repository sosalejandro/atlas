package tsscan

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests cover the failure issue #143 reported from the Windows CI
// leg: every scan died in buildScannerArgs with
//
//	shell metacharacter '\' in argument "C:\Users\RUNNER~1\...\scanner.ts"
//
// The separator is passed in explicitly rather than read from
// filepath.Separator, so both the POSIX and the Windows path shapes are
// exercised on every host. Deriving it from the host is what made this
// bug invisible: the Windows branch would only ever run on the platform
// CI could not gate on.

func TestSanitizeScannerPathArg_BothSeparators(t *testing.T) {
	t.Parallel()
	const winTemp = `C:\Users\RUNNER~1\AppData\Local\Temp\atlas-tsscan-2417\scanner.ts`

	tests := []struct {
		name    string
		in      string
		sep     rune
		want    string
		wantErr bool
	}{
		{
			name: "posix path is unchanged",
			in:   "/tmp/atlas-tsscan-2417/scanner.ts",
			sep:  '/',
			want: "/tmp/atlas-tsscan-2417/scanner.ts",
		},
		{
			// The exact argument the Windows runner rejected.
			name: "windows temp path is accepted as forward slashes",
			in:   winTemp,
			sep:  '\\',
			want: "C:/Users/RUNNER~1/AppData/Local/Temp/atlas-tsscan-2417/scanner.ts",
		},
		{
			name: "windows glob pattern",
			in:   `src\**\*.tsx`,
			sep:  '\\',
			want: "src/**/*.tsx",
		},
		{
			// On POSIX a backslash is NOT a separator, so it stays a
			// metacharacter and stays rejected. The guard is corrected for
			// Windows, not weakened everywhere.
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
			in:      "/tmp/$(id)/scanner.ts",
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
			in:      "--eval",
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

// TestBuildScannerArgs_EmitsSlashPaths asserts the sanitiser is actually
// wired into the argv builder — a correct helper nobody calls fixes
// nothing.
//
// The inputs are literal backslash-bearing Windows paths and the
// separator is passed in as '\\', for the same reason
// TestSanitizeScannerPathArg_BothSeparators does it: an earlier version
// of this test built its inputs with filepath.Join, which on Linux
// already yields forward slashes. There was nothing left for the
// sanitiser to change, so the assertion held whether or not
// buildScannerArgs called it — on Linux, the platform CI gates hardest
// on, the test could not detect its own unwiring. Every argument below
// arrives with a backslash in it, so a builder that stopped normalising
// fails here on every host.
func TestBuildScannerArgs_EmitsSlashPaths(t *testing.T) {
	t.Parallel()
	const (
		root   = `C:\Users\RUNNER~1\AppData\Local\Temp\atlas-tsscan-2417`
		script = root + `\scanner.ts`
	)

	args, err := buildScannerArgsSep(script, root, Options{
		Include:      []string{`src\**\*.ts`},
		Exclude:      []string{`dist\**`},
		TsconfigPath: root + `\tsconfig.json`,
		Routers:      []RouterKind{ReactRouter},
	}, '\\')
	if err != nil {
		t.Fatalf("buildScannerArgsSep: %v", err)
	}
	for i, a := range args {
		if strings.Contains(a, `\`) {
			t.Errorf("args[%d] = %q still carries a backslash; "+
				"Node and the metacharacter guard both want forward slashes", i, a)
		}
	}
	// Naming the values, not just "no backslashes": a builder that
	// dropped an argument entirely would pass the loop above.
	want := []string{
		"--experimental-strip-types",
		"C:/Users/RUNNER~1/AppData/Local/Temp/atlas-tsscan-2417/scanner.ts",
		"--root", "C:/Users/RUNNER~1/AppData/Local/Temp/atlas-tsscan-2417",
		"--include", "src/**/*.ts",
		"--exclude", "dist/**",
		"--router", string(ReactRouter),
		"--tsconfig", "C:/Users/RUNNER~1/AppData/Local/Temp/atlas-tsscan-2417/tsconfig.json",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %q (%d), want %q (%d)", args, len(args), want, len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

// TestBuildScannerArgs_UsesTheHostSeparator pins the wrapper to the host,
// so the seam TestBuildScannerArgs_EmitsSlashPaths tests through cannot
// drift away from what production actually calls. On POSIX a backslash is
// not a separator, so it stays a metacharacter and the builder must
// reject it; on Windows the same input is a legitimate path.
func TestBuildScannerArgs_UsesTheHostSeparator(t *testing.T) {
	t.Parallel()
	const winScript = `C:\Temp\atlas\scanner.ts`

	_, err := buildScannerArgs(winScript, `C:\Temp\atlas`, Options{})
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("buildScannerArgs rejected a Windows path on Windows: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("buildScannerArgs accepted %q on %s; a backslash is not a path "+
			"separator there, so it is still a shell metacharacter",
			winScript, runtime.GOOS)
	}
}

// TestNewNodeCommand_IsArgvNotShell pins the invariant that makes the
// backslash safe to allow through on Windows: the scanner is never
// assembled into a shell command line, so nothing downstream can treat a
// path separator as an escape. Issue #143 called this out explicitly —
// the fix is to keep the spawn shell-free, not to trust the guard alone.
func TestNewNodeCommand_IsArgvNotShell(t *testing.T) {
	t.Parallel()
	bin, err := exec.LookPath("go") // any real absolute binary will do
	if err != nil {
		t.Skipf("no probe binary on PATH: %v", err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatalf("abs probe binary: %v", err)
	}

	args := []string{"--experimental-strip-types", "/tmp/atlas/scanner.ts", "--root", "/tmp/atlas"}
	cmd, err := newNodeCommand(context.Background(), bin, args)
	if err != nil {
		t.Fatalf("newNodeCommand: %v", err)
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
	// A shell wrapper is the only way a metacharacter could be reinterpreted.
	for _, forbidden := range []string{"sh", "bash", "cmd.exe", "powershell", "-c", "/c"} {
		for _, a := range cmd.Args {
			if strings.EqualFold(filepath.Base(a), forbidden) {
				t.Fatalf("argv contains shell wrapper %q: %q", forbidden, cmd.Args)
			}
		}
	}
}
