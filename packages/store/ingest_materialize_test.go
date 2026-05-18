package store

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// materializeIndex builds a minimal *codeindex.Index that places a single
// symbol at file:line with one feature/contract annotation immediately above
// it. The materialize step in Ingest must find the symbol via the
// "nearest symbol at or after the annotation line" rule and emit one
// feature row + one feature_symbols link per id carried by the annotation.
func materializeIndex(t *testing.T, ann shared.Annotation, sym shared.Symbol) *codeindex.Index {
	t.Helper()
	g := graph.New()
	g.AddNode(&graph.Node{Symbol: sym})

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return &codeindex.Index{
		Root:        "/tmp/project",
		GeneratedAt: now,
		Graph:       g,
		Symbols:     []shared.Symbol{sym},
		Annotations: []shared.Annotation{ann},
		FileHashes: map[string]codeindex.FileHash{
			sym.Position.Path: {Path: sym.Position.Path, SHA256: "h-" + sym.Position.Path, ModTime: now, LastScanned: now},
		},
	}
}

func TestIngest_MaterializesFeaturesFromSingleIdAnnotation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sym := shared.Symbol{
		ID:       "pkg.LoginHandler",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/handler.go", Line: 10},
		Package:  "github.com/example/pkg",
	}
	ann := shared.Annotation{
		Kind:     shared.AnnFeature,
		IDs:      []string{"auth.login"},
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: "src/handler.go", Line: 9},
		Raw:      "auth.login",
	}

	stats, err := s.Ingest(ctx, materializeIndex(t, ann, sym))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.FeaturesMaterialized != 1 {
		t.Errorf("FeaturesMaterialized = %d, want 1", stats.FeaturesMaterialized)
	}
	if stats.FeatureSymbolsLinked != 1 {
		t.Errorf("FeatureSymbolsLinked = %d, want 1", stats.FeatureSymbolsLinked)
	}

	feat, err := s.Features().Get(ctx, "auth.login")
	if err != nil {
		t.Fatalf("Features.Get(auth.login): %v", err)
	}
	if feat.Title != "auth.login" {
		t.Errorf("Title = %q, want %q (id-as-title default)", feat.Title, "auth.login")
	}
	if feat.Kind != FeatureKindFeature {
		t.Errorf("Kind = %q, want %q", feat.Kind, FeatureKindFeature)
	}

	links, err := s.FeatureSymbols().ListByFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("FeatureSymbols.ListByFeature: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("len(links) = %d, want 1", len(links))
	}
	if links[0].Role != RoleImpl {
		t.Errorf("Role = %q, want %q", links[0].Role, RoleImpl)
	}
	if links[0].Source != SourceAnnotation {
		t.Errorf("Source = %q, want %q", links[0].Source, SourceAnnotation)
	}
	row, err := s.Symbols().FindByQualifiedName(ctx, "pkg.LoginHandler")
	if err != nil {
		t.Fatalf("FindByQualifiedName: %v", err)
	}
	if links[0].SymbolID != row.ID {
		t.Errorf("SymbolID = %d, want %d (handler row)", links[0].SymbolID, row.ID)
	}
}

func TestIngest_MaterializesFeaturesFromMultiIdAnnotation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sym := shared.Symbol{
		ID:       "pkg.MealsHandler",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/meals.go", Line: 15},
		Package:  "github.com/example/pkg",
	}
	// `// @testreg meals.log-create meals.history #mocked`
	ann := shared.Annotation{
		Kind:     shared.AnnFeature,
		IDs:      []string{"meals.log-create", "meals.history"},
		Tags:     []string{"mocked"},
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: "src/meals.go", Line: 14},
		Raw:      "meals.log-create meals.history #mocked",
	}

	stats, err := s.Ingest(ctx, materializeIndex(t, ann, sym))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.FeaturesMaterialized != 2 {
		t.Errorf("FeaturesMaterialized = %d, want 2", stats.FeaturesMaterialized)
	}
	if stats.FeatureSymbolsLinked != 2 {
		t.Errorf("FeatureSymbolsLinked = %d, want 2", stats.FeatureSymbolsLinked)
	}

	for _, id := range []shared.FeatureID{"meals.log-create", "meals.history"} {
		if _, err := s.Features().Get(ctx, id); err != nil {
			t.Errorf("Features.Get(%q): %v", id, err)
		}
		links, err := s.FeatureSymbols().ListByFeature(ctx, id)
		if err != nil {
			t.Fatalf("FeatureSymbols.ListByFeature(%q): %v", id, err)
		}
		if len(links) != 1 {
			t.Errorf("links for %q = %d, want 1", id, len(links))
		}
	}

	// `#mocked` tag must NOT become a feature row.
	if _, err := s.Features().Get(ctx, "#mocked"); err == nil {
		t.Error("#mocked unexpectedly became a feature row")
	}
	if _, err := s.Features().Get(ctx, "mocked"); err == nil {
		t.Error("`mocked` (tag) unexpectedly became a feature row")
	}
}

