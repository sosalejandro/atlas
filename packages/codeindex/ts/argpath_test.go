package tsscan

import (
	"context"
	"os/exec"
	"path/filepath"
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
// nothing. On POSIX hosts this is a no-op assertion on the paths; the
// value is that it fails loudly if buildScannerArgs stops normalising.
func TestBuildScannerArgs_EmitsSlashPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	script := filepath.Join(root, "scanner.ts")

	args, err := buildScannerArgs(script, root, Options{
		Include:      []string{filepath.Join("src", "**", "*.ts")},
		Exclude:      []string{filepath.Join("dist", "**")},
		TsconfigPath: filepath.Join(root, "tsconfig.json"),
	})
	if err != nil {
		t.Fatalf("buildScannerArgs: %v", err)
	}
	for i, a := range args {
		if strings.Contains(a, `\`) {
			t.Errorf("args[%d] = %q still carries a backslash; "+
				"Node and the metacharacter guard both want forward slashes", i, a)
		}
	}
	if want := filepath.ToSlash(script); args[1] != want {
		t.Errorf("args[1] = %q, want the normalised script path %q", args[1], want)
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
