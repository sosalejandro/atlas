package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	// The sqlite driver is registered here explicitly rather than relied on
	// transitively through packages/store. This command talks to the state
	// database with raw SQL on purpose -- see openStateDB -- so it should
	// not break if store's import chain is ever rearranged. Registering the
	// same driver twice is not possible: the blank import runs one init.
	_ "modernc.org/sqlite"

	"github.com/sosalejandro/atlas/packages/redact"
	"github.com/sosalejandro/atlas/packages/store"
)

// newSecurityCmd implements `atlas security`.
//
// The command exists because of a question no other verb answers: "if I send
// you this database, what am I sending?" Every security review asks it, and
// until now the only way to answer was to read the migrations. An answer
// derived from the database in front of you is also the only kind that
// cannot go stale.
func newSecurityCmd() *cobra.Command {
	var exportVerb string
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Report what the state database holds, what leaves the machine, and any secrets found",
		Long: `security answers the question a security review actually asks: if I hand
you this database, what am I handing you?

It reports three things, all read from the database in front of you rather
than from a document describing one:

  What is stored    every table, its row count, and what kind of content it
                    holds. The state database is NOT metadata: it holds SQL
                    query text verbatim, the source text of every branch
                    condition, and -- inside a snapshot -- the doc comment
                    and signature of every symbol.

  What leaves       every surface through which indexed content leaves the
                    database. All of them are local files or streams; atlas
                    makes no network calls of any kind.

  What is exposed   credentials found in the stored text. Source contains
                    them more often than anyone admits, and a hardcoded
                    connection string lands in sql_operations.sql_text as
                    query text. Run ` + "`atlas security redact`" + ` to replace them.

Nothing here is printed that would repeat a leak: findings carry the rule,
the location and a digest, never the credential.

See docs/security.md for the written statement this command is derived from.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSecurity(cmd, exportVerb)
		},
	}
	cmd.Flags().StringVar(&exportVerb, "export", "",
		"restrict the export section to one verb, e.g. --export 'report sarif'")
	cmd.AddCommand(newSecurityRedactCmd())
	return cmd
}

// newSecurityRedactCmd implements `atlas security redact`.
func newSecurityRedactCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "redact",
		Short: "Replace credentials found in the state database with placeholders",
		Long: `redact rewrites the stored values that contain a credential, replacing
each one with a placeholder naming the rule that fired and a digest of what
was removed.

Only free-text columns are rewritten -- query text, branch conditions,
annotation values, snapshot blobs. A credential that ended up in a symbol
name or a file path is reported and left alone: rewriting it would change
what the index means rather than what it discloses, and the credential is
still in the source file either way.

Redaction is conservative by design and it can still be wrong, so every
replacement is reported: the column, the row, the rule and the digest. Run
with --dry-run first to see what would change.

This does NOT remove the credential from your repository. Rotate it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSecurityRedact(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"report what would be replaced without writing to the database")
	return cmd
}

// egressStatement is the machine-readable form of the claim docs/security.md
// leads with. The booleans are separate from the prose so a consumer can
// gate on them without parsing a sentence, and so a future release that
// starts transmitting cannot quietly reword the sentence and keep the shape.
type egressStatement struct {
	NetworkCalls bool   `json:"network_calls"`
	Telemetry    bool   `json:"telemetry"`
	UpdateChecks bool   `json:"update_checks"`
	CrashReports bool   `json:"crash_reports"`
	Statement    string `json:"statement"`
	EnforcedBy   string `json:"enforced_by"`
}

const egressSentence = "nothing leaves this machine: atlas makes no network calls, " +
	"sends no telemetry, checks for no updates and reports no crashes"

const egressEnforcement = "packages/redact/egress_test.go walks the import graph of the " +
	"atlas binary and fails if any first-party package reaches network code; " +
	"the check does not audit third-party dependencies"

func currentEgress() egressStatement {
	return egressStatement{
		Statement:  egressSentence,
		EnforcedBy: egressEnforcement,
	}
}

