// Package report renders atlas findings into the three formats CI actually
// displays.
//
// Atlas's other packages answer questions; this one carries the answers to
// where the decision is made. A terminal report or a JSON envelope is invisible
// on a pull request, so every team that wanted atlas as a gate had to write
// their own glue: parse the envelope, decide what fails, map findings back to
// lines, render a comment. The industry already converged on three shapes and
// this package emits all three from one Finding type:
//
//   - SARIF 2.1.0 (RenderSARIF) — GitHub code scanning, Azure DevOps and
//     VS Code ingest it, and GitHub renders the results inline on the PR's
//     Files view. One `upload-sarif` step, no glue.
//   - GitHub workflow commands (RenderGitHub) — the cheap path. No upload,
//     no permissions, annotations appear on the diff as soon as the step
//     writes the line.
//   - One sticky PR comment (RenderComment) — the summary a human reads,
//     updated in place on every push rather than appended, so a long-lived
//     branch does not accumulate a column of stale bot comments.
//
// # What this package does NOT do
//
// It never talks to the GitHub API. RenderComment emits a body and the
// HTML-comment marker a workflow greps for to find the comment to update;
// posting it is `gh pr comment`'s job. Keeping the network out means the
// renderers are pure functions over a slice, which is why they are golden-
// testable at all — and a repo that cannot render its own CI output is exactly
// the kind of output that rots.
//
// # The Finding type
//
// Every producer (audit scores, coverage gaps, dead-code candidates, diagnose
// matches) is adapted into []Finding by the FromX functions in adapt.go. The
// renderers know nothing about audit or coverage; adding a producer means
// adding an adapter and a catalog entry, not touching a renderer.
package report
