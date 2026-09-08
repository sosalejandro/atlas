package envelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var errNotAnObject = errors.New("envelope: stable form is not an object")

// VolatileKeys are dropped by --stable wherever they appear in the envelope.
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
var VolatileKeys = map[string]bool{
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

// IsVolatileKey reports whether a JSON key's value depends on when or where
// the command ran rather than on the code it examined.
func IsVolatileKey(k string) bool {
	if VolatileKeys[k] {
		return true
	}
	for _, s := range volatileSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// StripVolatile returns v with every volatile key removed, recursively.
//
// It round-trips through JSON rather than reflecting over the caller's types
// on purpose: the envelope carries `any` from thirty different commands, and
// a reflective walk would need to know all of them. The cost is paid only
// under --stable.
func StripVolatile(v any) (any, error) {
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
			if IsVolatileKey(k) {
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

// VolatileKeysIn reports every volatile key present in v, sorted. It exists
// for the test that proves this list still covers what the commands emit --
// a policy nothing checks is a policy that drifts.
func VolatileKeysIn(v any) []string {
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
			if IsVolatileKey(k) {
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
