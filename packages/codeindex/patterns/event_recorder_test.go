package patterns

import (
	"context"
	"testing"
)

func TestEventRecorderEmbed_PositiveCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		src       string
		wantConf  float64
		wantTypes []string
	}{
		{
			name: "qualified value embed (sharedAgg.EventRecorder)",
			src: `package domain
import sharedAgg "x/agg"
type Subject struct {
	sharedAgg.EventRecorder
	Name string
}
`,
			wantConf:  1.0,
			wantTypes: []string{"Subject"},
		},
		{
			name: "qualified pointer embed (*sharedAgg.EventRecorder)",
			src: `package domain
import sharedAgg "x/agg"
type Order struct {
	*sharedAgg.EventRecorder
	ID int
}
`,
			wantConf:  1.0,
			wantTypes: []string{"Order"},
		},
		{
			name: "same-package value embed",
			src: `package agg
type EventRecorder struct{}
type Cart struct {
	EventRecorder
	Items []int
}
`,
			wantConf:  0.95,
			wantTypes: []string{"Cart"},
		},
		{
			name: "same-package pointer embed",
			src: `package agg
type EventRecorder struct{}
type Cart struct {
	*EventRecorder
	Items []int
}
`,
			wantConf:  0.95,
			wantTypes: []string{"Cart"},
		},
		{
			name: "multiple aggregates in one file",
			src: `package domain
import sharedAgg "x/agg"
type Subject struct {
	sharedAgg.EventRecorder
}
type Plan struct {
	sharedAgg.EventRecorder
}
`,
			wantConf:  1.0,
			wantTypes: []string{"Plan", "Subject"}, // sorted by line/name
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := parseSrc(t, "domain/agg.go", tc.src)
			matches, err := MatchFile(context.Background(), Config{}, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			er := filterByPattern(matches, PatternEventRecorderEmbed)
			if len(er) != len(tc.wantTypes) {
				t.Fatalf("want %d matches, got %d: %+v", len(tc.wantTypes), len(er), er)
			}
			got := make(map[string]bool, len(er))
			for _, m := range er {
				got[string(m.Symbol)] = true
				if m.Confidence < tc.wantConf-1e-9 {
					t.Errorf("confidence %.2f < %.2f for %s", m.Confidence, tc.wantConf, m.Symbol)
				}
			}
			for _, name := range tc.wantTypes {
				if !got[name] {
					t.Errorf("missing match for type %q (got %v)", name, got)
				}
			}
		})
	}
}

func TestEventRecorderEmbed_NegativeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "named field of type EventRecorder is NOT an embed",
			src: `package agg
type EventRecorder struct{}
type Subject struct {
	er EventRecorder
}
`,
		},
		{
			name: "named field with qualified type",
			src: `package domain
import sharedAgg "x/agg"
type Subject struct {
	recorder sharedAgg.EventRecorder
}
`,
		},
		{
			name: "embeds a similarly-named but different type",
			src: `package agg
type Recorder struct{}
type Subject struct {
	Recorder
}
`,
		},
		{
			name: "non-struct type def — interface with method",
			src: `package agg
type EventRecorder interface{ Record() }
`,
		},
		{
			name: "embed of wholly unrelated type",
			src: `package agg
type Base struct{}
type Subject struct {
	Base
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := parseSrc(t, "domain/x.go", tc.src)
			matches, err := MatchFile(context.Background(), Config{}, f)
			if err != nil {
				t.Fatalf("MatchFile: %v", err)
			}
			er := filterByPattern(matches, PatternEventRecorderEmbed)
			if len(er) != 0 {
				t.Fatalf("want 0 EventRecorder matches, got %d: %+v", len(er), er)
			}
		})
	}
}
