// Command calibrate runs the patterns recognisers against a real Go tree
// and prints every match. Intended for hand-eyeballing precision/recall
// during Phase 6f recogniser tuning — NOT part of any user-facing flow.
//
// Usage:
//
//	go run ./packages/codeindex/patterns/internal/calibrate \
//	    -root ~/Documents/startup-projects/nutrition-v2-go/src/contexts/measurements
package main

import (
	"context"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
)

func main() {
	root := flag.String("root", "", "absolute path to a Go source tree to calibrate against")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: calibrate -root <abs-path>")
		os.Exit(2)
	}

	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	if _, err := os.Stat(rootAbs); err != nil {
		fmt.Fprintf(os.Stderr, "calibrate: target %s missing — skipping cleanly\n", rootAbs)
		return
	}

	var inputs []patterns.FileInput
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", path, err)
			return nil
		}
		rel, _ := filepath.Rel(rootAbs, path)
		inputs = append(inputs, patterns.FileInput{
			File:    file,
			FSet:    fset,
			RelPath: filepath.ToSlash(rel),
		})
		return nil
	})
	if err != nil {
		fail(err)
	}

	matches, err := patterns.MatchAllFiles(context.Background(), patterns.Config{}, inputs)
	if err != nil {
		fail(err)
	}

	byPattern := make(map[string][]patterns.Match)
	for _, m := range matches {
		byPattern[m.Pattern] = append(byPattern[m.Pattern], m)
	}

	patternNames := make([]string, 0, len(byPattern))
	for p := range byPattern {
		patternNames = append(patternNames, p)
	}
	sort.Strings(patternNames)

	fmt.Printf("Root: %s\nFiles scanned: %d\nTotal matches: %d\n\n", rootAbs, len(inputs), len(matches))
	for _, p := range patternNames {
		ms := byPattern[p]
		fmt.Printf("== %s (%d matches) ==\n", p, len(ms))
		for _, m := range ms {
			fmt.Printf("  %s:%d  %s  (%s, conf=%.2f)\n",
				m.Position.Path, m.Position.Line, m.Symbol, m.Detail, m.Confidence)
		}
		fmt.Println()
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "calibrate:", err)
	os.Exit(1)
}
