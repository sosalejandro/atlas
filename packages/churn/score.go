package churn

import (
	"math"
	"time"
)

// accumulator folds the commit stream into per-file totals.
//
// It is stateful for one reason: renames. The log arrives newest-first, so
// by the time an older commit mentions "old.go" the accumulator has already
// seen the commit that renamed it and can redirect that history onto the
// current name. Doing this in a second pass would need the whole log in
// memory twice; doing it per file with `git log --follow` would need one
// subprocess per file.
type accumulator struct {
	opt resolved

	// alias maps a historical path to the name the file has today. Chains
	// are followed, so a file renamed twice still lands on its final name.
	alias map[string]string

	files map[string]*fileAcc

	scanned int
	bulk    int
	message int
}

type fileAcc struct {
	commits  int
	weighted float64
	authors  map[string]bool
	last     time.Time
}

func newAccumulator(opt resolved) *accumulator {
	return &accumulator{
		opt:   opt,
		alias: map[string]string{},
		files: map[string]*fileAcc{},
	}
}

// add folds one commit in, or records why it was dropped.
func (a *accumulator) add(c logCommit) {
	if a.excludedByMessage(c.Subject) {
		a.message++
		// A dropped commit still teaches us about renames: a licence sweep
		// that also moved a file must not sever that file's history.
		a.learnRenames(c)
		return
	}
	if a.opt.maxFiles > 0 && len(c.Entries) > a.opt.maxFiles {
		a.bulk++
		a.learnRenames(c)
		return
	}
	a.scanned++
	weight := decay(a.opt.now.Sub(c.At), a.opt.halfLife)
	for _, e := range c.Entries {
		path := a.canonical(e.Path)
		if !isPureMove(e) {
			a.touch(path, c, weight)
		}
		a.learnRename(e)
	}
}

// learnRenames records a dropped commit's renames without counting it.
func (a *accumulator) learnRenames(c logCommit) {
	for _, e := range c.Entries {
		a.learnRename(e)
	}
}

// learnRename points a historical path at its current name. Copies are
// deliberately excluded: after `C`, both paths still exist, so aliasing the
// source onto the copy would move the original's history off it.
func (a *accumulator) learnRename(e statusEntry) {
	if e.Old == "" || e.Status == "" || e.Status[0] != 'R' {
		return
	}
	a.alias[e.Old] = a.canonical(e.Path)
}

// canonical follows the alias chain to the file's present-day name. The
// bound is defensive: a cycle cannot arise from a real history, but a
// truncated or corrupt log should not spin.
func (a *accumulator) canonical(p string) string {
	for i := 0; i < 32; i++ {
		next, ok := a.alias[p]
		if !ok || next == p {
			return p
		}
		p = next
	}
	return p
}

func (a *accumulator) touch(path string, c logCommit, weight float64) {
	f, ok := a.files[path]
	if !ok {
		f = &fileAcc{authors: map[string]bool{}}
		a.files[path] = f
	}
	f.commits++
	f.weighted += weight
	if c.Author != "" {
		f.authors[c.Author] = true
	}
	if c.At.After(f.last) {
		f.last = c.At
	}
}

func (a *accumulator) excludedByMessage(subject string) bool {
	for _, re := range a.opt.exclude {
		if re.MatchString(subject) {
			return true
		}
	}
	return false
}

// finish writes the accumulated totals into the report as scored FileChurn.
func (a *accumulator) finish(rep *Report) {
	rep.CommitsScanned = a.scanned
	rep.CommitsSkippedBulk = a.bulk
	rep.CommitsSkippedMessage = a.message
	for path, f := range a.files {
		rep.Files[path] = FileChurn{
			Path:            path,
			Commits:         f.commits,
			WeightedCommits: f.weighted,
			Authors:         len(f.authors),
			LastCommit:      f.last,
			Score:           score(f.weighted, len(f.authors), a.opt),
		}
	}
}

// decay is the recency weight of a single commit: 0.5 at one half-life,
// 0.25 at two, and so on.
//
// Exponential rather than linear because a linear ramp has a cliff at the
// window edge — a commit one day outside the window would count zero while
// its neighbour counts something, and moving the window would reorder the
// backlog. The exponential has no edge: widening the window can only add
// weight that was already near-nothing.
func decay(age, halfLife time.Duration) float64 {
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, age.Seconds()/halfLife.Seconds())
}

// score folds decayed commit volume and author diversity into 0..100.
//
// The volume term saturates — w/(w+k) — rather than scaling linearly,
// because the difference between two and eight recent commits is the
// interesting one and the difference between eighty and two hundred is not.
// A linear scale would let one runaway file compress every other score
// toward zero and flatten exactly the part of the range the ranking has to
// discriminate over.
//
// Author diversity is a multiplier on top, capped, and worth at most
// authorBonus. Contended code is riskier code, but "three people touched
// it" is a weaker claim than "it changed last week", so it adjusts the
// score rather than competing with it.
func score(weighted float64, authors int, opt resolved) float64 {
	if weighted <= 0 {
		return 0
	}
	base := 100 * weighted / (weighted + opt.saturation)
	s := base * authorFactor(authors, opt.authorBonus)
	if s > 100 {
		return 100
	}
	return s
}

// authorFactor rises from 1 toward 1+bonus as authors accumulate, with
// diminishing returns: the second author is most of the signal (the file is
// shared at all), the fifth adds little.
func authorFactor(authors int, bonus float64) float64 {
	if authors <= 1 || bonus <= 0 {
		return 1
	}
	return 1 + bonus*(1-1/float64(authors))
}
