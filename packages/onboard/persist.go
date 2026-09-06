package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The provisional map lives in its own directory under .atlas/, beside the
// state DB but not inside it.
//
// Keeping it out of SQLite is the point. Every table in the store is read by
// some other verb as fact; a proposals table would eventually be joined
// against by something that forgot the distinction, and the distinction is
// the product. A JSON file in a directory named "provisional" cannot be
// joined against by accident, is reviewable in a diff, and is deleted by
// `rm -r` when the user decides the proposals were wrong.
const (
	provisionalDir  = "provisional"
	provisionalFile = "capabilities.json"
	atlasDir        = ".atlas"
)

// documentNote is written into every saved map. It is aimed at the person
// who finds this file six months from now with no idea what wrote it.
const documentNote = "These capabilities were INFERRED by `atlas onboard`. They are NOT " +
	"declarations and atlas does not treat them as any part of its registry. " +
	"Promote one with `atlas onboard promote --id <id> --apply`, which writes an " +
	"@atlas:feature annotation into your source; everything else ignores this file."

// ErrNotGenerated is returned by Load when no provisional map exists yet.
// It is a distinct error because "run onboard first" and "your map is
// corrupt" call for opposite responses from the caller.
var ErrNotGenerated = errors.New("onboard: no provisional capability map has been generated")

// IsNotGenerated reports whether err means the map has not been generated.
func IsNotGenerated(err error) bool { return errors.Is(err, ErrNotGenerated) }

// Document is the on-disk provisional map.
//
// Provisional is the first field and is checked on load. A document that
// does not label itself is rejected rather than read, so the label cannot
// decay into a field nobody enforces.
type Document struct {
	Provisional  bool         `json:"provisional"`
	Note         string       `json:"note"`
	GeneratedAt  time.Time    `json:"generated_at"`
	Root         string       `json:"root"`
	Stats        Stats        `json:"stats"`
	Capabilities []Capability `json:"provisional_capabilities"`
	Findings     []Finding    `json:"findings,omitempty"`
	Limits       []Limit      `json:"limits,omitempty"`
}

// Find looks a capability up by id, accepting either the bare id or the
// namespaced form the CLI prints. Users paste back what they were shown, and
// rejecting the printed form would be a puzzle with no purpose.
func (d Document) Find(id string) (Capability, bool) {
	id = strings.TrimPrefix(strings.TrimSpace(id), ProvisionalPrefix)
	for _, c := range d.Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return Capability{}, false
}

// Path returns where the provisional map lives for a repository root.
func Path(root string) string {
	return filepath.Join(root, atlasDir, provisionalDir, provisionalFile)
}

// Save writes the provisional map, creating its directory if needed, and
// returns the path written.
func Save(root string, res Result, now time.Time) (string, error) {
	doc := Document{
		Provisional:  true,
		Note:         documentNote,
		GeneratedAt:  now.UTC(),
		Root:         res.Root,
		Stats:        res.Stats,
		Capabilities: res.Capabilities,
		Findings:     res.Findings,
		Limits:       res.Limits,
	}
	path := Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("onboard: create %s: %w", filepath.Dir(path), err)
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("onboard: encode provisional map: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("onboard: write %s: %w", path, err)
	}
	return path, nil
}

// Load reads the provisional map back.
func Load(root string) (Document, error) {
	path := Path(root)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Document{}, fmt.Errorf("%w (looked in %s)", ErrNotGenerated, path)
	}
	if err != nil {
		return Document{}, fmt.Errorf("onboard: read %s: %w", path, err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("onboard: parse %s: %w", path, err)
	}
	if !doc.Provisional {
		return Document{}, fmt.Errorf(
			"onboard: %s does not declare itself provisional; refusing to treat it as a proposal set", path)
	}
	// Re-assert the per-record flag on the way out. A hand-edited file could
	// have dropped it, and a Capability that reaches the renderer with
	// Provisional=false would print as though somebody had declared it.
	for i := range doc.Capabilities {
		doc.Capabilities[i].Provisional = true
	}
	return doc, nil
}
