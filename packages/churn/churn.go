package churn

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// Churn status values. A caller that cannot tell "quiet" from "no history"
// apart will happily rank a file it knows nothing about as if it were dead,
// so the distinction is part of the public result rather than a comment.
const (
	StatusKnown   = "known"
	StatusUnknown = "unknown"
)

// Defaults for Options. Each is justified where it is used; the constants
// are exported because the CLI prints them as flag defaults and the docs
// quote them.
const (
	// DefaultWindow is how far back `git log` is asked to look. Twelve
	// months covers a full planning year, and past it the decay below has
	// already cut a commit's weight under 6% — mining further costs
	// subprocess time to move the ranking by nothing.
	DefaultWindow = 365 * 24 * time.Hour

	// DefaultHalfLife is one quarter. A quarter is the unit teams plan in,
	// so "counts half as much as this quarter's work" is a statement a
	// reader can check against their own calendar. It also sets the shape
	// of the curve we actually want: last month's commits stay near full
	// weight, last year's land near 1/16, and neither is erased.
	DefaultHalfLife = 90 * 24 * time.Hour

	// DefaultMaxFilesPerCommit is the bulk-change cut-off. Fifty is above
	// any hand-written change — a large refactor spanning fifty files is
	// rare and is genuinely fifty files' worth of risk — and below every
	// sweep: a gofmt pass, a licence header, a codegen refresh and a
	// dependency bump all touch hundreds. Set the option negative to
	// disable the rule.
	DefaultMaxFilesPerCommit = 50

	// DefaultSaturation is the half-saturation constant of the score
	// curve: a file with this many decay-weighted commits scores 50. Five
	// recent commits is a file under active work but not on fire, which is
	// the middle of the range the ranking has to spread.
	DefaultSaturation = 5.0

	// DefaultAuthorBonus is the most a file's score can be raised by author
	// diversity, as a fraction. Multiple authors mean contended, shared
	// code — a real risk multiplier — but a weaker signal than recency, so
	// it tops out at a quarter rather than competing with the decay. Set
	// the option negative to switch author diversity off.
	DefaultAuthorBonus = 0.25

	// DefaultUnknownScore is what churn is worth when git cannot answer.
	// The midpoint is the only honest value: zero would drop brand-new and
	// shallow-cloned code out of the backlog entirely, and 100 would put
	// it all at the top. Results carrying it are marked StatusUnknown so
	// nobody mistakes it for a measurement.
	DefaultUnknownScore = 50.0
)

// DefaultExcludeMessages are the commit-subject patterns dropped by
// default, matched case-insensitively against the subject line only.
//
// The first pattern names the conventional-commit types that are defined
// as not-a-behaviour-change: chore, build, style, and the revert of
// something already counted. The second names the formatter and codegen
// sweeps that arrive under any subject at all. Both are deliberately
// narrow — a filter that eats real work is worse than no filter, because
// the ranking then omits the very files the sweep was hiding.
var DefaultExcludeMessages = []string{
	`(?i)^\s*(chore|build|style|revert)(\([^)]*\))?!?:`,
	`(?i)\b(gofmt|goimports|prettier|clang-format|re-?format|licen[cs]e header)\b`,
}

// Options tunes a mining pass. The zero value is usable once Repo is set.
type Options struct {
	// Repo is the repository root `git` runs in. Required.
	Repo string

	// Window is how far back to mine. Default DefaultWindow.
	Window time.Duration

	// HalfLife is the age at which a commit counts half. Default
	// DefaultHalfLife.
	HalfLife time.Duration

	// MaxFilesPerCommit drops commits touching more files than this.
	// Zero means DefaultMaxFilesPerCommit; negative disables the rule.
	MaxFilesPerCommit int

	// ExcludeMessages are regexps matched against each commit subject.
	// A nil slice means DefaultExcludeMessages; a non-nil empty slice
	// disables message exclusion entirely. An unparseable pattern is an
	// error rather than a silently ignored filter — a filter the user
	// believes is running but is not would quietly change the ranking.
	ExcludeMessages []string

	// AuthorBonus is the maximum fractional score uplift from author
	// diversity. Zero means DefaultAuthorBonus; negative disables it.
	AuthorBonus float64

	// Saturation is the half-saturation constant of the score curve.
	// Zero means DefaultSaturation.
	Saturation float64

	// UnknownScore is the neutral score returned for files git has no
	// history for. Zero means DefaultUnknownScore; set it negative to ask
	// for a literal zero, which is a choice, not a default.
	UnknownScore float64

	// Now overrides time.Now for determinism. Zero = real time.
	Now func() time.Time

	// Git is the subprocess seam. Nil = NewGitRunner(Repo).
	Git GitRunner
}

