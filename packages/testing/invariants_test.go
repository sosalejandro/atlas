package atlastest

import "testing"

// The invariants are the property layer's assertions, so a wrong one is worse
// than a missing one: it either passes on a broken input (a gate with nothing
// behind it) or fails on a correct one, at which point somebody deletes the
// property and the bug class it named goes unguarded. These are the cases
// where CheckSpansWellNested has to get the boundary right.
func TestCheckSpansWellNested(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		spans     []SymbolSpan
		wantStrad bool
	}{
		{
			name: "disjoint",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 20},
				{ID: 2, File: "a.go", Start: 21, End: 30},
			},
		},
		{
			name: "strict nesting",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 40},
				{ID: 2, File: "a.go", Start: 12, End: 20},
			},
		},
		{
			// The regression this test exists for. Go declares a func and a
			// literal on one line all the time -- `var f = func() int {`
			// beside its enclosing decl, a method whose body opens on the
			// signature line -- and both spans then START on the same line
			// with the enclosing one ending later.
			//
			// With an ascending End tie-break the WIDER span sorts second,
			// the scan reads it as "starts inside the narrow one and ends
			// past it", and reports a straddle. That is a false alarm on
			// legal input, and a false alarm in a property is how a real
			// straddle gets waved through as "that check is noisy".
			name: "enclosure on a shared start line, narrow listed first",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 12},
				{ID: 2, File: "a.go", Start: 10, End: 40},
			},
		},
		{
			name: "enclosure on a shared start line, wide listed first",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 40},
				{ID: 2, File: "a.go", Start: 10, End: 12},
			},
		},
		{
			name: "identical spans",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 40},
				{ID: 2, File: "a.go", Start: 10, End: 40},
			},
		},
		{
			name: "three spans sharing a start line",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 15},
				{ID: 2, File: "a.go", Start: 10, End: 60},
				{ID: 3, File: "a.go", Start: 10, End: 30},
			},
		},
		{
			name: "same start and end lines in different files never interact",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 40},
				{ID: 2, File: "b.go", Start: 20, End: 50},
			},
		},
		{
			name: "a real straddle is still caught",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 40},
				{ID: 2, File: "a.go", Start: 20, End: 50},
			},
			wantStrad: true,
		},
		{
			// The shared-start fix must not swallow this one: a wide span
			// opening on the same line as a narrow one, plus a third that
			// begins inside the wide span and runs past its end.
			name: "a straddle hiding behind a shared start line",
			spans: []SymbolSpan{
				{ID: 1, File: "a.go", Start: 10, End: 12},
				{ID: 2, File: "a.go", Start: 10, End: 40},
				{ID: 3, File: "a.go", Start: 30, End: 55},
			},
			wantStrad: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckSpansWellNested(tc.spans)
			switch {
			case tc.wantStrad && err == nil:
				t.Fatalf("spans %v straddle and were accepted", tc.spans)
			case !tc.wantStrad && err != nil:
				t.Fatalf("spans %v are well nested and were rejected: %v", tc.spans, err)
			}
		})
	}
}
