# Installing atlas

atlas is a single static binary. It has no C dependencies — the store is
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), which is pure
Go — so every build ships with `CGO_ENABLED=0` and there is nothing to
install alongside it.

Optional runtime dependencies (`node` for the TypeScript scanner, `python3`
for the Python scanner) are described in the README; atlas warns and carries
on without them.

- [In GitHub Actions](#in-github-actions)
- [Download a release](#download-a-release)
- [Verifying what you downloaded](#verifying-what-you-downloaded)
- [`go install`](#go-install)
- [Homebrew](#homebrew)
- [Build from source](#build-from-source)
- [Reproducing a release build](#reproducing-a-release-build)
- [Channels](#channels)
- [Supported platforms](#supported-platforms)

## In GitHub Actions

```yaml
- uses: sosalejandro/atlas/.github/actions/atlas@v0.14.0
  with:
    args: audit --json
```

`v0.14.0` here stands for whichever release you want; pin a real one from
the [releases page](https://github.com/sosalejandro/atlas/releases). The
same goes for every version number in the examples below.

That installs the release matching the ref you pinned the action to,
verifies its checksum, its Sigstore signature and its SLSA provenance, puts
`atlas` on `PATH`, and runs it.

The version comes from the ref, so the binary cannot float away from the
action. If you pin the action by commit SHA instead of by tag, pass
`version:` explicitly — a SHA does not name a release, and the action will
tell you so rather than 404 on a URL built from a hash.

| Input | Default | Meaning |
| --- | --- | --- |
| `version` | the action's own ref | Release to install (`v0.14.0`, or `edge`) |
| `args` | `''` | Arguments for atlas. Empty installs only, leaving it on `PATH` for later steps |
| `working-directory` | `.` | Where to run |
| `verify` | `true` | Check the Sigstore signature |
| `verify-provenance` | `true` | Check the SLSA attestation with `gh attestation verify` |
| `repository` | `sosalejandro/atlas` | Where to download from (forks, testing) |

Install-only, for a workflow that runs several atlas commands:

```yaml
- uses: sosalejandro/atlas/.github/actions/atlas@v0.14.0
- run: |
    atlas scan
    atlas health --json > audit.json
```

## Download a release

Binaries are attached to every [release](https://github.com/sosalejandro/atlas/releases),
named `atlas_<version>_<os>_<arch>` (`.exe` on Windows). Alongside them:

| Asset | What it is |
| --- | --- |
| `SHA256SUMS` | One manifest covering every artifact in the release |
| `SHA256SUMS.cosign.bundle` | Keyless Sigstore signature over that manifest |
| `atlas_<version>_sbom.spdx.json` | SPDX SBOM of the source tree |

```bash
VERSION=v0.14.0
BASE="https://github.com/sosalejandro/atlas/releases/download/$VERSION"
curl -fsSLO "$BASE/atlas_${VERSION}_linux_amd64"
curl -fsSLO "$BASE/SHA256SUMS"
curl -fsSLO "$BASE/SHA256SUMS.cosign.bundle"
```

## Verifying what you downloaded

There are three separate questions, and they need three separate checks.
Doing only the first is common and answers almost nothing.

**1. Did the bytes arrive intact?**

```bash
grep " atlas_${VERSION}_linux_amd64$" SHA256SUMS | sha256sum -c -
```

This compares your download against the manifest you downloaded from the
same place. It catches a truncated transfer. It does not catch a manifest
that was replaced along with the binary.

**2. Did the release pipeline produce that manifest?**

```bash
cosign verify-blob \
  --bundle SHA256SUMS.cosign.bundle \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/sosalejandro/atlas/\.github/workflows/(release|edge)\.yml@refs/' \
  SHA256SUMS
```

The signature is keyless: no private key exists anywhere to be rotated or
leaked. The certificate is minted for the release workflow's OIDC identity,
used once, and recorded in Sigstore's public transparency log. The
`--certificate-identity-regexp` is the load-bearing part — without it, any
valid Sigstore certificate satisfies the check, which is to say any GitHub
Actions workflow on earth.

Releases are signed by cosign v3.1.3, pinned in `.github/workflows/release.yml`.

**3. Which workflow run and which commit built this binary?**

```bash
gh attestation verify atlas_${VERSION}_linux_amd64 --repo sosalejandro/atlas
```

This checks the SLSA provenance attestation, which binds the artifact to the
workflow, the commit and the run that produced it. The signature says "the
atlas release job signed this manifest"; the attestation says "this exact
binary came out of that run of that job on that commit".

**4. And, if you want to leave nothing to trust at all — rebuild it.**

## `go install`

```bash
go install github.com/sosalejandro/atlas/cmd/atlas@v0.14.0
```

Free, and how most Go developers will first try atlas. Note the trade-off:
`go install` compiles on your machine with your toolchain and your
environment, so the result is *not* byte-identical to the published release
and there is nothing to verify a signature against. What you get instead is
the module proxy's checksum-database guarantee that the source is the source
that was published under that tag.

What `atlas version` reports after a `go install` needs stating precisely,
because it is not what you might assume.

release-please bakes literal `Version` / `Commit` / `BuildDate` values into
`internal/cli/root.go` on the release commit. `resolveBuildInfo()` returns
those verbatim whenever any of them differs from its sentinel, and never
consults `runtime/debug.ReadBuildInfo()` when it does. So:

- **From a tag** (`@v0.14.0`): the version matches the tag. The `commit` and
  `built` values are the ones written into the source when the release PR
  was stamped — the release branch's tip and the time of stamping — not the
  tagged commit and not your build time.
- **From a branch or commit ref** (`@main`, `@<sha>`): you get the *same*
  baked values as the last stamped release, not `dev` and not the ref you
  asked for. A `go build` in a clone behaves the same way. Measured on a
  clone 39 commits past `v0.11.0`: `go run ./cmd/atlas version` printed
  `atlas v0.13.0`, `commit fba0d11`, `built 2026-05-24T01:31:52Z`.

So the version string from a non-tag install is *not* evidence of what you
installed. If you need that, install a tag, or use a release binary and
verify its provenance attestation, which is bound to a run and a commit
rather than to a string in a file.

The build flags differ too: `go install` passes neither `-trimpath` nor
`CGO_ENABLED=0`, so `atlas version` on such a build reports `trimpath:
false`, whatever cgo your machine defaults to, and
`reproducible_flags: false`. That is correct — it says this binary is not
one anybody can reproduce byte-for-byte.

## Homebrew

Every release generates a Homebrew formula from that release's signed
`SHA256SUMS`, so the digest `brew` checks and the digest the release
published cannot disagree. Publishing it needs a tap — a second repository
named `<owner>/homebrew-<tap>` — which is an operator decision rather than a
script, so **`brew install` is not available unless the maintainers have set
one up**; check the [releases page](https://github.com/sosalejandro/atlas/releases)
or the repository's README for a tap name before assuming it exists.

When no tap is configured the release run generates the formula anyway and
prints it in the job summary along with the two settings needed to publish
it (`HOMEBREW_TAP_REPO`, `HOMEBREW_TAP_TOKEN`). See
[docs/releasing.md](./releasing.md#homebrew).

## Build from source

```bash
git clone https://github.com/sosalejandro/atlas
cd atlas
make build-dev        # host platform, into dist/
```

`make build-dev` tolerates whatever Go you have. `make build` requires the
pinned toolchain (`.github/scripts/toolchain.txt`) and is the target to use
when you intend the output to match a release.

`atlas version` reports how the binary in front of you was built:

```
$ ./dist/atlas_v0.11.0-39-g189e713_linux_amd64 version
atlas v0.11.0-39-g189e713
  commit:      189e713
  built:       2026-09-06T07:44:19Z
  go:          go1.26.4-X:nodwarf5
  platform:    linux/amd64
  trimpath:    true
  cgo:         false
  build flags allow a reproducible rebuild: true
  (not a verification — to prove it, rebuild and compare digests: docs/install.md)
```

That is real output, captured on 2026-09-06 from a binary produced by
`make build-dev` in a clone at commit `189e713` (the build's own log lines
are omitted; nothing else is). It is a **development** build, not a release,
which is why every field looks the way it does:

- the version is `git describe` output (39 commits past `v0.11.0`), because
  no tag names this commit;
- `built` is the commit's committer date, not the wall clock — that is the
  `SOURCE_DATE_EPOCH` rule that makes rebuilds match;
- the `go:` line shows whichever toolchain compiled the binary, which is
  precisely why it is printed. A release build shows the pinned version from
  `.github/scripts/toolchain.txt`.

A release binary prints the same shape with a `vX.Y.Z` version. The numbers
above are one machine's, on one commit; do not read them as a release's.

`atlas version --json` emits the same fields in the standard envelope:
`version`, `commit`, `build_date`, `go_version`, `os`, `arch`, `trimpath`,
`cgo_enabled`, `reproducible_flags`.

`reproducible_flags` means the two preconditions hold — `-trimpath` was
passed and cgo was off. It does **not** mean anything has been verified.
Only the next section verifies anything.

`cgo_enabled` is a three-valued field: `true`, `false`, or `null` when the
binary's build-settings table did not say. `null` is not `false`. For cgo,
`false` is the *favourable* answer — it is what a release build looks like —
so reporting an unknown as `false` would hand out the reassuring answer on
no evidence. In the human output the same state prints as `cgo: unknown`.
`trimpath` has no such problem: there `false` denies the claim, so an
unknown is safely reported as `false`.

## Reproducing a release build

Every release is built so that anyone can produce the same bytes:

```bash
git clone https://github.com/sosalejandro/atlas
cd atlas
git checkout v0.14.0
make build VERSION=v0.14.0        # or: VERSION=v0.14.0 .github/scripts/build.sh
sha256sum dist/atlas_v0.14.0_linux_amd64
```

Compare that digest against the line for the same filename in the release's
`SHA256SUMS`. They should be identical.

For a different target, set `GOOS`/`GOARCH`:

```bash
GOOS=darwin GOARCH=arm64 make build VERSION=v0.14.0
```

What makes this work, and what will break it:

- **The timestamp is a function of the commit, not of the clock.**
  `SOURCE_DATE_EPOCH` defaults to the tagged commit's committer date, so you
  and the release runner derive the same value without coordinating.
- **`-trimpath`** keeps the absolute path of your checkout out of the
  binary. Without it, cloning to a different directory changes the bytes.
- **`-buildvcs=false`** keeps the VCS revision, commit time and dirty flag
  out of the binary, so a build from a source tarball matches a build from a
  git clone. The version, commit and date arrive via `-ldflags` instead.
- **The Go toolchain is pinned** in `.github/scripts/toolchain.txt`. Two Go
  patch releases do not produce the same bytes; `build.sh` refuses to run
  against a different one unless you set `ATLAS_SKIP_TOOLCHAIN_CHECK=1`, in
  which case the output will not match the published digests and it says so.
- **Go environment variables are pinned, not inherited.** `GOFLAGS`,
  `GOEXPERIMENT`, `GODEBUG` and the micro-architecture levels `GOAMD64` /
  `GOARM64` are all set or cleared by the build script. A machine with
  `GOAMD64=v3` exported would otherwise produce a different, faster binary
  that nobody would notice was different.

To check the property itself rather than one build of it:

```bash
make repro
```

That builds the same source twice — different output directory, different
`TMPDIR`, different `GOMAXPROCS`, second build from a copy of the tree at a
different absolute path with no `.git` present — and compares digests. CI
runs it on every push, and the release job runs it across all six targets
before publishing.

**What that check does and does not establish.** Both builds run on one
machine with one Go toolchain, so what it proves is that the output does not
depend on the output directory, the temp directory, the compiler's
parallelism, or the absolute path of the checkout — the four things that
break reproducible builds most often, and the last of which a same-directory
rebuild silently passes. It says nothing on its own about a *different*
machine, OS, filesystem or toolchain. The claim that you get the same bytes
is the recipe above, run by you: build the tag yourself with the pinned
toolchain and compare your digest against `SHA256SUMS`. That is the check
this section exists for, and it is the only one performed by someone who
does not have to trust us.

## Channels

| | **stable** | **edge** |
| --- | --- | --- |
| Tag | `vX.Y.Z` | `edge` (force-moved) |
| Cut by | a git tag, via release-please | every commit to `main` |
| Asset names | `atlas_vX.Y.Z_<os>_<arch>` | `atlas_edge_<os>_<arch>` |
| Reproducible, cgo-free, signed, SBOM, provenance | yes | yes |
| CLI flags and `--json` envelope | additive within a major version | may change without notice |
| Store schema | migrated forward | may change without notice |
| Previous builds retrievable | yes, every release stays | no, one rolling tag |

Every row of that table is a property of `.github/workflows/edge.yml`, not a
description of intent: the signing, SBOM, checksum and attestation steps are
the same steps `release.yml` runs, and `.github/scripts/scripts_test.sh`
fails if the attestation step disappears from the edge workflow while this
table still promises it.

Both channels come out of the same pipeline with the same verification
story. The difference is entirely about compatibility, not about build
quality: edge is not a place where less careful work is shipped, it is the
same work without a promise attached.

Edge version strings look like `v0.13.0-7-gabc1234` — the last release, how
many commits past it, and which commit. Under semver precedence that sorts
*below* `v0.13.0` even though it is newer code. That is a known property of
the convention and the reason edge carries no ordering promise relative to
stable.

That string is what `atlas version` prints; it is **not** in the filename.
Edge assets are named `atlas_edge_<os>_<arch>` and stay at that name across
commits, because a rolling channel needs a download URL that does not move:

```bash
BASE="https://github.com/sosalejandro/atlas/releases/download/edge"
curl -fsSLO "$BASE/atlas_edge_linux_amd64"
curl -fsSLO "$BASE/SHA256SUMS"
grep " atlas_edge_linux_amd64$" SHA256SUMS | sha256sum -c -
```

The signature and provenance checks are the same two commands as for a
stable release. In the action, `with: { version: edge }` installs it.

Use stable in CI. Use edge to find out whether a fix works before the next
release.

## Supported platforms

| OS | amd64 | arm64 |
| --- | --- | --- |
| linux | yes | yes |
| darwin | yes | yes |
| windows | yes | yes |

All six are cross-compiled from a single Linux runner, which is only
possible because nothing in the dependency tree needs cgo. The release job
asserts that property on each artifact after building it, by reading back
the build settings the linker embedded — so the day a cgo dependency
appears, the release fails rather than quietly shipping three platforms.
