// Package gocover parses a Go coverage profile (`go test -coverprofile`)
// into per-file executed line spans, the raw material for attributing REAL
// production-code execution to symbols (and thence features) — as opposed to
// the gotest framework parser, which only records test pass/fail keyed to the
// test function name.
//
// Profile format (cover.out), one block per line after the mode header:
//
//	mode: set
//	github.com/org/repo/pkg/file.go:12.34,15.6 2 1
//	                    └file──────┘ └span─────┘ ↑  ↑
//	                                     stmts ──┘  └─ exec count
//
// span is startLine.startCol,endLine.endCol. count > 0 means the block ran.
// We collapse blocks to (file → executed line spans), which the ingest layer
// maps onto symbols via their [line, end_line] range. See issue: go-test
// coverage not attributed to features.
package gocover

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Block is one coverage block from the profile.
type Block struct {
	File      string
	StartLine int
	EndLine   int
	NumStmts  int
	Count     int // execution count; >0 means executed
}

// Executed reports whether the block ran at least once.
func (b Block) Executed() bool { return b.Count > 0 }

// Parse reads a Go coverage profile and returns its blocks in file order.
// The leading `mode:` line is consumed and ignored. Malformed lines return
// an error (a truncated/garbage profile should fail loudly, not silently
// under-report coverage).
func Parse(r io.Reader) ([]Block, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var blocks []Block
	first := true
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
			// No mode header — tolerate and fall through to parse it as a block.
		}
		b, err := parseBlock(line)
		if err != nil {
			return nil, fmt.Errorf("gocover: line %d: %w", lineNo, err)
		}
		blocks = append(blocks, b)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gocover: scan: %w", err)
	}
	return blocks, nil
}

// parseBlock parses one `file:sL.sC,eL.eC stmts count` line.
func parseBlock(line string) (Block, error) {
	// Split off the trailing " <stmts> <count>".
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return Block{}, fmt.Errorf("expected 3 space-separated fields, got %d (%q)", len(fields), line)
	}
	stmts, err := strconv.Atoi(fields[1])
	if err != nil {
		return Block{}, fmt.Errorf("num-statements %q: %w", fields[1], err)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return Block{}, fmt.Errorf("count %q: %w", fields[2], err)
	}
	// fields[0] = "file.go:sL.sC,eL.eC" — the file may itself contain ':'
	// only on Windows drive paths (not a concern for repo-relative Go paths),
	// so split on the LAST ':' before the span.
	colon := strings.LastIndexByte(fields[0], ':')
	if colon < 0 {
		return Block{}, fmt.Errorf("missing ':' span separator in %q", fields[0])
	}
	file := fields[0][:colon]
	span := fields[0][colon+1:]
	startLine, endLine, err := parseSpan(span)
	if err != nil {
		return Block{}, fmt.Errorf("span %q: %w", span, err)
	}
	return Block{File: file, StartLine: startLine, EndLine: endLine, NumStmts: stmts, Count: count}, nil
}

// parseSpan parses "sL.sC,eL.eC" → (sL, eL).
func parseSpan(span string) (startLine, endLine int, err error) {
	comma := strings.IndexByte(span, ',')
	if comma < 0 {
		return 0, 0, fmt.Errorf("missing ',' in span")
	}
	startLine, err = lineOf(span[:comma])
	if err != nil {
		return 0, 0, err
	}
	endLine, err = lineOf(span[comma+1:])
	if err != nil {
		return 0, 0, err
	}
	return startLine, endLine, nil
}

// lineOf parses "L.C" → L (drops the column).
func lineOf(lc string) (int, error) {
	dot := strings.IndexByte(lc, '.')
	if dot < 0 {
		return 0, fmt.Errorf("missing '.' in %q", lc)
	}
	return strconv.Atoi(lc[:dot])
}

// ExecutedSpansByFile collapses blocks to the set of executed line spans per
// file (count > 0). Used by the ingest layer to decide, per symbol, whether
// any of its [line, end_line] range ran.
func ExecutedSpansByFile(blocks []Block) map[string][][2]int {
	out := map[string][][2]int{}
	for _, b := range blocks {
		if !b.Executed() {
			continue
		}
		out[b.File] = append(out[b.File], [2]int{b.StartLine, b.EndLine})
	}
	return out
}

// BlocksByFile groups ALL blocks (executed or not) by file. Used by the
// ingest layer (Tier B) to compute, per owned symbol, the statement-level
// fraction: Σ NumStmts of blocks whose span falls within the symbol's
// [line, end_line] range, and the executed subset of that sum. This is the
// raw material for line-weighted per-feature coverage that tracks
// `go tool cover -func`.
func BlocksByFile(blocks []Block) map[string][]Block {
	out := map[string][]Block{}
	for _, b := range blocks {
		out[b.File] = append(out[b.File], b)
	}
	return out
}