// resolved is Options with every default filled in and the message
// patterns compiled. Keeping it separate means the scoring code never has
// to ask "is this field the zero value or a real setting?"
type resolved struct {
	window       time.Duration
	halfLife     time.Duration
	maxFiles     int
	exclude      []*regexp.Regexp
	authorBonus  float64
	saturation   float64
	unknownScore float64
	now          time.Time
	git          GitRunner
}

func (o Options) resolve() (resolved, error) {
	r := resolved{
		window:       o.Window,
		halfLife:     o.HalfLife,
		maxFiles:     o.MaxFilesPerCommit,
		authorBonus:  o.AuthorBonus,
		saturation:   o.Saturation,
		unknownScore: o.UnknownScore,
		git:          o.Git,
	}
	if r.window <= 0 {
		r.window = DefaultWindow
	}
	if r.halfLife <= 0 {
		r.halfLife = DefaultHalfLife
	}
	if r.maxFiles == 0 {
		r.maxFiles = DefaultMaxFilesPerCommit
	}
	if r.authorBonus == 0 {
		r.authorBonus = DefaultAuthorBonus
	}
	if r.authorBonus < 0 {
		r.authorBonus = 0
	}
	if r.saturation <= 0 {
		r.saturation = DefaultSaturation
	}
	if r.unknownScore == 0 {
		r.unknownScore = DefaultUnknownScore
	}
	if r.unknownScore < 0 {
		r.unknownScore = 0
	}
	if o.Now != nil {
		r.now = o.Now()
	} else {
		r.now = time.Now().UTC()
	}
	if r.git == nil {
		r.git = NewGitRunner(o.Repo)
	}
	patterns := o.ExcludeMessages
	if patterns == nil {
		patterns = DefaultExcludeMessages
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return resolved{}, fmt.Errorf("churn: exclude pattern %q: %w", p, err)
		}
		r.exclude = append(r.exclude, re)
	}
	return r, nil
}

// FileChurn is one file's mined history. Score is the 0..100 value callers
// multiply; the raw counts are carried alongside it because a composite
// score nobody can decompose is a score nobody trusts.
type FileChurn struct {
	Path string `json:"path"`
	// Commits is the number of qualifying commits — after rename, bulk and
	// message exclusion — that touched this file inside the window.
	Commits int `json:"commits"`
	// WeightedCommits is the same set summed under the recency decay.
	WeightedCommits float64 `json:"weighted_commits"`
	// Authors is the number of distinct commit-author addresses.
	Authors int `json:"authors"`
	// LastCommit is the newest qualifying commit's author time.
	LastCommit time.Time `json:"last_commit"`
	// Score is WeightedCommits and Authors folded into 0..100.
	Score float64 `json:"score"`
}

// FeatureChurn is a roll-up of FileChurn over the files behind one feature.
type FeatureChurn struct {
	// Score is the 0..100 churn factor. When Status is StatusUnknown this
	// is the neutral Options.UnknownScore, not a measurement.
	Score float64 `json:"score"`
	// Status is StatusKnown when at least one file had usable history.
	Status string `json:"status"`
	// HotFile names the file that set Score — the decomposition that makes
	// the number arguable.
	HotFile string `json:"hot_file,omitempty"`
	// Commits, Authors and LastCommit describe HotFile, not the sum over
	// every file: they exist to explain Score, and Score came from one file.
	Commits    int       `json:"commits"`
	Authors    int       `json:"authors"`
	LastCommit time.Time `json:"last_commit,omitzero"`
	// FilesKnown / FilesUnknown split the input paths by whether git could
	// speak for them.
	FilesKnown   int `json:"files_known"`
	FilesUnknown int `json:"files_unknown"`
}

