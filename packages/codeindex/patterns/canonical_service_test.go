package patterns

import (
	"context"
	"testing"
)

func TestCanonicalService_PositiveCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		src       string
		wantCount int
	}{
		{
			name: "canonical saveWithEvents shape",
			src: `package svc
import "context"
type S struct{}
func (s *S) saveWithEvents(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		if err := s.repo.Save(ctx, nil); err != nil {
			return err
		}
		return s.outbox.AppendFromContext(ctx, nil)
	})
}
`,
			wantCount: 1,
		},
		{
			name: "anonymous closure arg name",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(_ context.Context) error {
		s.repo.Save(ctx, nil)
		s.outbox.Append(ctx, nil)
		return nil
	})
}
`,
			wantCount: 1,
		},
		{
			name: "Append before Save (order-agnostic)",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		s.outbox.Append(ctx, nil)
		s.repo.Save(ctx, nil)
		return nil
	})
}
`,
			wantCount: 1,
		},
		{
			name: "Update + AppendFromContext counts as canonical",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		s.repo.Update(ctx, nil)
		s.outbox.AppendFromContext(ctx, nil)
		return nil
	})
}
`,
			wantCount: 1,
		},
		{
			name: "two saveWithEvents methods in same file — two matches",
			src: `package svc
import "context"
type S struct{}
func (s *S) DoA(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		s.repo.Save(ctx, nil)
		s.outbox.Append(ctx, nil)
		return nil
	})
}
func (s *S) DoB(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		s.repo.Save(ctx, nil)
		s.outbox.Append(ctx, nil)
		return nil
	})
}
`,
			wantCount: 2,
		},
		{
			name: "nested closures inside UoW — still one match for outer",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		go func() { s.outbox.Append(ctx, nil) }()
		return s.repo.Save(ctx, nil)
	})
}
`,
			wantCount: 1,
		},
		{
			name: "configurable UoW method name (Execute)",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Execute(ctx, func(ctx context.Context) error {
		s.repo.Save(ctx, nil)
		s.outbox.Append(ctx, nil)
		return nil
	})
}
`,
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				UoWMethodNames: []string{"Run", "Execute"},
			}
			f := parseSrc(t, "svc/svc.go", tc.src)
			matches, err := MatchFile(context.Background(), cfg, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			canon := filterByPattern(matches, PatternCanonicalService)
			if got := len(canon); got != tc.wantCount {
				t.Fatalf("want %d canonical matches, got %d: %+v", tc.wantCount, got, canon)
			}
			for _, m := range canon {
				if m.Confidence < 1.0-1e-9 {
					t.Errorf("confidence %.2f < 1.0 for %s", m.Confidence, m.Symbol)
				}
			}
		})
	}
}

func TestCanonicalService_NegativeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "save without append",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		return s.repo.Save(ctx, nil)
	})
}
`,
		},
		{
			name: "append without save",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		return s.outbox.AppendFromContext(ctx, nil)
	})
}
`,
		},
		{
			name: "save + append in sibling calls, not in same closure",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	s.repo.Save(ctx, nil)
	return s.uow.Run(ctx, func(ctx context.Context) error {
		return s.outbox.AppendFromContext(ctx, nil)
	})
}
`,
		},
		{
			name: "no UoW wrapper — direct save + append",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	if err := s.repo.Save(ctx, nil); err != nil {
		return err
	}
	return s.outbox.Append(ctx, nil)
}
`,
		},
		{
			name: "UoW method name not in config",
			src: `package svc
import "context"
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Begin(ctx, func(ctx context.Context) error {
		s.repo.Save(ctx, nil)
		s.outbox.Append(ctx, nil)
		return nil
	})
}
`,
		},
		{
			name: "Run is called but the arg is not a closure (passing a func value)",
			src: `package svc
import "context"
type S struct{}
func (s *S) handler(ctx context.Context) error { return nil }
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, s.handler)
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{} // defaults — UoW=Run
			f := parseSrc(t, "svc/svc.go", tc.src)
			matches, err := MatchFile(context.Background(), cfg, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			canon := filterByPattern(matches, PatternCanonicalService)
			if len(canon) != 0 {
				t.Fatalf("want 0 canonical matches, got %d: %+v", len(canon), canon)
			}
		})
	}
}

// TestMatchAllFiles_StableOrdering covers the cross-recogniser pressure
// dimension: multiple recogniser kinds in one file, output must be
// deterministically ordered.
func TestMatchAllFiles_StableOrdering(t *testing.T) {
	t.Parallel()

	src := `package svc
import (
	"context"
	sharedAgg "x/agg"
)
type Subject struct {
	sharedAgg.EventRecorder
}
type S struct{}
func (s *S) Do(ctx context.Context) error {
	return s.uow.Run(ctx, func(ctx context.Context) error {
		s.repo.Save(ctx, nil)
		return s.outbox.Append(ctx, nil)
	})
}
`
	f := parseSrc(t, "svc/svc.go", src)

	// Run twice — order must be identical.
	m1, _ := MatchFile(context.Background(), Config{}, f)
	m2, _ := MatchFile(context.Background(), Config{}, f)
	if len(m1) != len(m2) {
		t.Fatalf("non-deterministic count: %d vs %d", len(m1), len(m2))
	}
	for i := range m1 {
		if m1[i] != m2[i] {
			t.Fatalf("non-deterministic order at %d: %+v vs %+v", i, m1[i], m2[i])
		}
	}
	// At minimum: 1 EventRecorder embed + 1 outbox append + 1 canonical service.
	if len(m1) < 3 {
		t.Fatalf("want at least 3 matches across recognisers, got %d: %+v", len(m1), m1)
	}
}

// TestMatchAllFiles_ContextCancel covers the cancellation pressure dim.
func TestMatchAllFiles_ContextCancel(t *testing.T) {
	t.Parallel()
	src := `package svc
type S struct{}
func (s *S) Do() {}
`
	f := parseSrc(t, "svc/svc.go", src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MatchAllFiles(ctx, Config{}, []FileInput{f, f, f}); err == nil {
		t.Fatal("expected cancellation error")
	}
}
