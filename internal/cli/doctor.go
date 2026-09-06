package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/doctor"
	"github.com/sosalejandro/atlas/packages/store"
)

// newDoctorCmd implements `atlas doctor` — the self-check that reports
// whether atlas's picture of the repo is still true.
func newDoctorCmd() *cobra.Command {
	var (
		failOn string
		root   string
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report whether atlas's picture of this repo is still true",
		Long: `doctor answers the one question no other atlas command can: is the
state atlas is answering from still about the code on disk?

Every number atlas prints -- a coverage percentage, an audit score, a
sprint ranking -- is downstream of a scan and an ingest, and both go
stale silently. A stale index does not error; it answers confidently
about a repo that no longer exists.

Every check is reported, passing ones included, so a clean run tells you
what was examined rather than nothing at all. A check that cannot run
(no coverage ingested yet, no features annotated yet) reports "n/a" with
the reason -- never "ok".

The exit code is the CI contract: zero unless some check reached the
--fail-on severity. --fail-on warn tightens the gate without changing
which checks run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, root, failOn)
		},
	}
	cmd.Flags().StringVar(&failOn, "fail-on", string(doctor.SeverityFail),
		`exit non-zero when any check reaches this severity ("warn" or "fail")`)
	cmd.Flags().StringVar(&root, "root", "",
		"working tree to compare the index against (default: repo root or cwd)")
	return cmd
}

func runDoctor(cmd *cobra.Command, rootArg, failOn string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	threshold, err := doctor.ParseSeverity(failOn)
	if err != nil {
		return fmt.Errorf("doctor: --fail-on: %w", err)
	}
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}

	// A store that will not open is NOT an early return. It is the single
	// most useful moment for this command: every other atlas verb dies
	// here in a wall of golang-migrate output, and doctor's job is to turn
	// that into "migration 11 is dirty, delete the cache and re-init".
	// The schema check reads the database file directly; the rest report
	// not-applicable naming this error.
	//
	// A missing file is reported, never opened. store.Open would create
	// and migrate an empty database, and a diagnostic that conjures the
	// state it was asked to inspect would report a repo as initialised
	// for the sole reason that someone asked whether it was.
	//
	// Existence alone is not enough of a guard, though: store.Open
	// migrates whatever it is handed, so a 0-byte placeholder, a database
	// an interrupted `atlas init` left half-made, or an unrelated SQLite
	// file at this path would all be turned INTO an atlas store by the
	// command asked whether they were one. doctor.IsAtlasStore answers
	// that from a read-only handle first; only a file that is already
	// ours is opened read-write.
	var (
		s       *store.Store
		openErr error
	)
	if _, statErr := os.Stat(dbPath); statErr != nil {
		openErr = fmt.Errorf("no state database at %s: %w", dbPath, statErr)
	} else if openErr = doctor.IsAtlasStore(ctx, dbPath); openErr == nil {
		if s, openErr = store.Open(ctx, dbPath); s != nil {
			defer func() { _ = s.Close() }()
		}
	}

	env := &doctor.Env{
		Store:          s,
		StoreErr:       openErr,
		DBPath:         dbPath,
		Root:           rootDir,
		SkipDirs:       loaded.Scan.SkipDirs,
		GeneratedGlobs: loaded.Scan.Generated,
	}
	report, err := doctor.Run(ctx, env, doctor.DefaultChecks())
	if err != nil {
		return fmt.Errorf("doctor: %w", err)
	}

	if flags.JSON {
		if err := emitJSON(stdoutOrJSON(cmd), "doctor",
			map[string]any{"root": rootDir, "db_path": dbPath, "fail_on": string(threshold)},
			report, nil); err != nil {
			return err
		}
	} else {
		printDoctorText(stdoutOrJSON(cmd), report, rootDir, dbPath, flags.Verbose)
	}

	// The gate is the last thing that happens, so the report is always on
	// stdout before the process exits non-zero. A CI job that only sees
	// the exit code learns nothing; one that sees the report can act.
	if report.TrippedBy(threshold) {
		return fmt.Errorf("doctor: %d check(s) at or above %q severity", tripped(report, threshold), threshold)
	}
	return nil
}

// tripped counts the checks that reached the gate, for the exit message.
func tripped(r doctor.Report, threshold doctor.Severity) int {
	n := 0
	for _, c := range r.Checks {
		if c.Severity.AtLeast(threshold) {
			n++
		}
	}
	return n
}

func printDoctorText(w io.Writer, r doctor.Report, root, dbPath string, verbose bool) {
	fmt.Fprintf(w, "atlas doctor — %s (db: %s)\n\n", root, dbPath)
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %-6s %s\n", "["+string(c.Severity)+"]", c.Name)
		fmt.Fprintf(w, "        examines: %s\n", c.Examines)
		fmt.Fprintf(w, "        %s\n", c.Finding)
		if c.Remediation != "" {
			fmt.Fprintf(w, "        fix: %s\n", c.Remediation)
		}
		if verbose {
			printDoctorDetails(w, c.Details)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  %d ok, %d warn, %d fail, %d n/a — worst: %s\n",
		r.Counts.OK, r.Counts.Warn, r.Counts.Fail, r.Counts.NotApplicable, r.Worst)
}

// printDoctorDetails renders the numbers behind a finding under -v, in
// sorted key order so two runs over the same state print identically.
func printDoctorDetails(w io.Writer, details map[string]any) {
	if len(details) == 0 {
		return
	}
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if list, ok := details[k].([]string); ok {
			if len(list) == 0 {
				continue
			}
			fmt.Fprintf(w, "        %s: %s\n", k, strings.Join(list, ", "))
			continue
		}
		fmt.Fprintf(w, "        %s: %v\n", k, details[k])
	}
}
