# Security policy

## Reporting a vulnerability

Report privately, through GitHub's private vulnerability reporting:

**<https://github.com/sosalejandro/atlas/security/advisories/new>**

Please do not open a public issue for a vulnerability, and please do not
post a working exploit anywhere public before a fix is available.

A useful report includes: what an attacker can do, the minimal steps that
demonstrate it, the atlas version (`atlas --version`), and the platform. If
you are unsure whether something counts, report it — deciding that is our
job, not yours.

## What to expect

Atlas is maintained by a small team. These are intentions, not contractual
guarantees, and they are written down so you know what silence means:

| Stage | Target |
| --- | --- |
| Acknowledgement that the report was received | 3 working days |
| An initial assessment: is it a vulnerability, and how severe | 10 working days |
| A fix or a documented mitigation for a confirmed high-severity issue | 30 days from the assessment |

If you have not heard back within the acknowledgement window, assume the
report was lost rather than ignored, and re-send it.

You will be credited in the advisory by the name or handle you ask for, or
not at all if you prefer. There is no bug bounty.

## Supported versions

Fixes land on the latest tagged release. There are no long-term support
branches: if you are running an older tag, the remedy is to upgrade. Atlas is
pre-1.0, and this will change when it stops being.

## Coordinated disclosure

We will publish a GitHub Security Advisory when a fix ships, describing the
issue, the affected versions and the fixed version. If a report is not
acted on within 90 days of acknowledgement, you are free to disclose it
publicly; tell us first so the advisory and your disclosure do not
contradict each other.

## Scope

Atlas is a local CLI. It reads your source code, writes a local SQLite
database, and — as of this writing — makes no network calls at all. See
[docs/security.md](docs/security.md) for the full data-handling statement and
for the test that enforces the no-network claim.

**In scope**

- Anything that makes atlas write outside the paths documented in
  [docs/security.md §2](docs/security.md), or read outside the scan root.
  Three verbs write into the working tree by design —
  `migrate-annotations --apply`, `cov shim init` and
  `onboard promote --apply` — and a write from any other verb, or from those
  three to a path they do not document, is a finding.
- Command injection through a repository atlas scans, a config file, a
  coverage report, or a flag value. On its own initiative atlas launches
  `git`, `go`, `node` and `python`; `atlas cov run -- <command>` additionally
  runs the argv you hand it, which is the point of that verb and not a
  finding. Every invocation is argv-style and none goes through a shell, so a
  way to break *that* — to get a value out of scanned content or config into
  a shell, or into the argv of a program you did not name — is a
  vulnerability.
- Any path by which atlas transmits data off the machine.
- Escapes from the MCP server's read-only boundary (`atlas mcp`): a tool call
  that writes to the store, reads an arbitrary path, executes anything, or
  returns unbounded output.
- SQL injection into the state database from indexed content — a symbol
  name, a file path, or query text that changes what atlas executes.
- A defect in `atlas security` that causes it to *understate* what the
  database holds, what leaves it, or what atlas writes and launches: a table
  or column omitted from the inventory, an export surface missing from the
  catalogue or declaring fewer content classes than it carries, or a detected
  secret reported in a way that discloses the secret.
- Anything that makes `atlas security` (or `atlas security redact --dry-run`)
  modify the database it is describing. Both open it read-only and neither
  migrates it; see [docs/security.md §6](docs/security.md).

**Out of scope**

- Credentials atlas found in your own source. That is your source's problem.
  The ingest paths redact what they can before storing it and
  `atlas security redact` cleans the rest, but neither touches your repository
  and you still have to rotate the credential. A gap in the detection
  *heuristics*, or a redactable column not yet covered at ingest, is a bug
  report rather than a vulnerability — the detector is documented as
  deliberately conservative and incomplete, and
  [docs/security.md §5](docs/security.md) lists which columns are covered
  where.
- The absence of encryption at rest. The state database is a plain SQLite
  file with your umask's permissions, documented as such. If the machine is
  untrusted, so is the database.
- What your MCP client does with the data `atlas mcp` hands it. That is a
  property of your client and its model provider, not of atlas.
- Vulnerabilities in `go`, `git`, `node`, `python` or your own test suite,
  which atlas runs but does not ship.
- Findings from an automated scanner with no demonstrated impact.

## Dependencies

Atlas is cgo-free by design (`modernc.org/sqlite`), so a release binary
carries no C dependency of its own. If you find a vulnerable Go dependency,
report it here rather than upstream-only; we would rather bump it and say so
than have you assume we saw the advisory.

Signing, provenance attestation and an SBOM for release artifacts are tracked
separately and are not in place yet. Until they are, the honest statement is
that a release binary's provenance rests on GitHub Actions and this
repository's history, and nothing stronger.
