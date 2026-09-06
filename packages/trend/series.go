package trend

import (
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ScopeProject names the whole-project series, as opposed to a series named
// after a feature id. It is a sentinel rather than an empty string so a JSON
// consumer never has to guess whether a blank scope meant "project" or
// "someone forgot to set it".
const ScopeProject = "project"

// Observation is one point on one series: a score, the size of the surface it
// was measured over, and where in history it sits.
//
// Score is nil when there was no coverage evidence at that commit — either
// nothing was ever ingested, or (for a feature series) this feature had no
// row in that commit's breakdown. Consumers MUST branch on Measured rather
// than dereferencing; treating nil as zero is how a trend line grows cliffs
// that never happened.
type Observation struct {
	CommitSHA   string    `json:"commit_sha"`
	MeasuredAt  time.Time `json:"measured_at"`
	Score       *float64  `json:"score"`
	Denominator int64     `json:"denominator"`
}

// Measured reports whether this point carries a usable score.
func (o Observation) Measured() bool { return o.Score != nil }

// Series is an ordered run of observations for one scope, oldest first.
type Series struct {
	// Scope is ScopeProject or a feature id.
	Scope  string        `json:"scope"`
	Points []Observation `json:"points"`
}

// Bounds returns the first and last MEASURED observations. Unmeasured points
// stay in Points (the gap is information) but must never define an endpoint —
// "coverage went from unknown to 80" is not a delta anyone can act on.
//
// ok is false when the series has no measured point at all.
func (s Series) Bounds() (first, last Observation, ok bool) {
	for _, p := range s.Points {
		if !p.Measured() {
			continue
		}
		if !ok {
			first, ok = p, true
		}
		last = p
	}
	return first, last, ok
}

// Latest returns the newest measured observation, or nil when the series has
// none.
func (s Series) Latest() *Observation {
	for i := len(s.Points) - 1; i >= 0; i-- {
		if s.Points[i].Measured() {
			p := s.Points[i]
			return &p
		}
	}
	return nil
}

// ProjectSeries lifts the whole-project score out of a run of history points.
// The caller supplies them oldest-first (store.History.List already does).
func ProjectSeries(points []store.HistoryPoint) Series {
	out := Series{Scope: ScopeProject, Points: make([]Observation, 0, len(points))}
	for _, p := range points {
		out.Points = append(out.Points, Observation{
			CommitSHA:   p.CommitSHA,
			MeasuredAt:  p.MeasuredAt,
			Score:       p.Score,
			Denominator: p.Denominator,
		})
	}
	return out
}

// FeatureSeries lifts one feature's score out of each history point.
//
// A commit whose breakdown has no row for this feature still contributes a
// point, with a nil score. Dropping it instead would silently close the gap
// and make a feature that stopped being measured look continuous.
func FeatureSeries(points []store.HistoryPoint, id shared.FeatureID) Series {
	out := Series{Scope: string(id), Points: make([]Observation, 0, len(points))}
	for _, p := range points {
		obs := Observation{CommitSHA: p.CommitSHA, MeasuredAt: p.MeasuredAt}
		for _, f := range p.Features {
			if f.FeatureID == id {
				obs.Score = f.Score
				obs.Denominator = f.Denominator
				break
			}
		}
		out.Points = append(out.Points, obs)
	}
	return out
}

// FeatureMeasurement is one feature's contribution to a point being
// assembled. Score is nil when the audit had no coverage evidence for the
// feature at this commit.
type FeatureMeasurement struct {
	FeatureID   shared.FeatureID
	Score       *float64
	Denominator int64
}

// AssembleInput is everything Assemble needs to build one history point.
type AssembleInput struct {
	CommitSHA  string
	MeasuredAt time.Time
	Note       *string
	Features   []FeatureMeasurement
}

// Assemble folds per-feature measurements into the history point that gets
// persisted. Pure: no store, no clock, no git.
//
// The project score is the DENOMINATOR-WEIGHTED mean of the measured
// features. An unweighted mean lets a one-symbol feature outvote a
// two-hundred-symbol one, so a repo could hold its headline number steady
// while its bulk rotted — the precise failure a trend line exists to catch.
//
// Unmeasured features are excluded from both the numerator and the
// denominator, and the project score is nil when nothing at all was measured.
// They are still recorded in the breakdown so `trend --feature` can show the
// gap rather than pretend the feature was never there.
func Assemble(in AssembleInput) store.HistoryPoint {
	feats := make([]store.HistoryFeaturePoint, 0, len(in.Features))
	var (
		weighted float64
		denom    int64
	)
	for _, f := range in.Features {
		feats = append(feats, store.HistoryFeaturePoint{
			FeatureID:   f.FeatureID,
			Score:       f.Score,
			Denominator: f.Denominator,
		})
		if f.Score == nil || f.Denominator <= 0 {
			continue
		}
		weighted += *f.Score * float64(f.Denominator)
		denom += f.Denominator
	}
	sort.Slice(feats, func(i, j int) bool { return feats[i].FeatureID < feats[j].FeatureID })

	point := store.HistoryPoint{
		CommitSHA:   in.CommitSHA,
		MeasuredAt:  in.MeasuredAt,
		Denominator: denom,
		Note:        in.Note,
		Features:    feats,
	}
	if denom > 0 {
		score := weighted / float64(denom)
		point.Score = &score
	}
	return point
}
