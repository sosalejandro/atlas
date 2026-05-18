package cli

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// newMigrateAnnotationsCmd implements `atlas migrate-annotations` — bulk
// rewrite of `// @testreg <id>` comments to `// @atlas:feature <id>`.
//
// --dry-run reports candidate annotations without touching disk.
// --apply rewrites in place.
func newMigrateAnnotationsCmd() *cobra.Command {
	var (
		dryRun bool
		apply  bool
		root   string
	)
	cmd := &cobra.Command{
		Use:   "migrate-annotations",
		Short: "Bulk rewrite @testreg → @atlas:feature across the project",
		Long: `migrate-annotations walks every source file under --root and rewrites
each '// @testreg <id> [#tag ...]' comment in place to the canonical
'// @atlas:feature <id> [tag ...]' form, preserving trailing tags
verbatim (the leading '#' is dropped).

The verb refuses to touch:

  - files inside vendor/ or node_modules/
  - files that contain the magic suppressor '// nolint:atlas-migrate'

You MUST pass exactly one of --dry-run or --apply. The default is a
no-op so a CI script that forgot to pick a mode fails loud.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dryRun == apply {
				return fmt.Errorf("migrate-annotations: exactly one of --dry-run or --apply is required")
			}
			rootDir := root
			if rootDir == "" {
				rootDir = loaded.repoRoot
			}
			return runMigrateAnnotations(cmd, rootDir, apply)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"report rewrite candidates without modifying any file")
	cmd.Flags().BoolVar(&apply, "apply", false,
		"rewrite candidates in place")
	cmd.Flags().StringVar(&root, "root", "",
		"project root to walk (default: repo root or cwd)")
	return cmd
}

// migrateAnnotationsResult is the JSON payload for `atlas migrate-annotations`.
type migrateAnnotationsResult struct {
	FilesScanned    int                        `json:"files_scanned"`
	FilesTouched    int                        `json:"files_touched"`
	Candidates      int                        `json:"candidates"`
	Rewrites        []migrateAnnotationRewrite `json:"rewrites,omitempty"`
	Mode            string                     `json:"mode"` // "dry-run" | "apply"
}

// migrateAnnotationRewrite records one line where a testreg annotation
// was found.
type migrateAnnotationRewrite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// migrateAnnotationsRE matches `@testreg <id> [<tag-list>]` with the
// leading comment open already stripped. We capture two groups: the id
// and everything that follows. Dashes are valid in ids per the Phase 6f
// PR #18 change.
var migrateAnnotationsRE = regexp.MustCompile(
	`^@testreg\s+([A-Za-z0-9._\-]+)(\s+.*)?$`,
)

// migrateAnnotationsExts is the closed set of extensions Atlas's annotation
// parser recognises (docs/annotations.md). Anything else is skipped.
var migrateAnnotationsExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true,
	".js": true, ".jsx": true, ".py": true,
}

// migrateAnnotationsSkipDirs are directories whose contents are never
// rewritten regardless of --apply.
var migrateAnnotationsSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
}

const migrateAnnotationsSuppressor = "// nolint:atlas-migrate"

func runMigrateAnnotations(cmd *cobra.Command, root string, apply bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	res := migrateAnnotationsResult{Mode: "dry-run"}
	if apply {
		res.Mode = "apply"
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("migrate-annotations: abs %q: %w", root, err)
	}

	err = filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permission errors on a directory shouldn't kill the whole walk;
			// surface them as warnings but keep going.
			if d != nil && d.IsDir() {
				fmt.Fprintf(cmd.ErrOrStderr(), "  warning: skip %s: %v\n", p, walkErr)
				return filepath.SkipDir
			}
			return walkErr //nolint:wrapcheck // bubble out for filepath.WalkDir.
		}
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // ctx error needs no extra context.
		}
		if d.IsDir() {
			name := d.Name()
			if migrateAnnotationsSkipDirs[name] || strings.HasPrefix(name, ".") && p != rootAbs {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !migrateAnnotationsExts[ext] {
			return nil
		}
		res.FilesScanned++

		touched, rewrites, err := processMigrateFile(p, rootAbs, apply)
		if err != nil {
			return err
		}
		if touched {
			res.FilesTouched++
		}
		res.Candidates += len(rewrites)
		res.Rewrites = append(res.Rewrites, rewrites...)
		return nil
	})
	if err != nil {
		return fmt.Errorf("migrate-annotations walk: %w", err)
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "migrate-annotations",
			map[string]any{"root": rootAbs, "apply": apply}, res, nil)
	}
	printMigrateAnnotationsText(cmd, res)
	return nil
}

// processMigrateFile reads one file, finds every @testreg line, and either
// (apply==true) rewrites the file in place, or (apply==false) just
// returns the list of rewrites it would have made.
//
// Files containing `// nolint:atlas-migrate` are skipped entirely — that
// is the documented escape hatch for code that intentionally keeps the
// legacy grammar.
func processMigrateFile(absPath, root string, apply bool) (bool, []migrateAnnotationRewrite, error) {
	rel := absPath
	if r, err := filepath.Rel(root, absPath); err == nil {
		rel = filepath.ToSlash(r)
	}

	b, err := os.ReadFile(absPath)
	if err != nil {
		return false, nil, fmt.Errorf("read %s: %w", absPath, err)
	}
	if strings.Contains(string(b), migrateAnnotationsSuppressor) {
		return false, nil, nil
	}

	out, rewrites := rewriteMigrateAnnotations(b, rel)
	if len(rewrites) == 0 {
		return false, nil, nil
	}
	if !apply {
		return false, rewrites, nil
	}
	// Preserve original file mode.
	info, err := os.Stat(absPath)
	if err != nil {
		return false, nil, fmt.Errorf("stat %s: %w", absPath, err)
	}
	if err := os.WriteFile(absPath, out, info.Mode()); err != nil {
		return false, nil, fmt.Errorf("write %s: %w", absPath, err)
	}
	return true, rewrites, nil
}

// rewriteMigrateAnnotations is the pure rewrite engine, separated so it
// can be unit-tested without touching disk.
//
// It scans line by line, identifies any `// @testreg <id> [...]` payload
// embedded in the line (`@testreg` can appear after arbitrary leading
// whitespace + the `//` / `#` comment open), and rewrites the `@testreg`
// keyword to `@atlas:feature` plus drops `#` from each tag.
func rewriteMigrateAnnotations(src []byte, relPath string) ([]byte, []migrateAnnotationRewrite) {
	scanner := bufio.NewScanner(strings.NewReader(string(src)))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var out strings.Builder
	var rewrites []migrateAnnotationRewrite
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		newLine, rewritten := rewriteLine(line)
		if rewritten {
			rewrites = append(rewrites, migrateAnnotationRewrite{
				File:   relPath,
				Line:   lineNum,
				Before: line,
				After:  newLine,
			})
		}
		out.WriteString(newLine)
		out.WriteByte('\n')
	}
	// Preserve original final-newline status: if the input did NOT end in
	// '\n', trim the trailing newline we just appended.
	res := out.String()
	if len(src) > 0 && src[len(src)-1] != '\n' && strings.HasSuffix(res, "\n") {
		res = strings.TrimSuffix(res, "\n")
	}
	return []byte(res), rewrites
}

// rewriteLine returns the rewritten form of one source line. Returns
// (line, false) when no rewrite applies.
//
// The rewrite finds the FIRST `@testreg ` occurrence after a comment
// marker (`//`, `/*`, `#`) on the line. Trailing tags shed their `#`
// prefix per docs/annotations.md (@atlas tags are unprefixed).
func rewriteLine(line string) (string, bool) {
	// Find a comment marker.
	idx := commentOpenIndex(line)
	if idx < 0 {
		return line, false
	}
	rest := strings.TrimLeftFunc(line[idx:], func(r rune) bool {
		return r == '/' || r == '*' || r == '#' || r == ' ' || r == '\t'
	})
	m := migrateAnnotationsRE.FindStringSubmatch(rest)
	if m == nil {
		return line, false
	}
	id := m[1]
	tail := ""
	if len(m) > 2 {
		tail = strings.TrimSpace(m[2])
	}
	// Drop '#' prefixes from each tag, preserve order + spacing.
	if tail != "" {
		parts := strings.Fields(tail)
		for i, p := range parts {
			parts[i] = strings.TrimPrefix(p, "#")
		}
		tail = " " + strings.Join(parts, " ")
	}
	// Reassemble: take the original prefix (everything up to and
	// including the comment open marker + intervening whitespace), then
	// emit the new annotation form. We preserve the leading whitespace
	// + comment open verbatim to keep diff churn minimal.
	openLen := commentOpenLength(line[idx:])
	prefix := line[:idx+openLen]
	// Maintain a single space between prefix and the annotation token.
	if !strings.HasSuffix(prefix, " ") {
		prefix += " "
	}
	newLine := prefix + "@atlas:feature " + id + tail
	return newLine, true
}

// commentOpenIndex returns the byte offset of the first comment marker
// on the line (or -1 if none). Recognised markers: '//', '/*', '#'
// (the last one for Python; '#' inside Go strings is fine because the
// regex below requires '@testreg' immediately after the marker).
func commentOpenIndex(line string) int {
	bestIdx := -1
	for _, marker := range []string{"//", "/*", "#"} {
		if i := strings.Index(line, marker); i >= 0 {
			if bestIdx < 0 || i < bestIdx {
				bestIdx = i
			}
		}
	}
	return bestIdx
}

// commentOpenLength returns the length of the marker at the start of s.
// Assumes s starts at a marker index returned by commentOpenIndex.
func commentOpenLength(s string) int {
	switch {
	case strings.HasPrefix(s, "//"), strings.HasPrefix(s, "/*"):
		return 2
	case strings.HasPrefix(s, "#"):
		return 1
	}
	return 0
}

func printMigrateAnnotationsText(cmd *cobra.Command, res migrateAnnotationsResult) {
	fmt.Fprintf(cmd.OutOrStdout(),
		"migrate-annotations: %s mode\n  files_scanned=%d  files_touched=%d  candidates=%d\n",
		res.Mode, res.FilesScanned, res.FilesTouched, res.Candidates)
	for _, r := range res.Rewrites {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s:%d\n    - %s\n    + %s\n",
			r.File, r.Line, r.Before, r.After)
	}
}
