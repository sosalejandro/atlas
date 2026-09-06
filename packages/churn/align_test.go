package churn

import "testing"

// alignReport builds a report whose tracked set is the given paths. The
// mining half is irrelevant here: alignment is a question about namespaces,
// not about history.
func alignReport(t *testing.T, tracked ...string) *Report {
	t.Helper()
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  joinNUL(tracked...),
		"log":       "",
	}}
	return mine(t, g, Options{})
}

func joinNUL(paths ...string) string {
	out := ""
	for _, p := range paths {
		out += p + "\x00"
	}
	return out
}

func TestAlignTo(t *testing.T) {
	cases := []struct {
		name       string
		tracked    []string
		paths      []string
		wantPrefix string
		wantOK     bool
	}{
		{
			name:    "same namespace needs no rebasing",
			tracked: []string{"pkg/a.go", "pkg/b.go"},
			paths:   []string{"pkg/a.go"},
			wantOK:  true,
		},
		{
			name:       "a scan root one level down is found",
			tracked:    []string{"svc/pkg/a.go", "svc/pkg/b.go", "other/z.go"},
			paths:      []string{"pkg/a.go", "pkg/b.go"},
			wantPrefix: "svc",
			wantOK:     true,
		},
		{
			name:       "a nested scan root is found whole",
			tracked:    []string{"a/b/c/x.go"},
			paths:      []string{"c/x.go"},
			wantPrefix: "a/b",
			wantOK:     true,
		},
		{
			name:    "two equally good sub-directories are not guessed between",
			tracked: []string{"x/a.go", "y/a.go"},
			paths:   []string{"a.go"},
			wantOK:  false,
		},
		{
			name:    "genuinely untracked paths align to nothing",
			tracked: []string{"pkg/a.go"},
			paths:   []string{"brand/new.go"},
			wantOK:  false,
		},
		{
			name:    "an empty tracked set cannot answer",
			tracked: nil,
			paths:   []string{"a.go"},
			wantOK:  false,
		},
		{
			name:       "one straggler does not outvote the real root",
			tracked:    []string{"svc/a.go", "svc/b.go", "svc/c.go", "elsewhere/c.go"},
			paths:      []string{"a.go", "b.go", "c.go"},
			wantPrefix: "svc",
			wantOK:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := alignReport(t, tc.tracked...)
			prefix, ok := rep.AlignTo(tc.paths)
			if ok != tc.wantOK || prefix != tc.wantPrefix {
				t.Errorf("AlignTo(%v) = (%q, %v), want (%q, %v)",
					tc.paths, prefix, ok, tc.wantPrefix, tc.wantOK)
			}
		})
	}
}

// Rebase must move both halves of the join — the scored files AND the
// tracked set — or ForFiles would call a rebased file untracked and hand
// back the neutral unknown score it hands back today.
func TestRebase_MovesFilesAndTrackedSet(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  joinNUL("svc/hot.go", "svc/quiet.go", "tool/other.go"),
		"log": commit("a1", "ann@example.com", daysAgo(2), "feat: work", "M\tsvc/hot.go") +
			commit("a2", "bob@example.com", daysAgo(3), "feat: more", "M\ttool/other.go"),
	}}
	rep := mine(t, g, Options{})

	sub := rep.Rebase("svc")
	if _, ok := sub.File("hot.go"); !ok {
		t.Fatalf("hot.go was not rebased; files = %v", sub.Files)
	}
	if _, ok := sub.File("svc/hot.go"); ok {
		t.Errorf("the pre-rebase key is still present; the roll-up would join twice")
	}
	if fc := sub.ForFiles([]string{"hot.go"}); fc.Status != StatusKnown || fc.Score <= 0 {
		t.Errorf("rebased roll-up = %+v, want known with a positive score", fc)
	}
	// quiet.go is tracked under svc but never committed to: known, zero.
	if fc := sub.ForFiles([]string{"quiet.go"}); fc.Status != StatusKnown || fc.Score != 0 {
		t.Errorf("quiet.go roll-up = %+v, want known/0", fc)
	}
	// Files outside the sub-directory cannot be named in the new namespace.
	if _, ok := sub.File("other.go"); ok {
		t.Errorf("a file outside the rebase prefix leaked into the report")
	}
	if sub.CommitsScanned != rep.CommitsScanned {
		t.Errorf("CommitsScanned = %d, want %d: the counters describe the mining pass, "+
			"which really did read the whole repository", sub.CommitsScanned, rep.CommitsScanned)
	}
	if rep.Rebase("") != rep {
		t.Errorf("Rebase(\"\") must be a no-op on the same report")
	}
	// Warnings must be copied, not aliased: the caller appends to one.
	sub.Warnings = append(sub.Warnings, "note")
	if len(rep.Warnings) != 0 {
		t.Errorf("appending to the rebased report's warnings mutated the original")
	}
}
