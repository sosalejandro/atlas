package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Comparable output, and why atlas did not have it.
//
// #111's premise is that a diagram is a checked-in artifact whose drift fails
// CI. #161's premise is that a report can carry a digest binding it to the
// index it came from. Both require that running the same command twice over
// the same code produces the same bytes, and until this file existed, nothing
// atlas emitted did.
//
// The reason is one line in emitJSON: every envelope carries `generated_at`.
// At RFC3339's second granularity a quick loop of three runs lands inside one
// second and looks stable, which is exactly how this went unnoticed -- the
// first measurement taken for #162 concluded the output WAS stable, because
// the runs finished in the same second.
//
// A digest computed over a payload containing a wall-clock stamp certifies
// nothing. A diagram regenerated in CI diffs against itself. So --stable
// omits the fields whose value depends on WHEN or WHERE the command ran
// rather than on what the code says.
//
// This is not "less information". It is the same answer with the parts that
// are not about the code removed, which is the only form in which two answers
// can be compared.

// volatileKeys are dropped by --stable wherever they appear in the envelope.
//
// Two classes, both disqualifying for comparison:
//
//   - TEMPORAL. When the command ran. `generated_at` (every envelope),
//     `sampled_at` (audit scores), and every duration.
//   - ENVIRONMENTAL. Where it ran. Absolute paths differ between a laptop
//     and a CI runner for the same commit, and a checked-in artifact
//     containing one is a diff on every machine.
//
// Held as an explicit list rather than inferred, so that adding a volatile
// field is a decision somebody makes rather than a suffix that happens to
// match. TestStable_EveryVolatileFieldIsDeclared fails when a command emits
// a field this list does not cover.
var volatileKeys = map[string]bool{
	"generated_at": true, // the envelope's own stamp
	"sampled_at":   true, // audit score sample time
	"parsed_at":    true, // annotation parse time
	"last_scanned": true, // file-hash scan time
	"db_path":      true, // absolute, machine-specific
	"root":         true, // absolute, machine-specific
	"project_root": true, // absolute; SCIP records the INDEXER's machine
	"input":        true, // an operand path, absolute when the user gave one
}

// volatileSuffixes catch the measurement fields, which are added often enough
// that enumerating them would go stale. `_ms` and `_ns` are durations;
// `duration` covers the spelled-out form.
var volatileSuffixes = []string{"_ms", "_ns", "_duration", "duration_ms"}

// isVolatileKey reports whether a JSON key's value depends on when or where
// the command ran rather than on the code it examined.
func isVolatileKey(k string) bool {
	if volatileKeys[k] {
		return true
	}
	for _, s := range volatileSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// stripVolatile returns v with every volatile key removed, recursively.
//
// It round-trips through JSON rather than reflecting over the caller's types
// on purpose: the envelope carries `any` from thirty different commands, and
// a reflective walk would need to know all of them. The cost is paid only
// under --stable.
func stripVolatile(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("stable: re-encode: %w", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("stable: re-decode: %w", err)
	}
	return prune(tree), nil
}

func prune(node any) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			if isVolatileKey(k) {
				continue
			}
			out[k] = prune(v)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, v := range n {
			out[i] = prune(v)
		}
		return out
	default:
		return node
	}
}

// volatileKeysIn reports every volatile key present in v, sorted. It exists
// for the test that proves this list still covers what the commands emit --
// a policy nothing checks is a policy that drifts.
func volatileKeysIn(v any) []string {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil
	}
	seen := map[string]bool{}
	collectVolatile(tree, seen)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func collectVolatile(node any, seen map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if isVolatileKey(k) {
				seen[k] = true
			}
			collectVolatile(v, seen)
		}
	case []any:
		for _, v := range n {
			collectVolatile(v, seen)
		}
	}
}
