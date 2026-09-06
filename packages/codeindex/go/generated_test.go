package goscan

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// generatedFixture holds one file per exclusion rule plus two hand-written
// files that must survive all of them — see testdata/generatedproject.
const generatedFixture = "testdata/generatedproject"

func TestScan_GeneratedExclusions(t *testing.T) {
	t.Parallel()

	handWritten := []shared.SymbolID{"app.Handwritten", "app.MentionsGenerated"}

	tests := []struct {
		name        string
		opts        Options
		wantPresent []shared.SymbolID
		wantAbsent  []shared.SymbolID
		wantSkipped []SkippedFile
	}{
		{
			name: "default rules catch the header and the generated dir",
			opts: Options{},
			wantPresent: append(handWritten,
				"api.MarshalSchema", "gen.WireBuild", "docs.DocSample"),
			wantAbsent: []shared.SymbolID{
				"db.GetUserByID", "generated.LegacyGenerated", "generated.QueryRow",
			},
			wantSkipped: []SkippedFile{
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
				// Both the directory and the header rule match this one; the
				// header wins because it is the signal that survives a move.
				{Path: "generated/with_header.go", Reason: SkipGeneratedHeader},
			},
		},
		{
			name: "globs catch the filename and directory conventions",
			opts: Options{GeneratedGlobs: []string{"*.pb.go", "gen/"}},
			wantPresent: append(handWritten,
				"docs.DocSample"),
			wantAbsent: []shared.SymbolID{
				"api.MarshalSchema", "db.GetUserByID",
				"gen.WireBuild", "generated.LegacyGenerated", "generated.QueryRow",
			},
			wantSkipped: []SkippedFile{
				{Path: "api/schema.pb.go", Reason: SkipGeneratedGlob},
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "gen/wire.go", Reason: SkipGeneratedGlob},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
				{Path: "generated/with_header.go", Reason: SkipGeneratedHeader},
			},
		},
		{
			name: "ignored packages are reported rather than silently dropped",
			opts: Options{IgnorePackages: []string{"docs"}},
			wantPresent: append(handWritten,
				"api.MarshalSchema", "gen.WireBuild"),
			wantAbsent: []shared.SymbolID{
				"db.GetUserByID", "docs.DocSample",
				"generated.LegacyGenerated", "generated.QueryRow",
			},
			wantSkipped: []SkippedFile{
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "docs/samples.go", Reason: SkipIgnoredPackage},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
				{Path: "generated/with_header.go", Reason: SkipGeneratedHeader},
			},
		},
		{
			name: "IncludeGenerated indexes every generated file",
			opts: Options{
				IncludeGenerated: true,
				GeneratedGlobs:   []string{"*.pb.go", "gen/"},
			},
			wantPresent: append(handWritten,
				"api.MarshalSchema", "db.GetUserByID", "gen.WireBuild",
				"generated.LegacyGenerated", "generated.QueryRow", "docs.DocSample"),
			wantSkipped: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := Scan(context.Background(), generatedFixture, tc.opts)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			for _, id := range tc.wantPresent {
				if _, ok := res.Graph.Nodes[id]; !ok {
					t.Errorf("symbol %s missing; nodes: %v", id, nodeIDs(res))
				}
			}
			for _, id := range tc.wantAbsent {
				if _, ok := res.Graph.Nodes[id]; ok {
					t.Errorf("symbol %s indexed but should have been excluded", id)
				}
			}
			if !reflect.DeepEqual(res.SkippedFiles, tc.wantSkipped) {
				t.Errorf("SkippedFiles = %+v; want %+v", res.SkippedFiles, tc.wantSkipped)
			}
		})
	}
}

// The skip list feeds coverage denominators, so two scans of the same tree
// must produce byte-identical records — no map-iteration order, no mtimes.
func TestScan_SkippedFilesAreDeterministic(t *testing.T) {
	t.Parallel()

	opts := Options{
		GeneratedGlobs: []string{"*.pb.go", "gen/"},
		IgnorePackages: []string{"docs"},
	}
	first, err := Scan(context.Background(), generatedFixture, opts)
	if err != nil {
		t.Fatalf("Scan (first): %v", err)
	}
	second, err := Scan(context.Background(), generatedFixture, opts)
	if err != nil {
		t.Fatalf("Scan (second): %v", err)
	}
	if !reflect.DeepEqual(first.SkippedFiles, second.SkippedFiles) {
		t.Fatalf("skip list differs between runs:\n%+v\n%+v",
			first.SkippedFiles, second.SkippedFiles)
	}
	if len(first.SkippedFiles) != 6 {
		t.Fatalf("expected 6 skipped files, got %d: %+v",
			len(first.SkippedFiles), first.SkippedFiles)
	}
}

func TestScan_MalformedGeneratedGlobWarns(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), generatedFixture, Options{
		GeneratedGlobs: []string{"[unclosed"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var found bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "[unclosed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the bad glob; warnings: %v", res.Warnings)
	}
}

func TestMatchGeneratedGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		relPath string
		want    bool
	}{
		{"bare filename glob matches at any depth", "*.pb.go", "internal/api/schema.pb.go", true},
		{"bare filename glob matches at the root", "*_gen.go", "wire_gen.go", true},
		{"bare filename glob does not match another suffix", "*.pb.go", "internal/api/schema.go", false},
		{"directory pattern matches a rooted prefix", "gen/", "gen/wire.go", true},
		{"directory pattern matches a nested occurrence", "gen/", "internal/gen/wire.go", true},
		{"directory pattern does not match a name prefix", "gen/", "general/wire.go", false},
		{"anchored path glob matches its own segment", "internal/db/*.go", "internal/db/query.go", true},
		{"anchored path glob does not cross a separator", "internal/db/*.go", "internal/db/pg/query.go", false},
		{"doublestar prefix is stripped and matched anywhere", "**/*.sql.go", "a/b/c/users.sql.go", true},
		{"empty pattern never matches", "", "anything.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := matchGeneratedGlob(tc.pattern, tc.relPath); got != tc.want {
				t.Fatalf("matchGeneratedGlob(%q, %q) = %v; want %v",
					tc.pattern, tc.relPath, got, tc.want)
			}
		})
	}
}

// The reason reported for a skipped file is the product, not a byproduct:
// `atlas doctor` uses it to explain a coverage denominator. When several
// rules match one file, the STRONGEST signal must win — the header holds
// wherever the tool wrote its output, the directory only says where someone
// filed it.
func TestGeneratedReason_StrongestSignalWins(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), "testdata/generatedproject", Options{
		GeneratedGlobs: []string{"*.pb.go"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := map[string]SkipReason{}
	for _, sf := range res.SkippedFiles {
		got[sf.Path] = sf.Reason
	}

	// with_header.go sits in generated/ AND carries the header. Both rules
	// match; the header is the one that would still be true if the file moved.
	if r := got["generated/with_header.go"]; r != SkipGeneratedHeader {
		t.Errorf("generated/with_header.go reason = %q, want %q — the directory rule won over a real header",
			r, SkipGeneratedHeader)
	}
	// legacy.go carries no header, so the directory is the only signal there
	// is, and reporting it is correct rather than a fallback.
	if r := got["generated/legacy.go"]; r != SkipGeneratedDir {
		t.Errorf("generated/legacy.go reason = %q, want %q", r, SkipGeneratedDir)
	}
	// schema.pb.go matches the glob and has no header: glob is then the
	// strongest signal available.
	if r := got["api/schema.pb.go"]; r != SkipGeneratedGlob && r != SkipGeneratedHeader {
		t.Errorf("api/schema.pb.go reason = %q, want glob or header", r)
	}
}
