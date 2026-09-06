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
    atlas audit --json > audit.json
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

`atlas --version` still reports a real version here: the release commit
carries the stamps in the source, so a tagged install does not report `dev`.
An install from a branch or commit ref will.

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
atlas v0.13.0
  commit:      7e9ccc2
  built:       2026-09-06T06:28:57Z
  go:          go1.26.4-X:nodwarf5
  platform:    linux/amd64
  trimpath:    true
  cgo:         false
  build flags allow a reproducible rebuild: true
  (not a verification — to prove it, rebuild and compare digests: docs/install.md)
```

(Captured from a local `make build-dev`; the `go:` line shows whichever
toolchain compiled the binary, which is precisely why it is printed. A
release build shows the pinned version.)

`atlas version --json` emits the same fields in the standard envelope:
`version`, `commit`, `build_date`, `go_version`, `os`, `arch`, `trimpath`,
`cgo_enabled`, `reproducible_flags`.

`reproducible_flags` means the two preconditions hold — `-trimpath` was
passed and cgo was off. It does **not** mean anything has been verified.
Only the next section verifies anything.

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

## Channels

| | **stable** | **edge** |
| --- | --- | --- |
| Tag | `vX.Y.Z` | `edge` (force-moved) |
| Cut by | a git tag, via release-please | every commit to `main` |
| Reproducible, cgo-free, signed, SBOM, provenance | yes | yes |
| CLI flags and `--json` envelope | additive within a major version | may change without notice |
| Store schema | migrated forward | may change without notice |
| Previous builds retrievable | yes, every release stays | no, one rolling tag |

Both channels come out of the same pipeline with the same verification
story. The difference is entirely about compatibility, not about build
quality: edge is not a place where less careful work is shipped, it is the
same work without a promise attached.

Edge version strings look like `v0.13.0-7-gabc1234` — the last release, how
many commits past it, and which commit. Under semver precedence that sorts
*below* `v0.13.0` even though it is newer code. That is a known property of
the convention and the reason edge carries no ordering promise relative to
stable.

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
