package annotations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The allocation half of the performance harness for this package (issue
// #152). `atlas scan` calls ParseRelative once per source file in the tree,
// so whatever it allocates per file is multiplied by the file count: on this
// repository it was 142.09 MB cumulative, 17.7% of a scan's 803 MB, for the
// job of finding a few hundred comment lines.
//
// Every figure in docs/performance.md §Annotation parsing comes from these
// benchmarks, with the command printed beside it.

// benchCorpus writes a deterministic three-file corpus into dir and returns
// the paths. Deterministic because a benchmark whose input differs between
// runs cannot be compared with its own previous output, and generated rather
// than sampled from the repository because the answer must not depend on
// which tree the benchmark happens to be run in.
//
// The three sizes are the three shapes that matter to the per-file cost:
//
//	tiny   — 551 B, the committed Go fixture. Files this small are where a
//	         fixed per-file overhead (a 64 KB scanner buffer, say) is the
//	         entire cost, and a real tree has a long tail of them.
//	medium — ~8 KB, the size of an ordinary source file.
//	large  — ~512 KB, where the cost of rebuilding the file into a growing
//	         bytes.Buffer overtakes the fixed overhead.
func benchCorpus(tb testing.TB, dir string) map[string]string {
	tb.Helper()

	tiny, err := os.ReadFile(filepath.Join("testdata", "login_test.go.fixture"))
	if err != nil {
		tb.Fatalf("read fixture: %v", err)
	}

	paths := map[string]string{}
	for _, c := range []struct {
		name    string
		content []byte
	}{
		{"tiny_551B", tiny},
		{"medium_8KB", genGoSource(8 << 10)},
		{"large_512KB", genGoSource(512 << 10)},
	} {
		p := filepath.Join(dir, c.name+".go")
		if err := os.WriteFile(p, c.content, 0o644); err != nil {
			tb.Fatalf("write %s: %v", p, err)
		}
		paths[c.name] = p
	}
	return paths
}

// genGoSource builds at least approxBytes of plausible Go: mostly code, one
// annotation-bearing comment every 20 lines. The ratio matters — a file that
// is all comments would exercise the annotation matchers rather than the
// read, and the read is what this measures.
func genGoSource(approxBytes int) []byte {
	var buf bytes.Buffer
	buf.WriteString("package bench\n\nimport \"context\"\n\n")
	for i := 0; buf.Len() < approxBytes; i++ {
		switch i % 20 {
		case 0:
			fmt.Fprintf(&buf, "// @atlas:feature bench.case%d #real\n", i)
		case 1:
			fmt.Fprintf(&buf, "// ordinary prose about case %d that carries no annotation\n", i)
		case 2:
			fmt.Fprintf(&buf, "/* @atlas:owner platform-team */\n")
		default:
			fmt.Fprintf(&buf, "func case%d(ctx context.Context) error { _ = ctx; return nil }\n", i)
		}
	}
	return buf.Bytes()
}

// BenchmarkParseRelative prices one file, which is the unit `atlas scan`
// multiplies. Run it as:
//
//	go test ./packages/codeindex/annotations -run '^$' \
//	    -bench BenchmarkParseRelative -benchmem -count 5
func BenchmarkParseRelative(b *testing.B) {
	paths := benchCorpus(b, b.TempDir())
	ctx := context.Background()

	for _, name := range []string{"tiny_551B", "medium_8KB", "large_512KB"} {
		p := paths[name]
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				anns, err := ParseRelative(ctx, p, "bench/"+name+".go")
				if err != nil {
					b.Fatalf("ParseRelative: %v", err)
				}
				if len(anns) == 0 {
					b.Fatalf("no annotations; the benchmark is measuring the wrong thing")
				}
			}
		})
	}
}