func TestIngest_StripsHashTagSuffixes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sym := shared.Symbol{
		ID:       "pkg.AuthHandler",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/auth.go", Line: 12},
		Package:  "github.com/example/pkg",
	}
	ann := shared.Annotation{
		Kind:     shared.AnnFeature,
		IDs:      []string{"auth.login"},
		Tags:     []string{"real"},
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: "src/auth.go", Line: 11},
		Raw:      "auth.login #real",
	}

	if _, err := s.Ingest(ctx, materializeIndex(t, ann, sym)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	feats, err := s.Features().List(ctx, FeatureFilter{})
	if err != nil {
		t.Fatalf("Features.List: %v", err)
	}
	if len(feats) != 1 || feats[0].ID != "auth.login" {
		t.Fatalf("Features.List = %+v, want 1 row with id auth.login", feats)
	}
	if _, err := s.Features().Get(ctx, "#real"); err == nil {
		t.Error("#real tag was materialized as a feature row")
	}
}

func TestIngest_HonoursDashedIds(t *testing.T) {
	// PR #18 enabled dashed segments in strict-kind ids. Regression guard:
	// ingest must accept and materialize them verbatim.
	s := openTestStore(t)
	ctx := context.Background()

	sym := shared.Symbol{
		ID:       "pkg.ExportPDF",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/export.go", Line: 20},
		Package:  "github.com/example/pkg",
	}
	ann := shared.Annotation{
		Kind:     shared.AnnFeature,
		IDs:      []string{"plans-patient.export-pdf"},
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: "src/export.go", Line: 19},
		Raw:      "plans-patient.export-pdf",
	}

	if _, err := s.Ingest(ctx, materializeIndex(t, ann, sym)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	feat, err := s.Features().Get(ctx, "plans-patient.export-pdf")
	if err != nil {
		t.Fatalf("Features.Get(plans-patient.export-pdf): %v", err)
	}
	if feat.ID != "plans-patient.export-pdf" {
		t.Errorf("Feature.ID = %q, want %q", feat.ID, "plans-patient.export-pdf")
	}
}

// TestIngest_OrphanAnnotationOnNonCodeFile asserts that a feature annotation
// in a file with NO symbol declared at or after the annotation line (typical
// of a markdown file, a README, or a comment-only block) is gracefully
// skipped: no feature row, no link row, and the orphan counter increments.
// The annotation row itself is still written (UpsertAnnotation in step 4).
func TestIngest_OrphanAnnotationOnNonCodeFile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// No symbols supplied; just one annotation in a .md file.
	g := graph.New()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	idx := &codeindex.Index{
		Root:        "/tmp/project",
		GeneratedAt: now,
		Graph:       g,
		Annotations: []shared.Annotation{
			{
				Kind:     shared.AnnFeature,
				IDs:      []string{"docs.intro"},
				Source:   shared.SourceTestreg,
				Position: shared.FilePosition{Path: "docs/intro.md", Line: 1},
				Raw:      "docs.intro",
			},
		},
		FileHashes: map[string]codeindex.FileHash{
			"docs/intro.md": {Path: "docs/intro.md", SHA256: "h-md", ModTime: now, LastScanned: now},
		},
	}

	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.FeaturesMaterialized != 0 {
		t.Errorf("FeaturesMaterialized = %d, want 0 (orphan should not create rows)", stats.FeaturesMaterialized)
	}
	if stats.FeatureSymbolsLinked != 0 {
		t.Errorf("FeatureSymbolsLinked = %d, want 0", stats.FeatureSymbolsLinked)
	}
	if stats.OrphanAnnotationsSkipped != 1 {
		t.Errorf("OrphanAnnotationsSkipped = %d, want 1", stats.OrphanAnnotationsSkipped)
	}

	if _, err := s.Features().Get(ctx, "docs.intro"); err == nil {
		t.Error("orphan annotation produced a feature row")
	}
}

