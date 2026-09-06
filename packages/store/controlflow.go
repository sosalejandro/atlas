package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// ---------------------------------------------------------------------------
// cfg_* — control flow inside a symbol (migration 0015, issue #127).
//
// Every other port here describes symbols and the edges BETWEEN them. This one
// describes what happens inside one: the branches, the loops, and — where the
// execution data can honestly support it — which way each branch actually
// went. The distinction matters because statement coverage says a line ran,
// not that the branch was taken both ways, and the untaken half is where the
// production incidents live.
// ---------------------------------------------------------------------------

// Finding kinds, matching the CHECK constraint on `cfg_findings.kind`.
const (
	// FindingQueryInLoop is the N+1 candidate: a query reached from inside a
	// loop body. Always carries a confidence — see FlowFinding.Confidence.
	FindingQueryInLoop = "flow.query-in-loop"
	// FindingUnreachable is a block no path from the entry can reach. This is
	// a STRUCTURAL fact ("no execution can get here"), categorically different
	// from "no test got here", and the two must never be merged.
	FindingUnreachable = "flow.unreachable"
	// FindingUntestedBranch is a decision outcome the profile shows was never
	// taken. Only ever raised for outcomes the profile can actually judge:
	// an outcome nothing could observe is not evidence of an untested branch.
	FindingUntestedBranch = "flow.untested-branch"
)

