# Releasing atlas

This document is for maintainers. Users want [docs/install.md](./install.md).

- [The one mechanism](#the-one-mechanism)
- [Why 0.14.0](#why-0140)
- [Cutting a stable release](#cutting-a-stable-release)
- [Cutting v0.14.0, which is a one-off](#cutting-v0140-which-is-a-one-off)
- [What an operator has to configure](#what-an-operator-has-to-configure)
- [What to check after a release](#what-to-check-after-a-release)
- [What has never run](#what-has-never-run)
- [How release.yml actually gets triggered](#how-releaseyml-actually-gets-triggered)
- [Homebrew](#homebrew)
- [The edge channel](#the-edge-channel)
- [Asset names, and the four places that spell them](#asset-names-and-the-four-places-that-spell-them)
- [What CI checks, and what blocks](#what-ci-checks-and-what-blocks)
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

`.github/scripts/check-version-consistency.sh` compares all four. It is
**blocking** in CI and blocking in the release job — a release whose binary
disagrees with its tag is not evidence of anything. It was advisory from the
day it landed until the reconciliation below, for the only defensible
reason an advisory gate ever has: it was failing.

## Why 0.14.0

The four sources had drifted to four different answers. Reconciling them is
a decision about which number is real, so here is the evidence and the
argument, not just the outcome.

**What the history actually says.** `v0.1.0` through `v0.8.0` were cut by
release-please — each one has a `chore(main): release X.Y.Z` merge and a
`CHANGELOG.md` entry. After that the mechanism was abandoned:

| Number | Tag? | Changelog? | How it came to exist |
| --- | --- | --- | --- |
| 0.9.0 | yes | no | hand-cut: `b635456 chore(release): bump version to v0.9.0` |
| 0.10.0 | yes | no | hand-cut: `29bcb05 chore(release): v0.10.0 …` |
| 0.11.0 | yes | no | hand-cut: `7b5e5e3 chore(release): v0.11.0 …` |
| 0.12.0 | **no** | no | declared in `53e3c6d chore(release): v0.12.0 …` and nowhere else |
| 0.13.0 | **no** | no | declared in `13c5c2b`'s subject and in `internal/cli.Version` |

`v0.11.0` (2026-06-01) is the newest tag. `v0.11.0..HEAD` is 60 commits, 40
of them conventional. The tell that 0.9.0 was hand-cut rather than
computed: `v0.8.0..v0.9.0` contains only `fix:` commits, and release-please
would have called that 0.8.1.

**The numbers that were rejected, and why.**

- **0.9.0** — what release-please computes today, because the manifest
  anchor was left at 0.8.0 and there are `feat:` commits after it. It
  collides with a tag that already exists. This is not a cosmetic
  disagreement: the manifest is a machine-read anchor, and it was pointing
  at a boundary five releases behind reality.
- **0.12.0** — what release-please would compute from an 0.11.0 anchor. The
  tree already spent that number. Every binary built from `main` between
  `53e3c6d` and `13c5c2b` reports `v0.12.0`.
- **0.13.0** — the number `internal/cli.Version` has carried since
  2026-06-02, which means every `go build` from `main` since then, this
  repository's own dogfood runs included, reports `v0.13.0`. Adopting it as
  "released" is the tempting option because it is the smallest edit, and it
  is the worst one: today a report against v0.13.0 resolves to nothing,
  which is at least honest about being unresolvable. Tagged, it would
  resolve to a tree 40 commits away from the one that printed it. Turning
  an unanswerable question into a confidently wrong answer is a
  downgrade.
- **1.0.0** — a stability promise, and nobody has made one. In this window
  alone the store schema moved from 8 to 19 and two top-level commands were
  renamed. There is still no LTS policy (see
  [What is not automated yet](#what-is-not-automated-yet)).

**0.14.0 is the first number that no artifact claims and no tag holds.**
Version numbers are free; ambiguity about which bytes a number names is
not, and it is the whole thing this pipeline exists to eliminate. Burning
0.12.0 and 0.13.0 costs nothing and buys an unambiguous first release.

It is a minor rather than a patch because the release is breaking:
`atlas audit` became `atlas health` and `atlas trace` became `atlas chain`,
so anything scripting the CLI breaks. Under `.release-please-config.json`'s
`bump-minor-pre-major`, a breaking change below 1.0 is a minor bump, which
is why this is 0.14.0 and not 1.0.0.

**Why the manifest gets 0.14.0 and not 0.13.0.** The manifest is the anchor
release-please bumps *from*, so it has to name a release that exists. Set
to 0.13.0 it would name one that never did, and every future delta would be
computed against a boundary release-please cannot find. Set to 0.14.0 it
names the release the next section tells you to cut, after which the anchor
and the tag agree and release-please owns every number that follows.

The consequence, stated plainly: this branch is shaped exactly like a
release-please Release PR for 0.14.0 — manifest bumped, changelog written,
version constant stamped — assembled by hand because release-please could
not assemble it from a wrong anchor. Merging it does not cut the tag.
Somebody has to, once.

## Cutting a stable release

This is the steady-state procedure, and it is what every release from
0.15.0 onward looks like. **0.14.0 itself does not work this way** — see
[the next section](#cutting-v0140-which-is-a-one-off).

1. Merge conventional-commit work to `main`. `release-please.yml` opens or
   updates a Release PR that bumps the version, regenerates `CHANGELOG.md`,
   and bakes the stamps into `internal/cli/root.go`.
2. Review and merge the Release PR. release-please cuts the `vX.Y.Z` tag.
3. `release-please.yml`'s `dispatch-release-build` job hands that tag to
   `release.yml` (see [How release.yml actually gets
   triggered](#how-releaseyml-actually-gets-triggered) — the tag push alone
   does **not** start it), which:
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

## Cutting v0.14.0, which is a one-off

0.14.0 cannot come out of release-please, because release-please needs an
anchor that names a real release and there was not one (see
[Why 0.14.0](#why-0140)). The reconciliation branch supplies the anchor by
hand; a human supplies the tag, once. Every release after this one goes
through the steady-state procedure above.

1. **Merge the reconciliation to `main`.** It carries the manifest at
   `0.14.0`, the `CHANGELOG.md` entry for 0.14.0, and
   `internal/cli.Version = "v0.14.0"`.

2. **Tag the merge commit and push the tag, immediately.**

   ```bash
   git checkout main && git pull
   .github/scripts/check-version-consistency.sh --tag v0.14.0   # must print "consistent"
   git tag -a v0.14.0 -m "v0.14.0"
   git push origin v0.14.0
   ```

   The push has to come from a human (or any credential that is not the
   Actions `GITHUB_TOKEN`). That is the whole reason it works: a tag pushed
   with `GITHUB_TOKEN` starts no workflow run, which is the defect the
   `dispatch-release-build` bridge exists to route around. A tag you push
   yourself fires `release.yml`'s `on: push: tags` trigger normally.

3. **A Release PR may appear in between. Do not merge it.** The merge in
   step 1 is a push to `main`, so `release-please.yml` runs while the
   manifest says 0.14.0 and no `v0.14.0` tag exists yet.

   What release-please does with an anchor whose tag it cannot find has
   **not been tested here** — nobody has run it in this state. The
   predicted outcome is a PR titled something like "chore(main): release
   0.15.0" carrying the whole backlog, because with no boundary to diff
   against it falls back to scanning from further back. It could also
   simply open nothing. Either is survivable and neither is a reason to
   stop; what is *not* survivable is merging that PR, because it would cut
   0.15.0 before 0.14.0 exists.

   Close whatever appears. Once the tag exists, the next push to `main`
   gives release-please a boundary it can find and it regenerates a correct
   PR. Keeping step 1 and step 2 close together shrinks the window to
   nothing.

4. **From 0.15.0 onward, stop doing this.** The anchor, the tag and the
   changelog now agree, so release-please computes the next number, opens
   the Release PR, cuts the tag on merge, and
   [hands it to `release.yml`](#how-releaseyml-actually-gets-triggered).
   Cutting a tag by hand again would put the numbers straight back where
   they were.

## What an operator has to configure

Split by what can be checked from a checkout and what cannot.

**Verified present in-tree** — read from the workflow files, no operator
action needed:

| Need | Where it comes from |
| --- | --- |
| create the release, upload assets | `contents: write` on the `release` job |
| cosign keyless signing | `id-token: write` on the `release` job; the OIDC token is minted per run |
| SLSA provenance | `attestations: write` on the `release` job |
| start `release.yml` from release-please | `actions: write` on `dispatch-release-build` |
| everything above's credential | `github.token`. **No secret to create, none to rotate.** |

**Repository settings, which cannot be read from a checkout.** Check these
before the first release rather than after it fails:

- *Settings → Actions → General → Workflow permissions*: "Allow GitHub
  Actions to create and approve pull requests" must be on, or
  release-please cannot open the Release PR at all. This is the single
  most common way a release-please setup produces a green run and no PR.
- Branch protection on `main` must permit the `github-actions[bot]` push
  that `stamp-release-binary` makes to the `release-please--*` branch. It
  pushes to that branch, not to `main`, so ordinary protection rules are
  usually fine — confirm rather than assume if `main` has rules that apply
  to all branches.

**Optional, and only for Homebrew** (the release does not need them; see
[Homebrew](#homebrew)):

| Setting | Kind | Value |
| --- | --- | --- |
| `HOMEBREW_TAP_REPO` | repository **variable** | `<owner>/homebrew-<tap>` |
| `HOMEBREW_TAP_TOKEN` | repository **secret** | a token that can push to that repository |

## What to check after a release

In order, because each one is cheap and rules out the next one's failure
mode being a mystery.

1. **Nine assets on the release page.** Six binaries, the SBOM,
   `SHA256SUMS`, and `SHA256SUMS.cosign.bundle`. Fewer means a step failed
   after the upload started; `--clobber` makes a re-run safe.
2. **The manifest covers everything but itself.** `SHA256SUMS` should carry
   seven lines: six binaries and the SBOM. An asset absent from it is an
   asset no signature covers.
3. **Verify the signature the way a user is told to**, with the commands in
   [docs/install.md](./install.md) — not a variant of them. If the
   `--certificate-identity-regexp` in those instructions does not match the
   identity the run actually signed with, this is the step that says so,
   and it is the likeliest thing to be wrong the first time.
4. **`gh attestation verify <binary> --repo sosalejandro/atlas`** for at
   least one binary.
5. **Rebuild and compare.** `git checkout v0.14.0 && make build
   VERSION=v0.14.0` with the pinned toolchain, then check the digest
   against `SHA256SUMS`. This is the only check that establishes the
   cross-machine reproducibility claim; CI's `make repro` deliberately does
   not (see [When reproducibility breaks](#when-reproducibility-breaks)).
6. **Install through the consumer action** from a scratch workflow:
   `uses: sosalejandro/atlas/.github/actions/atlas@v0.14.0` with
   `args: version`. That exercises the download, the checksum check, the
   cosign verification and the provenance check in one go, which is the
   path an adopter takes.
7. **`atlas version` reports `v0.14.0`** — from the downloaded binary, not
   from a local build.

## What has never run

Every item here has been reviewed and none has executed. This section
exists so the first release is done with eyes open rather than discovered
in pieces.

**Cannot run outside GitHub Actions, at all:**

- **cosign keyless signing.** The signing identity is an OIDC token minted
  for the workflow run; there is no way to obtain one from a laptop, and
  nothing to substitute for it. `dist/SHA256SUMS.cosign.bundle` has never
  been produced.
- **The SLSA provenance attestation**, for the same reason.
- **`gh release create` / `upload`.** No release has ever been published by
  this pipeline.
- **SBOM generation.** `anchore/sbom-action` runs `syft`; the artifact
  `atlas_vX.Y.Z_sbom.spdx.json` has never been generated. Its *name* is
  exercised locally by standing a placeholder in for it, which proves
  `SHA256SUMS` covers it and the ordering is right, and proves nothing
  about the SBOM's contents.
- **The consumer action's download path.** `install.sh`'s runner-label
  mapping and asset naming are tested locally; the `curl`, the
  `sha256sum -c` against a downloaded manifest and the `cosign verify-blob`
  need a published release.
- **The Homebrew tap push.** No tap repository exists to push to.

**Runnable in principle, but not exercised by cutting v0.14.0:**

- **The `dispatch-release-build` bridge — the most important gap.** It only
  fires when release-please reports `release_created == true`, and cutting
  0.14.0 by hand does not go through release-please. So the one leg whose
  defect was found by review and fixed on paper stays unproven until
  **0.15.0**. `scripts_test.sh` asserts statically that the dispatch call
  exists and that `release.yml` accepts a `tag` input; that it actually
  starts a run is a claim nobody has tested. Watch it specifically on the
  0.15.0 merge, and if no `release.yml` run appears within a minute of the
  tag, that is this defect, not a slow queue.
- **`stamp-release-binary`.** It has run before — `535cbaf chore(release):
  stamp build metadata for v0.9.0` is its output — but not since, and its
  `sed` anchors on the exact declaration shape of all three vars.

  There is a live consequence. The reconciliation moved `Version` to
  `v0.14.0` and deliberately left `Commit` and `BuildDate` alone
  (`fba0d11`, `2026-05-24T01:31:52Z`), because only the version constant
  was in scope. `resolveBuildInfo()` treats any one non-sentinel value as
  "ldflags mode" and returns all three verbatim, so **a plain `go build`
  from `main` now reports `v0.14.0` paired with a commit from the 0.8.0
  era.** That is not new — the triple was already mismatched, which is what
  `internal/cli/buildinfo.go` warns about — and it does not touch released
  binaries, because `build.sh` passes `-X` for all three and those win. It
  is fixed for good the first time `stamp-release-binary` rewrites the
  block on a Release PR. Until then, do not read `commit` from a
  locally-built binary.
- **The binaries on macOS and Windows.** All six targets are cross-compiled
  from one Linux host and asserted cgo-free and trimpath'd by reading each
  artifact's embedded settings — which is evidence about the file, not
  about it running. CI's Windows leg is still advisory (issue #143).

**A local dry run cannot produce the release digests.** `build.sh` refuses
a toolchain other than `.github/scripts/toolchain.txt`'s pin for exactly
this reason. Anything built locally with
`ATLAS_SKIP_TOOLCHAIN_CHECK=1` on a different Go is byte-different from
what the release job will publish, by design — so a local `SHA256SUMS` is
useful for checking *shape and names*, never for comparing against a
published one.

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

## Asset names, and the four places that spell them

What a released file is called is written down four times:

| Where | Spells the name for |
| --- | --- |
| `lib.sh`'s `atlas_artifact_name` | what the publisher writes into `dist/` |
| `.github/actions/atlas/install.sh` | what the consumer action downloads |
| `brew-formula.sh` | what `brew install` fetches |
| `checksums.sh` | what the signature ends up covering |

They agreed only by convention. Every test that touched them compared one
side to a **literal in the test file**, which is not agreement — it is four
independent chances to drift, and the drift is invisible in CI and fatal in
a consumer's: a rename in `lib.sh` keeps every literal assertion green and
404s in somebody else's workflow, or installs nothing on whichever platform
nobody happened to try. The edge channel already learned this the expensive
way; the stable channel had the identical hole and no incident yet.

Three cases in `scripts_test.sh` now compare the ends against each other,
with no expected string that both sides do not have to produce:

- every shipped target's published name is the name the installer asks for
  (all six, via the GOOS/GOARCH ↔ `runner.os`/`runner.arch` table, which is
  the one mapping that genuinely has to be written down);
- the formula's download URLs are those same published names;
- every URL in the formula names a file `SHA256SUMS` covers — because an
  asset the manifest does not name is an asset nobody can verify, and a
  formula linking one would send a user to an unverifiable download with
  every check in this pipeline still green.

## What CI checks, and what blocks

Job names carry `(blocking)` or `(ADVISORY)`. A green check that gates
nothing teaches contributors that checks do not matter.

| Job | Blocks? | What it establishes |
| --- | --- | --- |
| `build + test + lint — {ubuntu,macos,windows}` | yes | The suite passes on all three OSes. `-race` on Linux and macOS only — the race detector needs cgo and a C toolchain, which this project deliberately does not require. |
| `reproducible build + cross-compile + release scripts` | yes | `make test-scripts-full`: the release scripts' own tests, the six-target cross-compile with its cgo-free assertion, the build-twice digest comparison, workflow YAML parsing, and the "every action is pinned to a SHA" check. The digest comparison is same-machine, same-toolchain — see [When reproducibility breaks](#when-reproducibility-breaks) for what that scopes it to. |
| `scan determinism` | yes | The suite from [docs/testing/determinism.md](./testing/determinism.md), run with `-count=1` so a cached pass cannot stand in for a measurement. |
| `version / manifest / changelog agree` | yes | The four version sources agree. Advisory until the [0.14.0 reconciliation](#why-0140), because it was failing; blocking since, because an advisory gate that passes is a check people learn to ignore. |

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
