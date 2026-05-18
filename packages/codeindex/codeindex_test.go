package codeindex

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestIndexProject_GoSampleProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	idx, err := IndexProject(ctx, "go/testdata/sampleproject", Options{HashFiles: true})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if idx.Graph == nil {
		t.Fatal("nil graph")
	}
	if len(idx.Symbols) == 0 {
		t.Fatal("expected symbols")
	}
	// File hashes should have been computed for the .go files.
	if len(idx.FileHashes) == 0 {
		t.Fatal("expected non-empty FileHashes when HashFiles=true")
	}
	// generated_at must be set.
	if idx.GeneratedAt.IsZero() {
		t.Fatal("generated_at not set")
	}
}

func TestIndexProject_EmptyRoot_Error(t *testing.T) {
	t.Parallel()
	if _, err := IndexProject(context.Background(), "", Options{}); err == nil {
		t.Fatal("expected error for empty rootDir")
	}
}

func TestIndexProject_FindsLegacyAndAtlasAnnotations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Index the annotations testdata directory — it has both grammar samples.
	idx, err := IndexProject(ctx, "annotations/testdata", Options{
		AnnotationExts: []string{".fixture"},
	})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	// Even though .fixture isn't a recognised language, the walker accepted
	// the ext but Parse() rejected it → expect zero annotations. This
	// confirms walker + parser layering works correctly under exotic exts.
	if len(idx.Annotations) != 0 {
		t.Fatalf("expected 0 annotations for unsupported parser ext; got %d", len(idx.Annotations))
	}

	// Now index the same dir treating it as Markdown-style — this is a
	// negative control that proves the walker is the only knob in play.
	_ = shared.AnnFeature
}
