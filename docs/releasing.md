# Releasing atlas

This document is for maintainers. Users want [docs/install.md](./install.md).

- [The one mechanism](#the-one-mechanism)
- [Cutting a stable release](#cutting-a-stable-release)
- [The edge channel](#the-edge-channel)
- [What CI checks, and what blocks](#what-ci-checks-and-what-blocks)
- [The version-consistency debt](#the-version-consistency-debt)
- [Bumping the Go toolchain](#bumping-the-go-toolchain)
- [When reproducibility breaks](#when-reproducibility-breaks)
- [What is not automated yet](#what-is-not-automated-yet)

## The one mechanism

Four places record what version atlas is:

| Where | Who reads it |
| --- | --- |
| `internal/cli.Version` in `internal/cli/root.go` | the user, via `atlas --version` |
| `.release-please-manifest.json` | release-please, to decide the next bump |
| `CHANGELOG.md` | a human deciding whether to upgrade |
| the git tag | everything that downloads a release |

Exactly one thing is allowed to move them: **release-please**. Hand-editing
a version constant is how they drift apart, and once they have drifted a bug
report against "atlas v0.13.0" cannot be mapped to a commit, because no such
release exists.

`.github/scripts/check-version-consistency.sh` compares all four. It runs
advisory in CI and **blocking** in the release job — a release whose binary
disagrees with its tag is not evidence of anything.

## Cutting a stable release

1. Merge conventional-commit work to `main`. `release-please.yml` opens or
   updates a Release PR that bumps the version, regenerates `CHANGELOG.md`,
   and bakes the stamps into `internal/cli/root.go`.
2. Review and merge the Release PR. release-please cuts the `vX.Y.Z` tag.
3. The tag push triggers `release.yml`, which:
   - checks the version, manifest, changelog and tag agree — and stops here
     if they do not;
   - cross-compiles all six targets with `CGO_ENABLED=0` and asserts, by
     reading each artifact's embedded build settings, that they really are
     cgo-free and trimpath'd;
   - **builds everything a second time and compares digests**;
   - generates an SPDX SBOM into `dist/`;
   - writes `SHA256SUMS` over every artifact including the SBOM;
   - signs that one manifest with cosign keyless (no key exists; the signing
     identity is the workflow file at that tag, via GitHub's OIDC);
   - attests SLSA provenance for each binary;
   - publishes the GitHub Release.

Re-running is safe. `workflow_dispatch` accepts an existing tag, and asset
upload uses `--clobber`, which is only safe *because* the build is
reproducible: the replacement bytes are the same bytes.

## The edge channel

`edge.yml` runs the same pipeline on every commit to `main` and publishes to
a single rolling prerelease tagged `edge`. Same build guarantees, same
signature, same SBOM. No compatibility promise of any kind — see the channel
table in [docs/install.md](./install.md#channels), and keep the two in sync
if you change one.

The `edge` git tag is force-moved on every publish, so yesterday's edge
build is not retrievable. That is the promise, stated so nobody builds a
process on top of the opposite assumption.

## What CI checks, and what blocks

Job names carry `(blocking)` or `(ADVISORY)`. A green check that gates
nothing teaches contributors that checks do not matter.

| Job | Blocks? | What it establishes |
| --- | --- | --- |
| `build + test + lint — {ubuntu,macos,windows}` | yes | The suite passes on all three OSes. `-race` on Linux and macOS only — the race detector needs cgo and a C toolchain, which this project deliberately does not require. |
| `reproducible build + cross-compile + release scripts` | yes | `make test-scripts-full`: the release scripts' own tests, the six-target cross-compile with its cgo-free assertion, the build-twice digest comparison, workflow YAML parsing, and the "every action is pinned to a SHA" check. |
| `scan determinism` | yes | The suite from [docs/testing/determinism.md](./testing/determinism.md), run with `-count=1` so a cached pass cannot stand in for a measurement. |
| `version / manifest / changelog agree` | **no — see below** | The four version sources agree. |

Everything in the second row runs identically on a laptop:

```bash
make test-scripts-full     # what CI runs
make repro                 # just the build-twice comparison
make build-matrix          # just the cross-compile
make ci                    # vet + test + lint + the above
```

That is the point of keeping step logic in `.github/scripts/*.sh` rather
than in workflow YAML. A step that exists only in YAML can only be tested by
pushing and waiting.

## The version-consistency debt

At the time this pipeline landed, the four sources disagreed:

| Source | Value |
| --- | --- |
| `internal/cli.Version` | `v0.13.0` |
| `.release-please-manifest.json` | `0.8.0` |
| `CHANGELOG.md` newest entry | `0.8.0` |

Releases had been happening by hand-edited version constants, which is how
the binary got five minor versions ahead of the release tool. This is issue
[#121](https://github.com/sosalejandro/atlas/issues/121)'s "one mechanism"
requirement, and it is the reason the CI job is advisory rather than
blocking: turning it on now would red-X every pull request for a defect none
of them introduced.

The fix is a decision, not a script — someone has to choose which number is
real:

- **Adopt `0.13.0` as current.** Set `.release-please-manifest.json` to
  `0.13.0` and backfill `CHANGELOG.md` with the 0.9–0.13 entries. Honest
  about what shipped; the changelog entries have to be written by hand.
- **Treat 0.9–0.13 as never released.** Reset `internal/cli.Version` to
  `v0.8.0` and let release-please bump from there. Cheap, but anyone holding
  a binary that says v0.13.0 is now holding a version number that means
  nothing.

Whichever is chosen: after it lands, delete `continue-on-error` from the
`version-consistency` job in `.github/workflows/ci.yml` and drop `ADVISORY`
from its name. Leaving an advisory check advisory forever is how it becomes
invisible.

## Bumping the Go toolchain

The pin lives in `.github/scripts/toolchain.txt` and nowhere else — CI, the
release job and `build.sh` all read it.

Bumping it **changes the bytes of every artifact**. Binaries built before
and after the bump will not have matching digests, which is correct and
expected: reproducibility is a promise about "same source, same toolchain",
not about "same source, forever".

1. Edit `.github/scripts/toolchain.txt`.
2. Check `go.mod`'s `go` directive is still satisfied (it is a language
   floor, not a toolchain pin — the two are allowed to differ).
3. `make ci`.
4. Say so in the release notes. Someone who verified the previous release
   by rebuilding it will otherwise get a mismatch and reasonably assume
   tampering.

## When reproducibility breaks

`make repro` failing means something in the build depends on the environment
rather than on the source. In rough order of likelihood:

- a timestamp taken from the clock instead of `SOURCE_DATE_EPOCH`;
- an absolute path escaping `-trimpath` — usually a generated file that
  embeds the path it was generated from;
- a code generator (sqlc, an `embed` fixture) whose output is not
  deterministic, or one whose output was not committed;
- map iteration order reaching generated or embedded content;
- a Go env var leaking in from the shell. `build.sh` pins `GOFLAGS`,
  `GOEXPERIMENT`, `GODEBUG`, `GOAMD64` and `GOARM64` for exactly this
  reason, so if a new one appears it belongs in that list.

To find *where* the binaries differ, build both halves by hand and compare
their sections rather than staring at two digests:

```bash
VERSION=v0.0.0 DIST=/tmp/a .github/scripts/build.sh
VERSION=v0.0.0 DIST=/tmp/b .github/scripts/build.sh
cmp -l /tmp/a/atlas_* /tmp/b/atlas_* | head
go version -m /tmp/a/atlas_*   # and /tmp/b — the settings tables often differ visibly
```

## What is not automated yet

Named here rather than left for someone to discover:

- **Homebrew tap and Scoop manifest.** Issue #121 asks for both. The release
  assets and `SHA256SUMS` are exactly what a formula needs, but publishing
  to a tap repository needs a second repository and a token, which is a
  decision rather than a script.
- **Marketplace listing for the action.** The action ships in-tree at
  `.github/actions/atlas` and is usable today as
  `sosalejandro/atlas/.github/actions/atlas@vX.Y.Z`. A Marketplace listing
  requires the action at the root of its own repository.
- **deb/rpm packages and a container image.**
- **Benchmark regression gate (`benchstat` against the base branch).** #121
  asks for it; it defends the performance claims in #109 and is unrelated to
  the supply-chain work here.
- **An LTS policy.** #121 asks for "release-line X supported for N months"
  rather than "the last two minors". That is a support commitment, and
  writing one down that nobody has agreed to would be worse than the gap.
