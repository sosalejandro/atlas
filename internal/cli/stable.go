package cli

import "github.com/sosalejandro/atlas/packages/envelope"

// The volatility policy lives in packages/envelope, because the HTTP API
// (#101) needs the same answer to "which fields are not about the code" and
// two copies of that judgement would drift the way the testreg dashboard
// drifted from the CLI. These are the CLI's names for it.
//
// Why any of this exists, and how the bug hid: see
// docs/determinism-and-comparison.md.

func isVolatileKey(k string) bool { return envelope.IsVolatileKey(k) }

func stripVolatile(v any) (any, error) { return envelope.StripVolatile(v) }

func volatileKeysIn(v any) []string { return envelope.VolatileKeysIn(v) }