// TestIngest_DoesNotOverwriteExistingTitle proves the INSERT OR IGNORE
// contract: when a feature row pre-exists with rich metadata (Title, Owner)
// because a prior atlas migrate or a test harness Upsert'd it, the ingest
// pass MUST NOT clobber that metadata back to the id-as-title default.
func TestIngest_DoesNotOverwriteExistingTitle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	owner := "team-auth"
	if err := s.Features().Upsert(ctx, Feature{
		ID:    "auth.login",
		Title: "User Login",
		Owner: &owner,
		Kind:  FeatureKindFeature,
	}); err != nil {
		t.Fatalf("pre-seed Upsert: %v", err)
	}

	sym := shared.Symbol{
		ID:       "pkg.LoginHandler",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/handler.go", Line: 10},
		Package:  "github.com/example/pkg",
	}
	ann := shared.Annotation{
		Kind:     shared.AnnFeature,
		IDs:      []string{"auth.login"},
		Source:   shared.SourceTestreg,
		Position: shared.FilePosition{Path: "src/handler.go", Line: 9},
		Raw:      "auth.login",
	}

	if _, err := s.Ingest(ctx, materializeIndex(t, ann, sym)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	feat, err := s.Features().Get(ctx, "auth.login")
	if err != nil {
		t.Fatalf("Features.Get: %v", err)
	}
	if feat.Title != "User Login" {
		t.Errorf("Title = %q, want %q (pre-seeded title must survive)", feat.Title, "User Login")
	}
	if feat.Owner == nil || *feat.Owner != "team-auth" {
		t.Errorf("Owner = %v, want pointer to %q", feat.Owner, "team-auth")
	}

	// The link row should still be created — Ensure is for the feature row
	// only; LinkFeatureSymbol always runs.
	links, err := s.FeatureSymbols().ListByFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ListByFeature: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("links = %d, want 1", len(links))
	}
}

// TestIngest_ContractKindBecomesFeatureKindContract covers the contract
// branch: `@atlas:contract <id>` annotations materialize features with
// kind="contract" and link rows with role="contract".
func TestIngest_ContractKindBecomesFeatureKindContract(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sym := shared.Symbol{
		ID:       "pkg.LoginAPI",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/api/login.go", Line: 8},
		Package:  "github.com/example/pkg/api",
	}
	ann := shared.Annotation{
		Kind:     shared.AnnContract,
		IDs:      []string{"auth.api.login"},
		Source:   shared.SourceAtlas,
		Position: shared.FilePosition{Path: "src/api/login.go", Line: 7},
		Raw:      "auth.api.login",
	}

	if _, err := s.Ingest(ctx, materializeIndex(t, ann, sym)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	feat, err := s.Features().Get(ctx, "auth.api.login")
	if err != nil {
		t.Fatalf("Features.Get: %v", err)
	}
	if feat.Kind != FeatureKindContract {
		t.Errorf("Kind = %q, want %q", feat.Kind, FeatureKindContract)
	}

	links, err := s.FeatureSymbols().ListByFeature(ctx, "auth.api.login")
	if err != nil {
		t.Fatalf("ListByFeature: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("len(links) = %d, want 1", len(links))
	}
	if links[0].Role != RoleContract {
		t.Errorf("Role = %q, want %q", links[0].Role, RoleContract)
	}
}
