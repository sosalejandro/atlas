package onboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

func TestSaveLoad_RoundTripsAndStaysNamespaced(t *testing.T) {
	root := t.TempDir()
	res := Infer(Input{
		Root:    root,
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
	})

	path, err := Save(root, res, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The provisional map must not land anywhere the rest of atlas reads as
	// declared state. Its own directory is the whole guarantee.
	want := filepath.Join(root, ".atlas", "provisional", "capabilities.json")
	if path != want {
		t.Errorf("Save wrote %s, want %s", path, want)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if generic["provisional"] != true {
		t.Error("the document does not declare itself provisional")
	}
	if s, _ := generic["note"].(string); !strings.Contains(strings.ToLower(s), "not") {
		t.Errorf("the document carries no note saying what it is not: %q", s)
	}

	doc, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(doc.Capabilities) != len(res.Capabilities) {
		t.Errorf("round trip lost capabilities: %d -> %d", len(res.Capabilities), len(doc.Capabilities))
	}
	if doc.Capabilities[0].Ref() != ProvisionalPrefix+doc.Capabilities[0].ID {
		t.Error("a loaded capability lost its namespace")
	}
}

// A file that does not say it is provisional is not one this package will
// hand back. Anything that reaches promote must have come from a document
// that labelled itself, or the labelling is decoration.
func TestLoad_RejectsUnlabelledDocument(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".atlas", "provisional")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"provisional":false,"provisional_capabilities":[{"id":"a.b"}]}`
	if err := os.WriteFile(filepath.Join(dir, "capabilities.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted a document that does not declare itself provisional")
	}
}

func TestLoad_MissingIsADistinctError(t *testing.T) {
	if _, err := Load(t.TempDir()); !IsNotGenerated(err) {
		t.Fatalf("Load on a repo with no map returned %v, want a not-generated error", err)
	}
}

func TestDocument_Find(t *testing.T) {
	doc := Document{Capabilities: []Capability{
		{ID: "orders.place", Provisional: true},
		{ID: "billing.settle", Provisional: true},
	}}
	if _, ok := doc.Find("orders.place"); !ok {
		t.Error("Find missed a bare id")
	}
	// Users will paste back the namespaced form they were shown.
	if _, ok := doc.Find(ProvisionalPrefix + "billing.settle"); !ok {
		t.Error("Find rejected the namespaced form it printed")
	}
	if _, ok := doc.Find("nope.nope"); ok {
		t.Error("Find invented a capability")
	}
}
