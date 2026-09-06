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
			wantAbsent: []shared.SymbolID{"db.GetUserByID", "generated.LegacyGenerated"},
			wantSkipped: []SkippedFile{
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
			},
		},
		{
			name: "globs catch the filename and directory conventions",
			opts: Options{GeneratedGlobs: []string{"*.pb.go", "gen/"}},
			wantPresent: append(handWritten,
				"docs.DocSample"),
			wantAbsent: []shared.SymbolID{
				"api.MarshalSchema", "db.GetUserByID",
				"gen.WireBuild", "generated.LegacyGenerated",
			},
			wantSkipped: []SkippedFile{
				{Path: "api/schema.pb.go", Reason: SkipGeneratedGlob},
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "gen/wire.go", Reason: SkipGeneratedGlob},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
			},
		},
		{
			name: "ignored packages are reported rather than silently dropped",
			opts: Options{IgnorePackages: []string{"docs"}},
			wantPresent: append(handWritten,
				"api.MarshalSchema", "gen.WireBuild"),
			wantAbsent: []shared.SymbolID{
				"db.GetUserByID", "docs.DocSample", "generated.LegacyGenerated",
			},
			wantSkipped: []SkippedFile{
				{Path: "db/queries.sql.go", Reason: SkipGeneratedHeader},
				{Path: "docs/samples.go", Reason: SkipIgnoredPackage},
				{Path: "generated/legacy.go", Reason: SkipGeneratedDir},
			},
		},
		{
			name: "IncludeGenerated indexes every generated file",
			opts: Options{
				IncludeGenerated: true,
				GeneratedGlobs:   []string{"*.pb.go", "gen/"},
			},
			wantPresent: append(handWritten,
				"api.MarshalSchema", "db.GetUserByID",
				"gen.WireBuild", "generated.LegacyGenerated", "docs.DocSample"),
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
	if len(first.SkippedFiles) != 5 {
		t.Fatalf("expected 5 skipped files, got %d: %+v",
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
