package patterns

import (
	"context"
	"go/parser"
	"go/token"
	"testing"
)

// parseSrc parses a Go source string and returns a FileInput suitable for
// the matcher. Test-only helper.
func parseSrc(t *testing.T, relPath, src string) FileInput {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", relPath, err)
	}
	return FileInput{File: file, FSet: fset, RelPath: relPath}
}

func TestOutboxAppend_PositiveCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		src         string
		wantCount   int
		wantMinConf float64
	}{
		{
			name: "chained selector svc.outbox.AppendFromContext",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	return s.outbox.AppendFromContext(nil, nil)
}
`,
			wantCount:   1,
			wantMinConf: 1.0,
		},
		{
			name: "chained selector r.outbox.Append",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	return s.outbox.Append(nil, nil)
}
`,
			wantCount:   1,
			wantMinConf: 1.0,
		},
		{
			name: "uppercase Outbox field",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	return s.Outbox.Append(nil, nil)
}
`,
			wantCount:   1,
			wantMinConf: 1.0,
		},
		{
			name: "bare outbox.Append (lower confidence)",
			src: `package svc
func Do(outbox interface{ Append(any, any) error }) error {
	return outbox.Append(nil, nil)
}
`,
			wantCount:   1,
			wantMinConf: 0.8,
		},
		{
			name: "two appends in same function — two matches",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	s.outbox.Append(nil, nil)
	return s.outbox.AppendFromContext(nil, nil)
}
`,
			wantCount:   2,
			wantMinConf: 1.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := parseSrc(t, "svc/svc.go", tc.src)
			matches, err := MatchFile(context.Background(), Config{}, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			outbox := filterByPattern(matches, PatternOutboxAppend)
			if got := len(outbox); got != tc.wantCount {
				t.Fatalf("want %d outbox matches, got %d: %+v", tc.wantCount, got, outbox)
			}
			for _, m := range outbox {
				if m.Confidence < tc.wantMinConf {
					t.Errorf("confidence %.2f < %.2f for %s", m.Confidence, tc.wantMinConf, m.Detail)
				}
			}
		})
	}
}

func TestOutboxAppend_NegativeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "wrong method name on outbox receiver",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	s.outbox.Drain(nil)
	return nil
}
`,
		},
		{
			name: "Append on a non-outbox receiver",
			src: `package svc
type S struct{}
func (s *S) Do() error {
	s.buffer.Append("x")
	return nil
}
`,
		},
		{
			name: "Append on a totally unrelated type",
			src: `package svc
type Slice []int
func (sl Slice) Append(x int) Slice { return append(sl, x) }
func Use() {
	var sl Slice
	sl.Append(1)
}
`,
		},
		{
			name: "method-literal reference (no call)",
			src: `package svc
type S struct{}
func (s *S) Do() {
	_ = s.outbox.Append
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := parseSrc(t, "svc/svc.go", tc.src)
			matches, err := MatchFile(context.Background(), Config{}, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			outbox := filterByPattern(matches, PatternOutboxAppend)
			if len(outbox) != 0 {
				t.Fatalf("want 0 outbox matches, got %d: %+v", len(outbox), outbox)
			}
		})
	}
}

func filterByPattern(matches []Match, pattern string) []Match {
	var out []Match
	for _, m := range matches {
		if m.Pattern == pattern {
			out = append(out, m)
		}
	}
	return out
}
