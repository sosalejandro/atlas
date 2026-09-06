// Package patch scores the coverage of a git diff rather than of a whole
// repository (issue #89).
//
// Whole-repo coverage is a lagging number: no single pull request can move
// it, so no gate can be built on it. Patch coverage -- what fraction of the
// lines THIS diff added or modified is covered -- is the number that can
// gate, and it is the primitive every coverage product ships (Codecov's
// `patch` status, SonarQube's "Clean as You Code", diff-cover).
//
// The package is two halves that are deliberately separable:
//
//	ParseDiff / ChangedLines  -- git's `--unified=0` output to per-file
//	                             post-image line ranges. Shelling out to git
//	                             rather than linking a git library keeps the
//	                             dependency surface at "the binary the user
//	                             already runs CI with".
//	Score                     -- intersect those ranges with the indexed
//	                             symbol spans and charge them against the
//	                             coverage frontier.
//
// # The three states
//
// Score reports a changed line as covered, uncovered, or UNKNOWN, and the
// third state is the whole point. A changed line atlas has no symbol for --
// an unindexed language, a generated file, a docs edit -- is not 0% covered;
// atlas simply cannot see it (issue #85's blind spot). Folding it into the
// uncovered bucket makes the gate fire on files nobody can fix, which teaches
// teams to switch the gate off; folding it into the covered bucket hides real
// gaps. So it is its own bucket: --fail-under decides on the KNOWN fraction
// alone, and the unknown one is printed loudly next to it.
//
// Deleted lines are not patch coverage either -- there is nothing left to
// test -- so only the post-image of each hunk is counted.
package patch
