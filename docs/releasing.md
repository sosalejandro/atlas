# Releasing atlas

This document is for maintainers. Users want [docs/install.md](./install.md).

- [The one mechanism](#the-one-mechanism)
- [Cutting a stable release](#cutting-a-stable-release)
- [How release.yml actually gets triggered](#how-releaseyml-actually-gets-triggered)
- [Homebrew](#homebrew)
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
3. `release-please.yml`'s `dispatch-release-build` job hands that tag to
   `release.yml` (see the next section — the tag push alone does **not**
   start it), which:
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

## How release.yml actually gets triggered

`release.yml` declares `on: push: tags: ['v*']`, and that trigger is
**never** what fires it for a release-please tag. GitHub does not start new
workflow runs from events raised with the default `GITHUB_TOKEN`, and
release-please pushes the tag with exactly that token. Left alone, the
result is a pipeline that looks green and builds nothing: the Release PR
merges, the tag appears, and no release is ever published.

`workflow_dispatch` and `repository_dispatch` are the two documented
exceptions to that rule. So `release-please.yml` carries a
`dispatch-release-build` job that runs only when `release_created == 'true'`
and calls:

```bash
gh workflow run release.yml --ref "$TAG" -f tag="$TAG"
```

`--ref` is the tag, not `main`, and that is load-bearing rather than
stylistic: the run's OIDC identity is "this workflow file at this ref", so
it is what the published Sigstore signature is checked against. Dispatching
from `main` would sign as `release.yml@refs/heads/main` and quietly change
the identity docs/install.md tells people to verify — and would run
whatever `main`'s workflow says rather than what shipped in the tag.

**What an operator has to configure: nothing.** That is the reason this
bridge was chosen over the alternative. The other fix is a personal access
token or GitHub App token on the release-please step, so the tag push comes
from a non-`GITHUB_TOKEN` identity and fires the `push: tags` trigger
normally. It works, and it costs a long-lived credential with write access
to this repository plus a rotation story — for a pipeline whose entire
selling point is that its signing identity is keyless and has nothing to
rotate. The bridge needs `actions: write` on one job and no secret at all.

If you do switch to a PAT, remove the `dispatch-release-build` job in the
same change. With both in place a release-please tag fires `release.yml`
twice; the second run is harmless (reproducible build, `--clobber` upload)
but the duplicate is noise nobody should have to explain.

The `push: tags` trigger stays because it is still the right behaviour for a
tag pushed by a human, and `workflow_dispatch` also lets a maintainer
re-run an existing tag by hand.

## Homebrew

`brew install` is an acceptance criterion of issue #121. What lives in this
repository is the formula generator: `.github/scripts/brew-formula.sh`
renders `Formula/atlas.rb` for a tag, taking every `sha256` from that
release's signed `SHA256SUMS` rather than recomputing it, so the digest a
user's `brew` checks and the digest the release published cannot diverge. It
fails if any of the four `brew`-relevant assets (darwin/linux x
amd64/arm64) is missing from the manifest — a formula silently missing a
platform fails first for a user, not for us.

What is **not** in this repository is the tap. Homebrew resolves
`brew install <owner>/<tap>/atlas` to a repository named
`<owner>/homebrew-<tap>`, which has to exist and has to be writable by
something other than the default `GITHUB_TOKEN`. That is two settings, and
until they exist the `homebrew` job in `release.yml` generates the formula,
prints it in the job summary, and says so:

| Setting | Kind | Value |
| --- | --- | --- |
| `HOMEBREW_TAP_REPO` | repository **variable** | `<owner>/homebrew-<tap>` |
| `HOMEBREW_TAP_TOKEN` | repository **secret** | a token that can push to that repository |

With both set, the job commits `Formula/atlas.rb` to the tap's default
branch on every release. With neither, it reports the gap. It never fails
the release: it runs after publication, so a red X there could not undo
anything, and naming it "blocking" would misrepresent what it gates.

Nobody has run `brew install atlas` end to end from this repository, because
there is no tap to run it against. Do not write that it works until someone
has.

## The edge channel

`edge.yml` runs the same pipeline on every commit to `main` and publishes to
a single rolling prerelease tagged `edge`. Same build guarantees, same
signature, same SBOM, same SLSA provenance attestation. No compatibility
promise of any kind — see the channel table in
[docs/install.md](./install.md#channels), and keep the two in sync if you
change one.

The `edge` git tag is force-moved on every publish, so yesterday's edge
build is not retrievable. That is the promise, stated so nobody builds a
process on top of the opposite assumption.

**Asset names are fixed, and that is load-bearing.** `build.sh` stamps an
edge build with `git describe`, so it writes
`atlas_v0.13.0-7-gabc1234_linux_amd64`. But the consumer action installs
`edge` by asking for `atlas_edge_<goos>_<goarch>` — `install.sh` builds the
name from the version string it was handed, and for this channel that string
is literally `edge`. Nothing reconciled the two until
`.github/scripts/edge-assets.sh`, so every documented edge install 404'd.
That script renames the built assets before `SHA256SUMS` is written, so the
manifest and the signature over it cover the names people actually download;
the version inside the binary is untouched, and `atlas version` still
reports the describe string.

Two cases in `scripts_test.sh` check the installer's asset name and the
publisher's asset name against **each other** rather than each against a
literal, because two literals in two files is how they drifted apart.

## What CI checks, and what blocks

Job names carry `(blocking)` or `(ADVISORY)`. A green check that gates
nothing teaches contributors that checks do not matter.

| Job | Blocks? | What it establishes |
| --- | --- | --- |
| `build + test + lint — {ubuntu,macos,windows}` | yes | The suite passes on all three OSes. `-race` on Linux and macOS only — the race detector needs cgo and a C toolchain, which this project deliberately does not require. |
| `reproducible build + cross-compile + release scripts` | yes | `make test-scripts-full`: the release scripts' own tests, the six-target cross-compile with its cgo-free assertion, the build-twice digest comparison, workflow YAML parsing, and the "every action is pinned to a SHA" check. The digest comparison is same-machine, same-toolchain — see [When reproducibility breaks](#when-reproducibility-breaks) for what that scopes it to. |
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

`make repro` varies four things between the two builds — output directory,
`TMPDIR`, `GOMAXPROCS`, and the absolute path of the source tree — and holds
everything else fixed. Both builds run on one machine with one Go toolchain,
so a green result scopes to "the output does not depend on those four", not
to "any machine produces these bytes". The cross-machine claim is
established by a third party rebuilding a tag with the pinned toolchain and
comparing against `SHA256SUMS`, which is the recipe in docs/install.md. Do
not let the two be written up as the same check.

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

- **The Homebrew tap repository, and the Scoop manifest.** Issue #121 asks
  for both. The formula itself is now generated on every release from the
  signed `SHA256SUMS` (see [Homebrew](#homebrew) above), but it has nowhere
  to go until someone creates `<owner>/homebrew-<tap>` and a token for it —
  a second repository is a decision, not a script. Nothing Scoop-shaped
  exists at all.
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