// securityResult is the JSON payload for `atlas security`.
type securityResult struct {
	Store             redact.Inventory   `json:"store"`
	Egress            egressStatement    `json:"egress"`
	Exports           []redact.Export    `json:"exports"`
	SourceTextColumns []redact.Column    `json:"source_text_columns"`
	Secrets           redact.SweepReport `json:"secrets"`
}

// securityRedactResult is the JSON payload for `atlas security redact`.
type securityRedactResult struct {
	Path   string             `json:"path"`
	DryRun bool               `json:"dry_run"`
	Sweep  redact.SweepReport `json:"sweep"`
}

// openStateDB migrates the state database (via the store, so the schema is
// exactly the one every other verb uses) and then hands back a raw handle.
//
// Raw SQL rather than the store's typed ports is the point of this command,
// not a shortcut around it. The ports expose the tables atlas reads for its
// own features; a security inventory has to enumerate what is THERE,
// including a table nobody wrote a port for yet, or it will keep reporting
// completeness it does not have.
//
// Opening an absent database creates and migrates an empty one, matching
// `atlas mcp`. Answering "this store is empty" is more useful than refusing
// to answer.
func openStateDB(ctx context.Context) (*sql.DB, string, error) {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return nil, "", err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, "", fmt.Errorf("security: open store %s: %w", dbPath, err)
	}
	if err := s.Close(); err != nil {
		return nil, "", fmt.Errorf("security: close store %s: %w", dbPath, err)
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "", fmt.Errorf("security: open %s: %w", dbPath, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("security: ping %s: %w", dbPath, err)
	}
	return db, dbPath, nil
}

func runSecurity(cmd *cobra.Command, exportVerb string) error {
	ctx := cmdContext(cmd)
	exports, err := resolveExports(cmd, exportVerb)
	if err != nil {
		return err
	}
	db, dbPath, err := openStateDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	inv, err := redact.Take(ctx, db, dbPath)
	if err != nil {
		return fmt.Errorf("security: inventory: %w", err)
	}
	sweep, err := redact.Sweep(ctx, db, redact.SweepOptions{})
	if err != nil {
		return fmt.Errorf("security: sweep: %w", err)
	}

	res := securityResult{
		Store:             inv,
		Egress:            currentEgress(),
		Exports:           exports,
		SourceTextColumns: inv.SourceTextColumns(),
		Secrets:           sweep,
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "security",
			map[string]any{"export": exportVerb}, res, securityWarnings(res))
	}
	printSecurity(stdoutOrJSON(cmd), res)
	return nil
}

// resolveExports applies --export, distinguishing a verb that has no
// dedicated artifact from one that does not exist.
//
// The distinction matters more than it looks: a typo silently answering
// "this command discloses nothing" is the exact failure this command was
// built to stop happening in prose.
func resolveExports(cmd *cobra.Command, verb string) ([]redact.Export, error) {
	if verb == "" {
		return redact.Exports(), nil
	}
	path := strings.Fields(verb)
	found, _, err := cmd.Root().Find(path)
	if err != nil || found.CommandPath() != "atlas "+strings.Join(path, " ") {
		return nil, fmt.Errorf("security: --export %q is not an atlas command", verb)
	}
	matched := redact.ExportsFor(verb)
	if len(matched) == 0 {
		// Not an error: the verb is real, it just has no artifact of its
		// own. The cross-cutting surfaces still apply to it, so return them
		// rather than an empty list that reads as "discloses nothing".
		for _, e := range redact.Exports() {
			if e.Verb == "" {
				matched = append(matched, e)
			}
		}
	}
	return matched, nil
}

// securityWarnings surfaces, in the JSON envelope, the two conditions a
// consumer must not have to infer from counts.
func securityWarnings(res securityResult) []string {
	var out []string
	if n := len(res.Store.Unclassified); n > 0 {
		out = append(out, fmt.Sprintf(
			"%d table(s)/column(s) in this database are not described by atlas: %s. "+
				"The inventory above is incomplete.",
			n, strings.Join(res.Store.Unclassified, ", ")))
	}
	if n := len(res.Secrets.Hits); n > 0 {
		out = append(out, fmt.Sprintf(
			"%d credential(s) are stored in this database; run `atlas security redact`, "+
				"and rotate them at the source", n))
	}
	return out
}

