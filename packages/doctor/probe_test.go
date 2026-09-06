package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// The guard the CLI leans on. os.Stat answers "is there a file here",
// which is not the question: store.Open migrates whatever it is handed,
// so anything sitting at the state path became an atlas store the moment
// someone asked whether it was one.
func TestIsAtlasStore_AcceptsAMigratedStore(t *testing.T) {
	f := newFixture(t)

	if err := IsAtlasStore(context.Background(), f.dbPath); err != nil {
		t.Fatalf("a store atlas just migrated was rejected: %v", err)
	}
}

func TestIsAtlasStore_RejectsWhatIsNotAnAtlasStore(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		bytes []byte
	}{
		// The placeholder an interrupted `atlas init` (or a `touch`)
		// leaves behind. SQLite reads a zero-length file as a valid empty
		// database, so nothing below the schema level rejects it.
		{"zero byte placeholder", nil},
		// Any other file that happens to be at the configured path.
		{"not a database at all", []byte("hello")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".db")
			if err := os.WriteFile(path, tc.bytes, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}

			if err := IsAtlasStore(context.Background(), path); err == nil {
				t.Fatal("accepted a file that is not an atlas store")
			}

			after, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat after: %v", err)
			}
			if after.Size() != before.Size() {
				t.Errorf("the check wrote to the file it was inspecting: %d -> %d bytes",
					before.Size(), after.Size())
			}
		})
	}
}

func TestIsAtlasStore_RejectsAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")

	if err := IsAtlasStore(context.Background(), missing); err == nil {
		t.Fatal("accepted a path with no file at it")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the check created the database it was asked to inspect")
	}
}

// A SQLite file with no migration bookkeeping is not an atlas store, and
// the schema check has to be able to say so from the probe alone --
// version 0, which it renders as a fail, rather than a raw "no such
// table" error surfacing as "the check could not complete".
func TestMigrationState_MissingTableReadsAsVersionZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := (&Env{DBPath: path, Root: dir}).withDefaults()
	if err := env.openProbe(); err != nil {
		t.Fatalf("openProbe on an empty sqlite file: %v", err)
	}
	defer env.closeProbe()

	version, dirty, err := env.probe.migrationState(context.Background())
	if err != nil {
		t.Fatalf("migrationState: %v", err)
	}
	if version != 0 || dirty {
		t.Errorf("migrationState = (%d, %v), want (0, false)", version, dirty)
	}
}

// TestOpenReadOnly_OpensTheFileItWasGiven pins that doctor's read-only probe
// reads the database at the path it was handed.
//
// The DSN is a `file:` URI, so SQLite parses it: an unescaped '?' starts a
// query string and an unescaped '#' starts a fragment, and both are DISCARDED
// from the filename. A probe built by plain concatenation therefore opens a
// TRUNCATED path -- for ".../with#hash/atlas.db" it asks for ".../with",
// which under mode=ro is either a different store or no store at all. Neither
// outcome looks like a path bug from the inside: doctor reports on a database
// it was never asked about, or fails a healthy one.
//
// store.Open already escapes; this asserts the probe agrees with it, because
// a diagnostic that disagrees with the thing it diagnoses is worse than none.
func TestOpenReadOnly_OpensTheFileItWasGiven(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	// Paths that differ only in what a URI parser would throw away.
	names := []string{"plain", "with#hash", "with#hash-and-more", "with?query", "with%25pct"}
	paths := make([]string, len(names))
	for i, name := range names {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		paths[i] = filepath.Join(dir, "atlas.db")
		s, err := store.Open(ctx, paths[i])
		if err != nil {
			t.Fatalf("store.Open under %q: %v", name, err)
		}
		if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(fmt.Sprintf("pkg.Sym%d", i)),
			Kind:          shared.KindFunc,
			FilePath:      "pkg/f.go",
			Line:          1,
		}); err != nil {
			t.Fatalf("Insert under %q: %v", name, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close under %q: %v", name, err)
		}
	}

	for i, name := range names {
		db, err := openReadOnly(paths[i])
		if err != nil {
			t.Fatalf("openReadOnly under %q: %v", name, err)
		}
		var got string
		err = db.QueryRowContext(ctx, `SELECT qualified_name FROM symbols`).Scan(&got)
		_ = db.Close()
		if err != nil {
			t.Fatalf("read symbols under %q: %v", name, err)
		}
		if want := fmt.Sprintf("pkg.Sym%d", i); got != want {
			t.Fatalf("probe of %s read %q, want %q -- it opened a different file",
				paths[i], got, want)
		}
	}
}
