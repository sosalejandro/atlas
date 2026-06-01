package gocover

import (
	"reflect"
	"strings"
	"testing"
)

const sampleProfile = `mode: set
github.com/org/repo/pkg/svc.go:12.34,15.6 2 1
github.com/org/repo/pkg/svc.go:18.2,20.10 3 0
github.com/org/repo/pkg/other.go:5.1,9.2 4 7
`

func TestParse_Sample(t *testing.T) {
	blocks, err := Parse(strings.NewReader(sampleProfile))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Block{
		{File: "github.com/org/repo/pkg/svc.go", StartLine: 12, EndLine: 15, NumStmts: 2, Count: 1},
		{File: "github.com/org/repo/pkg/svc.go", StartLine: 18, EndLine: 20, NumStmts: 3, Count: 0},
		{File: "github.com/org/repo/pkg/other.go", StartLine: 5, EndLine: 9, NumStmts: 4, Count: 7},
	}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks = %+v\nwant %+v", blocks, want)
	}
}

func TestParse_NoModeHeaderTolerated(t *testing.T) {
	blocks, err := Parse(strings.NewReader("github.com/x/y.go:1.1,2.2 1 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 1 || blocks[0].StartLine != 1 {
		t.Fatalf("got %+v", blocks)
	}
}

func TestParse_MalformedFailsLoudly(t *testing.T) {
	for _, bad := range []string{
		"mode: set\ngarbage line here\n",
		"mode: set\ngithub.com/x/y.go:1.1,2.2 notanumber 1\n",
		"mode: set\ngithub.com/x/y.go:nodot,2.2 1 1\n",
	} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func TestExecutedSpansByFile(t *testing.T) {
	blocks, _ := Parse(strings.NewReader(sampleProfile))
	spans := ExecutedSpansByFile(blocks)
	// svc.go: only the count=1 block (12-15) executed; the count=0 (18-20) dropped.
	if got := spans["github.com/org/repo/pkg/svc.go"]; !reflect.DeepEqual(got, [][2]int{{12, 15}}) {
		t.Errorf("svc.go spans = %v, want [[12 15]]", got)
	}
	if got := spans["github.com/org/repo/pkg/other.go"]; !reflect.DeepEqual(got, [][2]int{{5, 9}}) {
		t.Errorf("other.go spans = %v, want [[5 9]]", got)
	}
}