func runSecurityRedact(cmd *cobra.Command, dryRun bool) error {
	ctx := cmdContext(cmd)
	db, dbPath, err := openStateDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	sweep, err := redact.Sweep(ctx, db, redact.SweepOptions{Apply: !dryRun})
	if err != nil {
		return fmt.Errorf("security redact: %w", err)
	}
	res := securityRedactResult{Path: dbPath, DryRun: dryRun, Sweep: sweep}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "security.redact",
			map[string]any{"dry_run": dryRun}, res, nil)
	}
	printSecurityRedact(stdoutOrJSON(cmd), res)
	return nil
}

// ---- rendering ---------------------------------------------------------

func printSecurity(w io.Writer, res securityResult) {
	printSecurityStore(w, res.Store)
	printSecuritySourceText(w, res.SourceTextColumns)
	printSecurityEgress(w, res.Egress, res.Exports)
	printSecuritySecrets(w, res.Secrets)
}

func printSecurityStore(w io.Writer, inv redact.Inventory) {
	fmt.Fprintf(w, "STATE DATABASE\n")
	fmt.Fprintf(w, "  path            %s\n", inv.Path)
	fmt.Fprintf(w, "  size            %s", humanBytes(inv.SizeBytes))
	if inv.SidecarBytes > 0 {
		// -wal and -shm, named rather than folded into one figure: a copy of
		// atlas.db taken without its -wal can be a stale database, and the
		// size a person quotes should not hide that.
		fmt.Fprintf(w, " + %s in sidecar files (-wal, -shm)", humanBytes(inv.SidecarBytes))
	}
	fmt.Fprintf(w, "\n  schema version  %d\n\n", inv.SchemaVersion)

	nonEmpty := inv.NonEmptyTables()
	if len(nonEmpty) == 0 {
		fmt.Fprintf(w, "  Every table is empty. Run `atlas scan` to populate the index.\n\n")
		return
	}
	fmt.Fprintf(w, "  %8s  %-26s %s\n", "ROWS", "TABLE", "HOLDS")
	for _, t := range nonEmpty {
		fmt.Fprintf(w, "  %8d  %-26s %s\n", t.Rows, t.Name, tableHolds(t.Classes))
	}
	if empty := len(inv.Tables) - len(nonEmpty); empty > 0 {
		fmt.Fprintf(w, "  (%d further table(s) hold no rows)\n", empty)
	}
	if len(inv.Unclassified) > 0 {
		fmt.Fprintf(w, "\n  NOT DESCRIBED BY ATLAS: %s\n"+
			"  This inventory is incomplete; treat the unlisted items as unknown content.\n",
			strings.Join(inv.Unclassified, ", "))
	}
	fmt.Fprintln(w)
}

func printSecuritySourceText(w io.Writer, cols []redact.Column) {
	fmt.Fprintf(w, "VERBATIM SOURCE TEXT\n")
	fmt.Fprintf(w, "  This database is not metadata about your code. Alongside file paths\n"+
		"  and symbol names it stores text copied verbatim out of your repository,\n"+
		"  including -- in a snapshot -- every symbol's doc comment and signature.\n"+
		"  Anyone who receives the file receives all of it.\n\n")
	for _, c := range cols {
		fmt.Fprintf(w, "    %-34s %s\n", c.Table+"."+c.Name, c.Holds)
	}
	fmt.Fprintln(w)
}

func printSecurityEgress(w io.Writer, e egressStatement, exports []redact.Export) {
	fmt.Fprintf(w, "EGRESS\n")
	fmt.Fprintf(w, "  %s.\n", wrapIndent(e.Statement, 2, 72))
	fmt.Fprintf(w, "  %s.\n\n", wrapIndent("Enforced by: "+e.EnforcedBy, 2, 72))
	fmt.Fprintf(w, "  Surfaces that put indexed content somewhere else. Every destination\n"+
		"  is local; what happens to the file afterwards is your decision:\n\n")
	for _, x := range exports {
		verb := x.Verb
		if verb == "" {
			verb = "(every verb)"
		} else {
			verb = "atlas " + verb
		}
		fmt.Fprintf(w, "    %s -- %s\n", verb, x.Surface)
		fmt.Fprintf(w, "        to      %s\n", x.Destination)
		fmt.Fprintf(w, "        carries %s\n", joinClasses(x.Classes))
		fmt.Fprintf(w, "        %s\n\n", wrapIndent(x.Note, 8, 68))
	}
}

