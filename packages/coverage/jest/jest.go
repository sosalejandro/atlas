// Package jest parses Jest's `--json` reporter output.
//
// Jest's report shape (`testResults[].assertionResults[]`) is the same
// surface Vitest copied, so this parser delegates to vitest.FlattenReport
// for the row mapping and only owns the framework tag + summary shape.
package jest

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/coverage/vitest"
)

// Framework returns the constant identifying this parser.
func Framework() coverage.Framework { return coverage.FrameworkJest }

// jestHeader captures only the top-level timing fields. The per-test rows
// are flattened by vitest.FlattenReport against the same bytes.
type jestHeader struct {
	StartTime int64 `json:"startTime"` // epoch ms
	// EndTime is not always emitted by Jest; we derive a max-endTime
	// across testResults inside vitest.FlattenReport instead.
}

// Parse reads a Jest JSON report and returns a Run + Results pair.
func Parse(r io.Reader) (coverage.Run, []coverage.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return coverage.Run{}, nil, fmt.Errorf("jest: read: %w", err)
	}
	var hdr jestHeader
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return coverage.Run{}, nil, fmt.Errorf("jest: decode: %w", err)
	}

	results, pass, fail, skip, err := vitest.FlattenReport(raw)
	if err != nil {
		return coverage.Run{}, nil, fmt.Errorf("jest: flatten: %w", err)
	}
	run := coverage.Run{Framework: coverage.FrameworkJest}
	if hdr.StartTime > 0 {
		run.StartedAt = time.UnixMilli(hdr.StartTime).UTC()
	}
	summary, _ := json.Marshal(map[string]int{"pass": pass, "fail": fail, "skip": skip})
	run.SummaryJSON = string(summary)
	return run, results, nil
}
