// Package sprintplan ranks features for sprint backlog inclusion using
// gap-weighted priority signals. It consumes the output of packages/audit
// and produces an ordered slice of SprintItem records — highest priority
// first.
//
// Priority formula (default weights):
//
//	Priority = (100 - Score) * 0.6   // how broken the feature is
//	         + BugSignal     * 0.2   // recent failing-coverage rate
//	         + RecencyDecay  * 0.2   // freshness of underlying code
//
// All three terms live in 0..100. BugSignal is the count of failing
// coverage results for this feature's symbols in the last 7 days (capped
// at 100). RecencyDecay is 100 if any annotation in the feature was touched
// in the last 7 days, then decays linearly to 0 over 90 days — the
// "fresh code is worth fixing first" heuristic.
//
// Cost is a discrete bucket — S (≤3 symbols), M (4–15), L (>15) — based on
// the count of impl/contract symbols linked to the feature.
//
// The package is intentionally read-only against Store; it doesn't mutate
// the database. Callers persist results separately when needed.
package sprintplan
