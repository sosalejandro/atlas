// Package onboard derives a PROVISIONAL capability map from a repository
// that carries no @atlas:feature annotations, and turns it into a first-run
// report whose first line is something the reader did not already know.
//
// # Why it exists
//
// Every signal atlas computes -- coverage per capability, the audit gate,
// the sprint ranking -- is gated behind someone having annotated the code
// first. On a repository where nobody has done that yet, atlas can report a
// symbol count and nothing else, which is not a reason to keep going. This
// package closes that gap by inferring groupings from what a scan already
// knows: HTTP route registrations, the SQL each file issues, test names,
// the directory tree, git history, and any coverage that happens to be
// present.
//
// # Inferred is not declared
//
// The registry is atlas's ground truth. Its value comes entirely from the
// fact that a human wrote every entry in it, so an inferred grouping must
// never enter it, and must never be presentable as though it had.
//
// Two mechanisms enforce that here, one structural and one editorial:
//
//   - Infer is a pure function. It takes values and returns values; it holds
//     no store handle and opens no database, so no code path through this
//     package can write the features table even by mistake. Promotion is a
//     separate, explicit user action in the CLI layer that writes an
//     annotation into the source file, which then flows through the normal
//     ingest -- the same path a hand-written annotation takes.
//   - Every record this package emits carries Provisional=true, is addressed
//     through the "provisional:" namespace (see Capability.Ref), and is
//     persisted under .atlas/provisional/ rather than anywhere the rest of
//     atlas reads as declared state.
//
// # What the proposals are made of
//
// Grouping proceeds strongest-signal-first, and each stage claims symbols
// the earlier stages left. A symbol already inside a declared feature is
// claimed by nobody: existing annotations are adopted as they are, never
// duplicated into a proposal.
//
//  1. Routes. An HTTP surface is a capability list its own clients already
//     agree with, so a route registration claims its handler.
//  2. Test-name clusters. Two or more tests in one package that lead with
//     the same subject word are evidence for that subject; it claims the
//     production symbols in the package whose names carry the same word.
//  3. Directories. Everything left, grouped where it lives.
//
// Anything that resolves to no symbol is dropped before display. A proposal
// with nothing behind it is noise the reader has to refute.
package onboard
