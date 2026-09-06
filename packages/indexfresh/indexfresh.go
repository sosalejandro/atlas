// Package indexfresh answers one question that several commands silently got
// wrong: are the symbol spans atlas holds for a file still true of that file?
//
// The commands that map a git diff onto symbols -- `cov diff` (#89) and
// `affected` (#90) -- join two things measured at different times. The line
// numbers come from the working tree at HEAD; the [line, end_line] spans come
// from whenever `atlas scan` last ran. Those are only comparable if the index
// was built at HEAD.
//
// When they disagree the failure is silent and directional. Insert twenty lines
// near the top of a file, and every symbol below shifts down by twenty in the
// working tree while the stored spans stay put. A changed line then lands
// inside whichever symbol used to occupy that range -- so `cov diff` scores the
// wrong function's coverage and calls it patch coverage, and `affected` selects
// the tests of a function the diff never touched while omitting the tests of
// the one it did. Both report a confident number. Neither is measuring what it
// says.
//
// Detecting it is cheap: file_hashes already stores the content hash the
// scanner saw. Re-hash the handful of files in the diff and compare. This
// package exists so both commands ask the same question and get the same
// answer, rather than each growing its own half-correct version.
package indexfresh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// State is how much the caller may trust a file's stored symbol spans.
type State string

const (
	// StateCurrent means the file on disk hashes to what the scanner
	// recorded, so its spans place symbols exactly where they still are.
	StateCurrent State = "current"

	// StateStale means the file changed since the scan. The spans are for a
	// version of the file that no longer exists, and any line-number join
	// against them is meaningless -- not approximate, meaningless, because a
	// single inserted line at the top invalidates every span below it.
	StateStale State = "stale"

	// StateAbsent means atlas has no hash row for the file: never scanned,
	// excluded by config, or generated (#96). There are no spans to be wrong
	// about, which is a different situation from having wrong ones.
	StateAbsent State = "absent"

	// StateUnreadable means the file could not be hashed for a reason other
	// than not existing -- a permission error, a device error. Distinct from
	// StateAbsent because it says nothing about whether the file is indexed,
	// only that this check could not run.
	StateUnreadable State = "unreadable"

	// StateDeleted means the file is gone from the working tree but atlas
	// still holds a hash row for it. Its spans describe nothing.
	StateDeleted State = "deleted"
)

// Trustworthy reports whether spans for a file in this state may be joined
// against working-tree line numbers. Only StateCurrent qualifies: every other
// state is a reason to widen or refuse, never to proceed quietly.
func (s State) Trustworthy() bool { return s == StateCurrent }

// Report is the classification of one batch of paths.
type Report struct {
	// States maps each requested repo-relative path to its state.
	States map[string]State `json:"states"`
	// Stale, Absent, Deleted and Unreadable are the paths in each
	// non-trustworthy state, sorted. They are broken out because the caller's
	// response differs per state and because the user needs the list, not
	// just a count -- "3 files are stale" is not actionable.
	Stale      []string `json:"stale,omitempty"`
	Absent     []string `json:"absent,omitempty"`
	Deleted    []string `json:"deleted,omitempty"`
	Unreadable []string `json:"unreadable,omitempty"`
}

// AllCurrent reports whether every classified path is safe to join against.
func (r Report) AllCurrent() bool {
	return len(r.Stale) == 0 && len(r.Deleted) == 0 && len(r.Unreadable) == 0
}

// Untrustworthy returns every path whose spans must not be joined against
// working-tree line numbers, sorted. StateAbsent is included: a file with no
// spans cannot be attributed either, though for the opposite reason.
func (r Report) Untrustworthy() []string {
	out := make([]string, 0, len(r.Stale)+len(r.Absent)+len(r.Deleted)+len(r.Unreadable))
	out = append(out, r.Stale...)
	out = append(out, r.Absent...)
	out = append(out, r.Deleted...)
	out = append(out, r.Unreadable...)
	sort.Strings(out)
	return out
}

// Classify hashes each path under root and compares it to what the scanner
// recorded.
//
// Paths are repo-relative, matching what git reports and what the store holds.
// A path outside root is rejected rather than resolved, so a crafted diff
// cannot make this read arbitrary files.
func Classify(ctx context.Context, hashes store.FileHashes, root string, paths []string) (Report, error) {
	rep := Report{States: make(map[string]State, len(paths))}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("indexfresh: resolve root %s: %w", root, err)
	}

	for _, p := range dedupe(paths) {
		state, err := classifyOne(ctx, hashes, absRoot, p)
		if err != nil {
			return Report{}, err
		}
		rep.States[p] = state
		switch state {
		case StateStale:
			rep.Stale = append(rep.Stale, p)
		case StateAbsent:
			rep.Absent = append(rep.Absent, p)
		case StateDeleted:
			rep.Deleted = append(rep.Deleted, p)
		case StateUnreadable:
			rep.Unreadable = append(rep.Unreadable, p)
		case StateCurrent:
		}
	}
	sort.Strings(rep.Stale)
	sort.Strings(rep.Absent)
	sort.Strings(rep.Deleted)
	sort.Strings(rep.Unreadable)
	return rep, nil
}

func classifyOne(ctx context.Context, hashes store.FileHashes, absRoot, rel string) (State, error) {
	abs, ok := safeJoin(absRoot, rel)
	if !ok {
		// A path that escapes the root is not classifiable, and following it
		// would mean hashing a file outside the repo on the say-so of a diff.
		return StateUnreadable, nil
	}

	row, err := hashes.Get(ctx, rel)
	indexed := true
	switch {
	case errors.Is(err, shared.ErrNotFound):
		indexed = false
	case err != nil:
		return "", fmt.Errorf("indexfresh: read hash row for %s: %w", rel, err)
	}

	sum, err := hashFile(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if indexed {
			return StateDeleted, nil
		}
		// Not on disk and not indexed: the file the diff named was deleted by
		// the diff itself. Nothing to be stale about.
		return StateAbsent, nil
	case err != nil:
		return StateUnreadable, nil
	}

	if !indexed {
		// Also the --hash-files=false case: that scan indexes symbols but
		// writes no hash rows at all, so every file classifies absent and both
		// callers degrade to their fallback. That is the safe direction -- the
		// alternative is trusting spans nothing can corroborate.
		return StateAbsent, nil
	}
	// The store rejects an empty content_hash on write, so an indexed file
	// always has a digest to compare against.
	if row.ContentHash != sum {
		return StateStale, nil
	}
	return StateCurrent, nil
}

// safeJoin resolves rel under absRoot and reports whether the result stayed
// inside it. filepath.Join alone would happily resolve "../../etc/passwd".
func safeJoin(absRoot, rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	joined := filepath.Join(absRoot, rel)
	if joined != absRoot && !hasPrefixPath(joined, absRoot) {
		return "", false
	}
	return joined, true
}

func hasPrefixPath(p, prefix string) bool {
	if len(p) <= len(prefix) {
		return false
	}
	return p[:len(prefix)] == prefix && p[len(prefix)] == filepath.Separator
}

// hashFile mirrors the scanner's digest exactly: SHA-256 of the file bytes,
// hex-encoded. It must stay byte-identical to codeindex.hashFile, or every
// file classifies as stale and both callers degrade to their fallback forever.
func hashFile(abs string) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		// Wrapped with %w so the caller's errors.Is(err, os.ErrNotExist) still
		// distinguishes "deleted from the tree" from "could not be read".
		return "", fmt.Errorf("hash %s: %w", abs, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", abs, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