func printSecuritySecrets(w io.Writer, s redact.SweepReport) {
	fmt.Fprintf(w, "SECRETS\n")
	fmt.Fprintf(w, "  swept %d column(s) over %d value(s)\n", s.ColumnsRead, s.RowsRead)
	if s.Clean() {
		fmt.Fprintf(w, "  none detected.\n")
		return
	}
	fmt.Fprintf(w, "  %d finding(s):\n\n", len(s.Hits))
	for _, h := range s.Hits {
		fmt.Fprintf(w, "    %s.%s  row %s\n", h.Table, h.Column, h.Row)
		fmt.Fprintf(w, "        %s  %d bytes  digest %s\n", h.Kind, h.Length, h.Digest)
		if h.Context != "" {
			fmt.Fprintf(w, "        context %s\n", h.Context)
		}
		if !h.Redactable {
			fmt.Fprintf(w, "        NOT REDACTABLE: rewriting this column would change what\n"+
				"        the index means. Fix it at the source.\n")
		}
		fmt.Fprintln(w)
	}
	if s.Unredactable < len(s.Hits) {
		fmt.Fprintf(w, "  Run `atlas security redact` to replace the redactable ones.\n")
	}
	fmt.Fprintf(w, "  Redaction does not remove anything from your repository. Rotate them.\n")
}

func printSecurityRedact(w io.Writer, res securityRedactResult) {
	mode := "redacted"
	if res.DryRun {
		mode = "would redact"
	}
	fmt.Fprintf(w, "atlas security redact  %s\n", res.Path)
	fmt.Fprintf(w, "  swept %d column(s) over %d value(s)\n",
		res.Sweep.ColumnsRead, res.Sweep.RowsRead)
	if res.Sweep.Clean() {
		fmt.Fprintf(w, "  nothing to redact.\n")
		return
	}
	for _, h := range res.Sweep.Hits {
		verb := mode
		if !h.Redactable {
			verb = "left alone"
		}
		fmt.Fprintf(w, "  %-12s %s.%s row %s  %s digest %s\n",
			verb, h.Table, h.Column, h.Row, h.Kind, h.Digest)
	}
	fmt.Fprintf(w, "  %d distinct value(s) %s; %d finding(s) in columns atlas will not rewrite.\n",
		res.Sweep.ValuesRewritten, mode, res.Sweep.Unredactable)
	fmt.Fprintf(w, "  This did not touch your repository. Rotate the credentials.\n")
}

// ---- small formatting helpers -----------------------------------------

// tableHolds renders a table's content classes. A table with no TEXT column
// says so, rather than reading as a table that holds nothing at all: it
// still holds ids, counts and timestamps, none of which can carry text out
// of the repository.
func tableHolds(cs []redact.Class) string {
	if len(cs) == 0 {
		return "no text columns (ids, counts, timestamps)"
	}
	return joinClasses(cs)
}

func joinClasses(cs []redact.Class) string {
	if len(cs) == 0 {
		return "nothing from the index"
	}
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// humanBytes formats a byte count for a person, keeping the exact figure for
// small files where a rounded "0.1 MiB" would hide the difference between an
// empty store and a populated one.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// wrapIndent soft-wraps a note to width columns, indenting continuation
// lines by indent spaces.
func wrapIndent(s string, indent, width int) string {
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	col := 0
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			b.WriteString(word)
			col = len(word)
		case col+1+len(word) > width:
			b.WriteString("\n" + pad + word)
			col = len(word)
		default:
			b.WriteString(" " + word)
			col += 1 + len(word)
		}
	}
	return b.String()
}
