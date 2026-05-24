package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newCodebaseCmd builds the `atlas codebase` group: structural lookups
// against the persisted SQLite state.
func newCodebaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codebase",
		Short: "Structural lookups against the indexed codebase",
		Long:  "codebase groups read-only verbs that answer 'where is X' against the SQLite state.",
	}
	cmd.AddCommand(newCodebaseFindCmd())
	cmd.AddCommand(newCodebaseBCCmd())
	cmd.AddCommand(newCodebaseAggCmd())
	cmd.AddCommand(newCodebaseConsumerCmd())
	cmd.AddCommand(newCodebaseEmitCmd())
	cmd.AddCommand(newCodebasePatternCmd())
	cmd.AddCommand(newCodebaseCyclesCmd())
	cmd.AddCommand(newCodebaseDeadCmd())
	return cmd
}

// --- find ---

func newCodebaseFindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "find <symbol>",
		Short: "Look up file:line for a qualified symbol name",
		Long: `find resolves a fully-qualified symbol name (e.g. "auth.AuthHandler.Login")
to its position in the codebase via the persisted symbols table.

If no exact match exists, find performs a case-sensitive suffix match
('Login' will resolve 'auth.AuthHandler.Login') and returns the first hit.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodebaseFind(cmd, args[0])
		},
	}
	return cmd
}

type codebaseFindResult struct {
	Symbol *store.SymbolRow `json:"symbol,omitempty"`
}

func runCodebaseFind(cmd *cobra.Command, name string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Try exact match first.
	row, err := s.Symbols().FindByQualifiedName(ctx, shared.SymbolID(name))
	if errors.Is(err, shared.ErrSymbolNotFound) {
		// Fall back: scan symbols looking for either a dotted-suffix match
		// ("Login" → "auth.AuthHandler.Login") or a substring match
		// ("PatientService" → "PatientService.saveWithEvents"). Dotted
		// suffix wins when both branches have a hit.
		all, err2 := s.Symbols().List(ctx, store.SymbolFilter{})
		if err2 != nil {
			return fmt.Errorf("codebase find: list symbols: %w", err2)
		}
		var substringHit store.SymbolRow
		for _, r := range all {
			qn := string(r.QualifiedName)
			if qn == name || hasDottedSuffix(qn, name) {
				row = r
				err = nil
				break
			}
			if substringHit.ID == 0 && strings.Contains(qn, name) {
				substringHit = r
			}
		}
		if err != nil && substringHit.ID != 0 {
			row = substringHit
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("codebase find %q: %w", name, err)
	}
	if row.ID == 0 {
		return fmt.Errorf("codebase find %q: no symbol matches", name)
	}

	rowCopy := row
	res := codebaseFindResult{Symbol: &rowCopy}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.find",
			map[string]any{"name": name}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s:%d  [%s]\n",
		row.QualifiedName, row.FilePath, row.Line, row.Kind)
	return nil
}

func hasDottedSuffix(qn, suffix string) bool {
	if qn == "" || suffix == "" {
		return false
	}
	// exact match counts as a suffix match (no dot prefix required when qn is
	// itself a leaf identifier with no namespace segments)
	if qn == suffix {
		return true
	}
	// require ".<suffix>" at the very end so we don't match "NewPatientService"
	// when looking for "PatientService"
	if len(qn) > len(suffix) && qn[len(qn)-len(suffix)-1] == '.' &&
		qn[len(qn)-len(suffix):] == suffix {
		return true
	}
	return false
}

// --- bc ---

func newCodebaseBCCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bc <bc-name>",
		Short: "List annotations/symbols within a bounded context",
		Long: `bc returns every annotation row inside files that declare
@atlas:bc <name>. Useful for "what's in this BC" inventories.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodebaseBC(cmd, args[0])
		},
	}
	return cmd
}

type codebaseBCResult struct {
	Annotations []store.AnnotationRow `json:"annotations"`
}

func runCodebaseBC(cmd *cobra.Command, name string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rows, err := s.EDA().ListByBC(ctx, name)
	if err != nil {
		return fmt.Errorf("codebase bc %q: %w", name, err)
	}
	res := codebaseBCResult{Annotations: rows}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.bc",
			map[string]any{"bc": name}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "bc %s: %d annotations\n", name, len(rows))
	for _, a := range rows {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s:%d  [%s] %s\n",
			a.FilePath, a.Line, a.Kind, a.Value)
	}
	return nil
}

// --- agg ---

func newCodebaseAggCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agg <id>",
		Short: "Aggregate declaration + canonical-service link",
		Long: `agg returns the @atlas:aggregate declaration for an aggregate id
plus its linked canonical-service site (when one exists).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodebaseAgg(cmd, args[0])
		},
	}
	return cmd
}

type codebaseAggResult struct {
	Aggregate store.AggregateView `json:"aggregate"`
}

func runCodebaseAgg(cmd *cobra.Command, id string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	view, err := s.EDA().FindAggregate(ctx, id)
	if err != nil {
		return fmt.Errorf("codebase agg %q: %w", id, err)
	}
	res := codebaseAggResult{Aggregate: view}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.agg",
			map[string]any{"id": id}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "aggregate %s\n", id)
	fmt.Fprintf(cmd.OutOrStdout(), "  decl: %s:%d  %s\n",
		view.Declaration.FilePath, view.Declaration.Line, view.Declaration.Value)
	if view.CanonicalService != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  service: %s:%d  %s\n",
			view.CanonicalService.FilePath,
			view.CanonicalService.Line,
			view.CanonicalService.Value)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "  service: (none)")
	}
	return nil
}

// --- consumer ---

func newCodebaseConsumerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer [<stream>]",
		Short: "List @atlas:consumer subscriptions (optionally filtered by stream)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stream := ""
			if len(args) > 0 {
				stream = args[0]
			}
			return runCodebaseConsumer(cmd, stream)
		},
	}
	return cmd
}

type codebaseConsumerResult struct {
	Consumers []store.ConsumerView `json:"consumers"`
}

func runCodebaseConsumer(cmd *cobra.Command, stream string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	views, err := s.EDA().ListConsumers(ctx, stream)
	if err != nil {
		return fmt.Errorf("codebase consumer: %w", err)
	}
	res := codebaseConsumerResult{Consumers: views}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.consumer",
			map[string]any{"stream": stream}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "consumers: %d", len(views))
	if stream != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (stream=%s)", stream)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	for _, v := range views {
		fmt.Fprintf(cmd.OutOrStdout(), "  stream=%s  %s:%d  %s\n",
			v.Stream, v.Annotation.FilePath, v.Annotation.Line, v.Annotation.Value)
	}
	return nil
}

// --- emit ---

func newCodebaseEmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "emit <event-name>",
		Short: "Emit + outbox-publish sites for a named event",
		Long: `emit groups every @atlas:event-emit and @atlas:outbox-publish
annotation for a given event name. Useful for "where does this event
fire from" and "is it published to the bus, or staged in the outbox".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodebaseEmit(cmd, args[0])
		},
	}
	return cmd
}

type codebaseEmitResult struct {
	Emit store.EventEmitView `json:"emit"`
}

func runCodebaseEmit(cmd *cobra.Command, name string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	view, err := s.EDA().FindEventEmitters(ctx, name)
	if err != nil {
		return fmt.Errorf("codebase emit %q: %w", name, err)
	}
	res := codebaseEmitResult{Emit: view}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.emit",
			map[string]any{"event": name}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "event %s: %d emitters / %d publishers\n",
		name, len(view.Emitters), len(view.Publishers))
	for _, e := range view.Emitters {
		fmt.Fprintf(cmd.OutOrStdout(), "  emit:    %s:%d  %s\n",
			e.FilePath, e.Line, e.Value)
	}
	for _, p := range view.Publishers {
		fmt.Fprintf(cmd.OutOrStdout(), "  publish: %s:%d  %s\n",
			p.FilePath, p.Line, p.Value)
	}
	return nil
}

// --- pattern ---

func newCodebasePatternCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pattern <name>",
		Short: "Symbols matching a Phase 6f pattern recogniser",
		Long: `pattern lists every symbol whose pattern_matches column carries a
hit for the given recogniser name (e.g. "canonical-service",
"event-recorder-embed", "outbox-append").`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodebasePattern(cmd, args[0])
		},
	}
	return cmd
}

type codebasePatternResult struct {
	Symbols []store.SymbolRow `json:"symbols"`
}

func runCodebasePattern(cmd *cobra.Command, name string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rows, err := s.Symbols().FindByPattern(ctx, name)
	if err != nil {
		return fmt.Errorf("codebase pattern %q: %w", name, err)
	}
	res := codebasePatternResult{Symbols: rows}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.pattern",
			map[string]any{"pattern": name}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pattern %s: %d symbols\n", name, len(rows))
	for _, r := range rows {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s:%d  [%s]\n",
			r.QualifiedName, r.FilePath, r.Line, r.Kind)
	}
	return nil
}

// --- helpers ---

func openStoreForRead(ctx context.Context) (*store.Store, error) {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	return s, nil
}
