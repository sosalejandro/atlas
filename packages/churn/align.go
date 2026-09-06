package churn

import (
	"path"
	"strings"
)

// AlignTo answers whether paths are in the same namespace as this report,
// and if not, which sub-directory of the mined repository maps them there.
//
// The two namespaces are NOT the same by construction. Churn is mined at
// the git top level, so its paths are relative to that; the paths a caller
// rolls up are relative to whatever root the code was indexed from, which
// `atlas scan --root <subdir>` makes a different directory. Joining the two
// by string equality then misses every single file, and the roll-up reports
// "unknown churn" for the whole repository — a number-shaped answer to a
// question git was never asked.
//
// The three outcomes are deliberately distinct:
//
//   - ("", true): at least one path is already tracked, so the namespaces
//     coincide and nothing needs rebasing.
//   - (prefix, true): no path joins directly, but adding this one
//     sub-directory joins more of them than any other candidate does. That
//     is a mapping we can check, not a guess.
//   - ("", false): cannot determine. Either the paths are genuinely
//     untracked (brand-new files) or two sub-directories fit equally well.
//     Callers must say so rather than pick one.
func (r *Report) AlignTo(paths []string) (string, bool) {
	if len(r.tracked) == 0 || len(paths) == 0 {
		return "", false
	}
	byBase := r.trackedByBase()
	votes := map[string]int{}
	for _, p := range paths {
		p = strings.TrimPrefix(p, "./")
		if p == "" {
			continue
		}
		if r.tracked[p] {
			// One direct join is proof the namespaces already agree; a
			// path that misses is then simply an untracked file.
			return "", true
		}
		for _, t := range byBase[path.Base(p)] {
			if pre, ok := strings.CutSuffix(t, "/"+p); ok && pre != "" {
				votes[pre]++
			}
		}
	}
	return pickPrefix(votes)
}

// trackedByBase indexes the tracked set by base name so AlignTo costs one
// pass over it plus a short lookup per path, rather than a suffix scan of
// every tracked file for every path.
func (r *Report) trackedByBase() map[string][]string {
	out := make(map[string][]string, len(r.tracked))
	for t := range r.tracked {
		b := path.Base(t)
		out[b] = append(out[b], t)
	}
	return out
}

// pickPrefix returns the single best-supported candidate prefix.
//
// A tie is reported as "cannot determine" rather than resolved: two
// sub-directories that fit the observed paths equally well are two
// different answers, and picking either would attribute one directory's
// commit history to another directory's code.
func pickPrefix(votes map[string]int) (string, bool) {
	best, count, ties := "", 0, 0
	for pre, n := range votes {
		switch {
		case n > count:
			best, count, ties = pre, n, 1
		case n == count:
			ties++
		}
	}
	if count == 0 || ties != 1 {
		return "", false
	}
	return best, true
}

// Rebase re-expresses the report relative to a sub-directory of the mined
// repository, so its paths join a caller whose paths are relative to that
// sub-directory (see AlignTo).
//
// Files outside the sub-directory are dropped: they cannot be named in the
// caller's namespace at all, and keeping them would let a path collide with
// an unrelated file of the same relative name. The commit counters are left
// alone — they describe the mining pass, which really did read the whole
// repository.
func (r *Report) Rebase(prefix string) *Report {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return r
	}
	pre := prefix + "/"
	out := &Report{
		Window:                r.Window,
		HalfLife:              r.HalfLife,
		Shallow:               r.Shallow,
		CommitsScanned:        r.CommitsScanned,
		CommitsSkippedBulk:    r.CommitsSkippedBulk,
		CommitsSkippedMessage: r.CommitsSkippedMessage,
		Files:                 make(map[string]FileChurn, len(r.Files)),
		Warnings:              append([]string(nil), r.Warnings...),
		tracked:               make(map[string]bool, len(r.tracked)),
		unknownScore:          r.unknownScore,
	}
	for p, fc := range r.Files {
		if rest, ok := strings.CutPrefix(p, pre); ok {
			fc.Path = rest
			out.Files[rest] = fc
		}
	}
	for t := range r.tracked {
		if rest, ok := strings.CutPrefix(t, pre); ok {
			out.tracked[rest] = true
		}
	}
	return out
}
