package resolver

import (
	"os"
	"path/filepath"
	"testing"
)

// A go.work `use` directive is a filesystem path written by a human, and
// on Windows a human writes `.\backend`. It becomes half of a `go list`
// PATTERN, which is always slash-separated regardless of host — so the
// conversion has to happen, and it has to happen unconditionally rather
// than through filepath, whose answer depends on the machine reading the
// file rather than the one that wrote it (issue #143).
//
// Getting this wrong is silent in the way the rest of #143 is silent: the
// pattern resolves to no package, the module is never loaded, and a
// monorepo reports a smaller call graph with no error anywhere.
func TestNormaliseUse_AcceptsEitherSeparator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"posix relative", "./backend", "backend"},
		{"windows relative", `.\backend`, "backend"},
		{"bare", "backend", "backend"},
		{"nested posix", "./services/api", "services/api"},
		{"nested windows", `.\services\api`, "services/api"},
		{"quoted, as go.work writes a path with a space", `"./my backend"`, "my backend"},
		{"trailing separator", "./backend/", "backend"},
		{"trailing windows separator", `.\backend\`, "backend"},
		{"current directory", ".", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normaliseUse(tt.in); got != tt.want {
				t.Errorf("normaliseUse(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoadPatterns_OneSlashSpelledPatternPerWorkspaceModule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Both spellings in one file, because a go.work edited on two
	// machines contains both.
	work := "go 1.25.0\n\nuse (\n\t./backend\n\t.\\tools\\gen\n)\n\nuse ./cmd\n"
	if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadPatterns(dir)
	want := []string{"./backend/...", "./cmd/...", "./tools/gen/..."} // sorted
	if len(got) != len(want) {
		t.Fatalf("loadPatterns = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pattern[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// No go.work at all is the common case and must stay "./...".
func TestLoadPatterns_WithoutAWorkspace(t *testing.T) {
	t.Parallel()
	got := loadPatterns(t.TempDir())
	if len(got) != 1 || got[0] != "./..." {
		t.Errorf("loadPatterns = %q, want [./...]", got)
	}
}
