# atlas version

`atlas version` prints two independent things about the binary in front of
you: the **stamps** it carries (which release it claims to be) and the
**build settings** the Go linker recorded (how it was actually compiled).
They come from different places and can disagree, which is the whole point
of printing both.

The root command's `--version` flag prints the one-line form. This
subcommand exists because that line answers "which atlas is this?" and says
nothing about "can I check that this binary is the one the release claims?"
— the question a supply-chain tool has to be able to answer about itself.

## Usage

```
atlas version [flags]
```

## Flags

| Flag                         | Default | Description                                        |
| ---------------------------- | ------- | -------------------------------------------------- |
| `--json` *(global)*          | off     | Emit the stable JSON envelope instead of text.      |
| `-v`, `--verbose` *(global)* | off     | Verbose human-readable output.                      |

## Output

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

Real output, captured on 2026-09-06 from a binary produced by
`make build-dev` in a clone at commit `189e713`. It is a development build,
rather than a release, and that is why the fields read as they do: the
version is `git describe` output because no tag names that commit, and
`built` is the commit's committer date rather than the wall clock. A release
binary prints the same shape with a `vX.Y.Z` version and the pinned
toolchain on the `go:` line.

## JSON fields

`atlas version --json` emits these inside the standard envelope's `result`.
Field names are a contract: a CI job that checks "the binary I downloaded is
the reproducible one" reads them.

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | string | The version stamp. See the caveat below. |
| `commit` | string | Short SHA stamp. |
| `build_date` | string | RFC 3339 build-date stamp. |
| `go_version` | string | Toolchain that compiled this binary. |
| `os` / `arch` | string | `GOOS` / `GOARCH` from the build settings. |
| `trimpath` | bool | Whether `-trimpath` was passed. |
| `cgo_enabled` | bool **or null** | Whether cgo was on. `null` means undetermined. |
| `reproducible_flags` | bool | `trimpath && cgo_enabled == false`. |

### `cgo_enabled` is three-valued

`null` is not `false`. When the linker's build-settings table is unreadable
or does not carry `CGO_ENABLED`, nothing is known, and the human output
prints `cgo: unknown`.

The asymmetry with `trimpath` is deliberate. For `-trimpath`, `false` is the
*unfavourable* answer — it denies the reproducibility claim — so defaulting
an unknown to `false` costs the binary the benefit of the doubt and is safe.
For cgo it is the other way round: `cgo: false` is exactly what a release
build looks like, so reporting an unreadable table as `false` would publish
the reassuring answer on no evidence at all. A consumer asserting "this
binary is cgo-free" must treat `null` as a failure to establish that.

`reproducible_flags` is `false` whenever `cgo_enabled` is `null`, for the
same reason.

### `reproducible_flags` is not a verification

It means the two *preconditions* hold: `-trimpath` was passed and cgo was
off. Without `-trimpath` the binary embeds the absolute path of the checkout
it was built from; a cgo build is tied to the host's C library. Both are
necessary for a byte-identical rebuild and neither is sufficient.

The only thing that proves reproducibility is rebuilding the same commit and
comparing digests. [docs/install.md](../install.md#reproducing-a-release-build)
carries that recipe.

## The version stamp is weaker evidence than it looks

release-please bakes literal `Version` / `Commit` / `BuildDate` values into
`internal/cli/root.go` on the release commit, and those stamps win over
everything else — the resolver returns them without consulting
`runtime/debug.ReadBuildInfo()`. So any build made from that source reports
them, including `go install …@main`, `go install …@<sha>` and a plain
`go build` in a clone.

Measured on a clone 39 commits past `v0.11.0`: `go run ./cmd/atlas version`
printed `atlas v0.13.0`, `commit fba0d11`, `built 2026-05-24T01:31:52Z` —
the last stamped release's values, not that clone's.

Consequences worth knowing:

- a `dev` version means the source carried no stamps, but a real-looking
  version does **not** mean you installed a release;
- on a binary built by `.github/scripts/build.sh` (`make build`, and every
  release) the stamps are passed with `-ldflags` and do describe that build.

If you need to know what a binary really is, use a release download and
verify its provenance attestation, which binds the artifact to a workflow
run and a commit rather than to a string in a file. See
[docs/install.md](../install.md#verifying-what-you-downloaded).

## Exit status

`0` always, absent an I/O error. `atlas version` reports; it does not gate.
