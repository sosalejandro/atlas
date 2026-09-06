// Package churn mines git history for the change-frequency half of a
// hotspot ranking.
//
// A backlog ranked by health deficit alone puts code that is bad and dead
// next to code that is bad and edited every week. Only the second kind is
// worth a sprint, and the thing that separates them — how often the file
// actually changes — is already sitting in the repository. This package
// reads it, with `git log`, and reports it as a 0..100 score per file that
// callers multiply against their own gap measure.
//
// # What is deliberately NOT counted
//
// Raw commit counts rank noise at the top, so three classes of commit are
// filtered before anything is scored:
//
//   - A pure rename. `R100 old new` moves a file; it does not change it.
//     The move is dropped, but the alias is remembered so the edits that
//     happened under the old path still land on the new one. A rename that
//     also edited the file (`R085`) is real churn and is kept.
//   - A commit touching more than Options.MaxFilesPerCommit files. A
//     reformat, a licence-header sweep, a codegen refresh or a dependency
//     bump adds the same +1 to every file in the repository, which is
//     exactly zero discrimination in a ranking whose only job is to
//     discriminate.
//   - A commit whose subject matches Options.ExcludeMessages. The defaults
//     name the conventional-commit types that are by definition not
//     behaviour change, plus the usual formatter sweeps.
//
// # Recency beats volume
//
// Each surviving commit contributes 0.5^(age/HalfLife) rather than 1, so
// twenty commits three years ago cannot outrank twenty commits last month.
// See DefaultHalfLife for why the default is a quarter.
//
// # Unknown is not zero
//
// A file git has no history for — brand new, or truncated away by a
// shallow clone, which is the normal CI checkout — has UNKNOWN churn.
// Scoring it zero would silently delete the newest code in the repository
// from the backlog; scoring it 100 would put every new file at the top.
// Report.ForFiles returns StatusUnknown and the neutral
// DefaultUnknownScore so the caller can say so out loud. Shallow clones
// are detected once and reported as a warning.
//
// # Generated files
//
// This package does not classify generated code and must not: the scanner
// already made that determination (issue #96) and duplicating its rules
// would let the two drift. Callers roll churn up over the file paths Atlas
// has indexed — a set that excludes generated files precisely because the
// scanner declined them — so a generated file's constant churn never
// reaches a score. See Report.ForFiles.
package churn