// Report is the result of one mining pass.
type Report struct {
	// Window and HalfLife echo the settings the numbers were produced
	// under. A churn score is meaningless without them.
	Window   time.Duration `json:"window"`
	HalfLife time.Duration `json:"half_life"`

	// Shallow is true when the repository has truncated history. Every
	// score in the report is then a lower bound and every roll-up is
	// StatusUnknown.
	Shallow bool `json:"shallow"`

	// CommitsScanned counts commits that survived every filter;
	// the two Skipped counters say what the filters removed and why.
	CommitsScanned        int `json:"commits_scanned"`
	CommitsSkippedBulk    int `json:"commits_skipped_bulk"`
	CommitsSkippedMessage int `json:"commits_skipped_message"`

	// Files holds every file with at least one qualifying commit, keyed by
	// its current (post-rename) repo-relative slash path.
	Files map[string]FileChurn `json:"files"`

	// Warnings carries anything that makes the numbers less trustworthy —
	// most importantly the shallow-clone case, which is the normal CI
	// checkout and silently wrong if unreported.
	Warnings []string `json:"warnings,omitempty"`

	// tracked is the `git ls-files` set. It is what separates "quiet" from
	// "no history": a tracked file absent from Files really has had no
	// commits in the window, while an untracked one is simply unknown.
	tracked map[string]bool

	unknownScore float64
}

// Mine walks the repository's history and scores every file it touched.
//
// The pass is three subprocesses regardless of repository size: one to ask
// whether history is truncated, one for the tracked-file set, one for the
// log itself. Per-file `git log --follow` would be correct too and is what
// a naive implementation reaches for, but it is one subprocess per file —
// minutes on a repository where this whole call should take under a second.
func Mine(ctx context.Context, opts Options) (*Report, error) {
	if opts.Repo == "" {
		return nil, fmt.Errorf("churn: Repo is required")
	}
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}

	rep := &Report{
		Window:       r.window,
		HalfLife:     r.halfLife,
		Files:        map[string]FileChurn{},
		unknownScore: r.unknownScore,
	}

	shallow, err := isShallow(ctx, r.git)
	if err != nil {
		return nil, err
	}
	rep.Shallow = shallow
	if shallow {
		rep.Warnings = append(rep.Warnings,
			"shallow clone: git history is truncated, so every churn score is a lower "+
				"bound and all rankings are reported as unknown. Re-run with a full "+
				"clone (actions/checkout fetch-depth: 0) for a usable hotspot ranking.")
	}

	rep.tracked, err = trackedFiles(ctx, r.git)
	if err != nil {
		return nil, err
	}

	raw, err := runLog(ctx, r.git, r.now.Add(-r.window))
	if err != nil {
		return nil, err
	}

	acc := newAccumulator(r)
	forEachCommit(raw, acc.add)
	acc.finish(rep)
	return rep, nil
}

// File returns one file's mined churn. The bool is false when the file had
// no qualifying commit in the window — which is NOT the same as unknown;
// use ForFiles when the difference matters.
func (r *Report) File(path string) (FileChurn, bool) {
	fc, ok := r.Files[path]
	return fc, ok
}

// ForFiles rolls per-file churn up to one feature.
//
// The roll-up takes the MAX rather than the mean or the sum. A feature is
// as hot as its hottest file: averaging would let a pile of frozen helpers
// dilute the one module being rewritten weekly, and summing would make a
// feature hot merely for being large. Taking the max also leaves the result
// explainable — HotFile names the file the score came from, so the ranking
// can be argued with rather than merely believed.
//
// Paths are expected to be the file paths Atlas indexed. That is the reuse
// of the scanner's generated-code determination: a generated file is absent
// from the index because the scanner declined it, so its constant churn
// never reaches this function.
func (r *Report) ForFiles(paths []string) FeatureChurn {
	out := FeatureChurn{Status: StatusUnknown, Score: r.unknownScore}
	if len(paths) == 0 || r.Shallow {
		out.FilesUnknown = len(paths)
		return out
	}
	best := FileChurn{Score: -1}
	for _, p := range paths {
		if !r.tracked[p] {
			out.FilesUnknown++
			continue
		}
		out.FilesKnown++
		fc, ok := r.Files[p]
		if !ok {
			// Tracked but untouched inside the window: genuinely quiet.
			// This is the "bad and dead" case the whole ranking exists to
			// push down, so it scores a real zero, not the unknown neutral.
			fc = FileChurn{Path: p}
		}
		if fc.Score > best.Score {
			best = fc
		}
	}
	if out.FilesKnown == 0 {
		return out
	}
	out.Status = StatusKnown
	out.Score = best.Score
	out.HotFile = best.Path
	out.Commits = best.Commits
	out.Authors = best.Authors
	out.LastCommit = best.LastCommit
	return out
}

// UnknownScore is the neutral value ForFiles reports for files git cannot
// speak for. Exposed so callers can label it in their own output.
func (r *Report) UnknownScore() float64 { return r.unknownScore }