// FlowBlock is one CFG node (`cfg_blocks`). Index 0 is always the synthetic
// entry and index 1 the synthetic exit, so a renderer can anchor a flowchart
// without loading the whole function first.
type FlowBlock struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// FlowEdge is one CFG edge (`cfg_edges`). Condition carries the source text of
// the expression gating the edge, which is what makes a rendered flowchart
// readable without re-parsing the file.
type FlowEdge struct {
	Index     int    `json:"index"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	Kind      string `json:"kind"`
	Condition string `json:"condition,omitempty"`
}

// FlowMetrics is one symbol's structural summary (`cfg_symbols`).
//
// These are facts about the SOURCE and stay valid with no test evidence at
// all — which is exactly why they are stored apart from DecisionCoverage.
// Complexity is the number the hotspot ranking (#93) weights by and which did
// not exist in the store before this migration.
type FlowMetrics struct {
	SymbolID   int64 `json:"symbol_id"`
	Complexity int   `json:"complexity"`
	Decisions  int   `json:"decisions"`
	BranchArms int   `json:"branch_arms"`
	// Conditions / ConditionsIndependent are the MC/DC enumeration: how many
	// atomic conditions exist, and how many could in principle be varied on
	// their own. They are NOT an MC/DC result — no statement-coverage profile
	// can produce one — and any surface printing them must say so.
	Conditions            int `json:"conditions"`
	ConditionsIndependent int `json:"conditions_independent"`
	Defers                int `json:"defers"`
	// UnreachableBlocks is 0 for any function containing a `goto`, and that 0
	// means "nothing claimed", not "none found". The builder does not draw
	// goto edges, so a label reached only by one has no predecessor in the
	// graph and a reachability walk there would report live code as dead. The
	// writer declines instead, and says so on the run.
	UnreachableBlocks int       `json:"unreachable_blocks"`
	BuiltAt           time.Time `json:"built_at"`
}

// SymbolFlow is a symbol's whole control-flow record.
type SymbolFlow struct {
	SymbolID int64       `json:"symbol_id"`
	Blocks   []FlowBlock `json:"blocks"`
	Edges    []FlowEdge  `json:"edges"`
	Metrics  FlowMetrics `json:"metrics"`
}

// DecisionCoverage is the execution-derived half (`cfg_decision_coverage`).
//
// The three counters are separate on purpose. OutcomesTotal is every branch
// outcome the source has; OutcomesDecidable is how many of those a statement-
// coverage profile can judge at all (a `&&` operand's outcome never is);
// OutcomesTaken is how many of the decidable ones were taken.
//
// Every outcome therefore carries one of three verdicts, and the third is a
// verdict and not a gap: taken, not taken, or UNDETERMINED. The undetermined
// ones are OutcomesTotal - OutcomesDecidable, exposed as Undetermined().
//
// The absence of a row is a fourth state again — "never measured" — and it is
// what a symbol whose file no profile covered must be left in. A zero-valued
// row would read as "no branch was taken", which is a claim about the tests
// that nothing in that run supports.
type DecisionCoverage struct {
	SymbolID          int64     `json:"symbol_id"`
	OutcomesTotal     int       `json:"outcomes_total"`
	OutcomesDecidable int       `json:"outcomes_decidable"`
	OutcomesTaken     int       `json:"outcomes_taken"`
	Source            string    `json:"source,omitempty"`
	MeasuredAt        time.Time `json:"measured_at"`
}

// Percent is taken/decidable, and whether the ratio exists at all.
//
// The bool is not ceremony. A symbol whose only branch is a short-circuit
// operator has NO judgeable outcome; reporting 0% for it would be
// indistinguishable from a symbol whose branches were all missed, and a reader
// would act differently on the two.
func (d DecisionCoverage) Percent() (float64, bool) {
	if d.OutcomesDecidable == 0 {
		return 0, false
	}
	return 100 * float64(d.OutcomesTaken) / float64(d.OutcomesDecidable), true
}

// Undetermined is the size of the blind spot: outcomes that exist in the
// source and that no statement-coverage profile can judge. They are neither
// taken nor untaken, and a surface that renders them as either is reporting a
// verdict the data does not contain.
func (d DecisionCoverage) Undetermined() int {
	n := d.OutcomesTotal - d.OutcomesDecidable
	if n < 0 {
		return 0
	}
	return n
}

// FlowFinding is one diagnostic (`cfg_findings`).
//
// Confidence is mandatory and not decoration: a query inside a loop is a
// smell, not a proof — one behind a cache is fine — and a finding that does
// not say how sure it is gets muted wholesale the first time it is wrong.
type FlowFinding struct {
	SymbolID   int64  `json:"symbol_id"`
	Index      int    `json:"index"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Line       int    `json:"line"`
	// RelatedLine is the finding's second site — the loop, for a query-in-loop
	// candidate. The fix is at the loop and the cost is at the query; naming
	// only one of them sends the reader looking for the other.
	RelatedLine int    `json:"related_line,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// ControlFlow is the narrow port for the cfg_* tables.
type ControlFlow interface {
	// Replace rewrites one symbol's blocks, edges and metrics in a single
	// transaction. Rewriting rather than merging is what keeps two generations
	// of blocks from interleaving after a re-scan: an edge count that silently
	// doubles is a cyclomatic complexity that silently doubles.
	Replace(ctx context.Context, flow SymbolFlow) error

	// Get returns one symbol's flow, or shared.ErrNotFound when the symbol has
	// no flow recorded (never analysed, or analysed and since deleted).
	Get(ctx context.Context, symbolID int64) (SymbolFlow, error)

	// TopComplexity returns the most complex symbols first — the ranking the
	// hotspot score wants. limit <= 0 applies DefaultFlowListLimit.
	TopComplexity(ctx context.Context, limit int) ([]FlowMetrics, error)

	// SetDecisionCoverage records one symbol's measured decision coverage,
	// replacing any previous measurement (a re-measurement is a correction,
	// not a second data point).
	SetDecisionCoverage(ctx context.Context, dc DecisionCoverage) error

	// GetDecisionCoverage returns one symbol's measurement, or
	// shared.ErrNotFound when the symbol has never been measured. That error
	// is meaningfully different from a zero-valued row: "not measured" must
	// not render as "0% covered".
	GetDecisionCoverage(ctx context.Context, symbolID int64) (DecisionCoverage, error)

	// ReplaceFindings rewrites one symbol's findings. Passing nil clears them,
	// which is the case that matters: a fixed N+1 must leave the report.
	ReplaceFindings(ctx context.Context, symbolID int64, findings []FlowFinding) error

	// Findings lists findings of one kind, highest confidence first. An empty
	// kind lists every kind.
	Findings(ctx context.Context, kind string) ([]FlowFinding, error)
}

// DefaultFlowListLimit caps an unbounded TopComplexity read. The table has one
// row per indexed function, so an uncapped read on a large repo is tens of
// thousands of rows nobody looks past the top of.
const DefaultFlowListLimit = 200

var _ ControlFlow = (*controlFlowStore)(nil)

// ControlFlow returns the Store's ControlFlow port.
func (s *Store) ControlFlow() ControlFlow {
	return &controlFlowStore{db: s, q: s.queries()}
}

type controlFlowStore struct {
	db *Store
	q  *sqlc.Queries
}

func (c *controlFlowStore) Replace(ctx context.Context, flow SymbolFlow) error {
	if flow.SymbolID == 0 {
		return fmt.Errorf("control flow replace: symbol_id required")
	}
	tx, err := c.db.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control flow replace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := c.q.WithTx(tx)

	// Clear first: a rebuild that finds FEWER blocks must shrink the stored
	// graph, not leave the surplus behind.
	if err := qtx.DeleteCFGBlocks(ctx, flow.SymbolID); err != nil {
		return fmt.Errorf("control flow replace: clear blocks (symbol %d): %w", flow.SymbolID, err)
	}
	if err := qtx.DeleteCFGEdges(ctx, flow.SymbolID); err != nil {
		return fmt.Errorf("control flow replace: clear edges (symbol %d): %w", flow.SymbolID, err)
	}
	if err := insertFlowRows(ctx, qtx, flow); err != nil {
		return err
	}
	m := flow.Metrics
	if err := qtx.UpsertCFGSymbol(ctx, sqlc.UpsertCFGSymbolParams{
		SymbolID:              flow.SymbolID,
		Complexity:            int64(m.Complexity),
		Decisions:             int64(m.Decisions),
		BranchArms:            int64(m.BranchArms),
		Conditions:            int64(m.Conditions),
		ConditionsIndependent: int64(m.ConditionsIndependent),
		Defers:                int64(m.Defers),
		UnreachableBlocks:     int64(m.UnreachableBlocks),
	}); err != nil {
		return fmt.Errorf("control flow replace: metrics (symbol %d): %w", flow.SymbolID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("control flow replace: commit (symbol %d): %w", flow.SymbolID, err)
	}
	return nil
}

func insertFlowRows(ctx context.Context, qtx *sqlc.Queries, flow SymbolFlow) error {
	for _, b := range flow.Blocks {
		if err := qtx.InsertCFGBlock(ctx, sqlc.InsertCFGBlockParams{
			SymbolID:   flow.SymbolID,
			BlockIndex: int64(b.Index),
			Kind:       b.Kind,
			StartLine:  int64(b.StartLine),
			EndLine:    int64(b.EndLine),
		}); err != nil {
			return fmt.Errorf("control flow replace: block %d (symbol %d): %w", b.Index, flow.SymbolID, err)
		}
	}
	for _, e := range flow.Edges {
		if err := qtx.InsertCFGEdge(ctx, sqlc.InsertCFGEdgeParams{
			SymbolID:  flow.SymbolID,
			EdgeIndex: int64(e.Index),
			FromBlock: int64(e.From),
			ToBlock:   int64(e.To),
			Kind:      e.Kind,
			Condition: e.Condition,
		}); err != nil {
			return fmt.Errorf("control flow replace: edge %d (symbol %d): %w", e.Index, flow.SymbolID, err)
		}
	}
	return nil
}

func (c *controlFlowStore) Get(ctx context.Context, symbolID int64) (SymbolFlow, error) {
	row, err := c.q.GetCFGSymbol(ctx, symbolID)
	if errors.Is(err, sql.ErrNoRows) {
		return SymbolFlow{}, shared.ErrNotFound
	}
	if err != nil {
		return SymbolFlow{}, fmt.Errorf("control flow get (symbol %d): %w", symbolID, err)
	}
	blocks, err := c.q.ListCFGBlocks(ctx, symbolID)
	if err != nil {
		return SymbolFlow{}, fmt.Errorf("control flow get blocks (symbol %d): %w", symbolID, err)
	}
	edges, err := c.q.ListCFGEdges(ctx, symbolID)
	if err != nil {
		return SymbolFlow{}, fmt.Errorf("control flow get edges (symbol %d): %w", symbolID, err)
	}
	out := SymbolFlow{SymbolID: symbolID, Metrics: fromSQLCFlowMetrics(row)}
	for _, b := range blocks {
		out.Blocks = append(out.Blocks, FlowBlock{
			Index: int(b.BlockIndex), Kind: b.Kind,
			StartLine: int(b.StartLine), EndLine: int(b.EndLine),
		})
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, FlowEdge{
			Index: int(e.EdgeIndex), From: int(e.FromBlock), To: int(e.ToBlock),
			Kind: e.Kind, Condition: e.Condition,
		})
	}
	return out, nil
}

func fromSQLCFlowMetrics(row sqlc.CfgSymbol) FlowMetrics {
	return FlowMetrics{
		SymbolID:              row.SymbolID,
		Complexity:            int(row.Complexity),
		Decisions:             int(row.Decisions),
		BranchArms:            int(row.BranchArms),
		Conditions:            int(row.Conditions),
		ConditionsIndependent: int(row.ConditionsIndependent),
		Defers:                int(row.Defers),
		UnreachableBlocks:     int(row.UnreachableBlocks),
		BuiltAt:               row.BuiltAt,
	}
}

func (c *controlFlowStore) TopComplexity(ctx context.Context, limit int) ([]FlowMetrics, error) {
	if limit <= 0 {
		limit = DefaultFlowListLimit
	}
	rows, err := c.q.ListCFGSymbolsByComplexity(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("control flow top complexity: %w", err)
	}
	out := make([]FlowMetrics, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCFlowMetrics(r))
	}
	return out, nil
}

func (c *controlFlowStore) SetDecisionCoverage(ctx context.Context, dc DecisionCoverage) error {
	if dc.SymbolID == 0 {
		return fmt.Errorf("control flow decision coverage: symbol_id required")
	}
	if dc.OutcomesTaken > dc.OutcomesDecidable || dc.OutcomesDecidable > dc.OutcomesTotal {
		// Refuse rather than store a ratio that cannot be true. A stored
		// taken > decidable would render as coverage above 100% and destroy
		// the credibility of every other number on the same screen.
		return fmt.Errorf(
			"control flow decision coverage (symbol %d): impossible counters taken=%d decidable=%d total=%d",
			dc.SymbolID, dc.OutcomesTaken, dc.OutcomesDecidable, dc.OutcomesTotal)
	}
	if err := c.q.UpsertCFGDecisionCoverage(ctx, sqlc.UpsertCFGDecisionCoverageParams{
		SymbolID:          dc.SymbolID,
		OutcomesTotal:     int64(dc.OutcomesTotal),
		OutcomesDecidable: int64(dc.OutcomesDecidable),
		OutcomesTaken:     int64(dc.OutcomesTaken),
		Source:            dc.Source,
	}); err != nil {
		return fmt.Errorf("control flow decision coverage (symbol %d): %w", dc.SymbolID, err)
	}
	return nil
}

func (c *controlFlowStore) GetDecisionCoverage(ctx context.Context, symbolID int64) (DecisionCoverage, error) {
	row, err := c.q.GetCFGDecisionCoverage(ctx, symbolID)
	if errors.Is(err, sql.ErrNoRows) {
		return DecisionCoverage{}, shared.ErrNotFound
	}
	if err != nil {
		return DecisionCoverage{}, fmt.Errorf("control flow decision coverage get (symbol %d): %w", symbolID, err)
	}
	return DecisionCoverage{
		SymbolID:          row.SymbolID,
		OutcomesTotal:     int(row.OutcomesTotal),
		OutcomesDecidable: int(row.OutcomesDecidable),
		OutcomesTaken:     int(row.OutcomesTaken),
		Source:            row.Source,
		MeasuredAt:        row.MeasuredAt,
	}, nil
}

func (c *controlFlowStore) ReplaceFindings(ctx context.Context, symbolID int64, findings []FlowFinding) error {
	if symbolID == 0 {
		return fmt.Errorf("control flow findings: symbol_id required")
	}
	tx, err := c.db.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control flow findings: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := c.q.WithTx(tx)

	if err := qtx.DeleteCFGFindings(ctx, symbolID); err != nil {
		return fmt.Errorf("control flow findings: clear (symbol %d): %w", symbolID, err)
	}
	for i, f := range findings {
		if err := qtx.InsertCFGFinding(ctx, sqlc.InsertCFGFindingParams{
			SymbolID:     symbolID,
			FindingIndex: int64(i),
			Kind:         f.Kind,
			Confidence:   f.Confidence,
			Line:         int64(f.Line),
			RelatedLine:  int64(f.RelatedLine),
			Detail:       f.Detail,
		}); err != nil {
			return fmt.Errorf("control flow findings: insert %s (symbol %d): %w", f.Kind, symbolID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("control flow findings: commit (symbol %d): %w", symbolID, err)
	}
	return nil
}

func (c *controlFlowStore) Findings(ctx context.Context, kind string) ([]FlowFinding, error) {
	var rows []sqlc.CfgFinding
	var err error
	if kind == "" {
		rows, err = c.q.ListAllCFGFindings(ctx)
	} else {
		rows, err = c.q.ListCFGFindingsByKind(ctx, kind)
	}
	if err != nil {
		return nil, fmt.Errorf("control flow findings list (%q): %w", kind, err)
	}
	out := make([]FlowFinding, 0, len(rows))
	for _, r := range rows {
		out = append(out, FlowFinding{
			SymbolID:    r.SymbolID,
			Index:       int(r.FindingIndex),
			Kind:        r.Kind,
			Confidence:  r.Confidence,
			Line:        int(r.Line),
			RelatedLine: int(r.RelatedLine),
			Detail:      r.Detail,
		})
	}
	return out, nil
}
