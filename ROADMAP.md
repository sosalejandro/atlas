# Roadmap

> Ordered by what produces information soonest, not by what is most
> interesting to build. The reasoning is in
> [docs/strategy/positioning.md](docs/strategy/positioning.md); this file is
> the sequence and the gates.
>
> **The premise:** the engineering has never been the constraint. This project
> has never been contradicted by a user, and every phase below is designed to
> arrange that as cheaply as possible.

## The one sentence

**Produces evidence. Everything else produces either an opinion or a link
somebody has to maintain.**

Incumbent traceability tools work requirements-down: a human writes
requirements and hand-links them to code. Manual links rot. This works
code-up — it derives the map and refuses to answer when the index is stale. A
derived link cannot rot, because there is nothing to maintain.

---

## Phase 0 — Make the first impression survivable

**3–5 days. Nothing else starts until this gate passes.**

| | |
| --- | --- |
| [#177](../../issues/177) | Onboard's provisional names are noise. Measured on a real repo: it proposes `root.no` and `root.bash`, and the excellent "what grunnr cannot see" section sits *below* them. |
| [#176](../../issues/176) | Rename. `ariga/grunnr` (8,733★, Go, binary `grunnr`) and MongoDB's Grunnr CLI (binary `grunnr`) both ship today — a user cannot have two on their PATH. Install failure at line one of the quickstart. |
| — | Tag the first release. The pipeline is complete and verified; it has never been run. |

**GATE — three engineers who did not build it, on repositories that are
applications rather than libraries. If none asks an unprompted follow-up
question, the framing is wrong and no further engineering reaches the
problem.**

---

## Phase 1 — The gate, because it is the reason to adopt

**4–6 weeks.**

`cov diff --fail-under` in somebody's CI is the whole wedge: it catches
untested code in a pull request, which is a problem teams already know they
have. Not the agent surface — an agent cannot consume a registry that does not
exist yet.

| | |
| --- | --- |
| [#162](../../issues/162) | The ordering half of determinism. `--stable` shipped; Go's map iteration still makes any formatter emit a different file each run. |
| [#175](../../issues/175) | Agents get edges with no provenance — `packages/mcp` has zero references to `Tier`. The surface built for the consumers who most need provenance omits it. |

**GATE — one team runs it in CI for two weeks without being asked to.**

---

## Phase 2 — The drift gate: the moat, and the screenshot

**6–10 weeks.**

| | |
| --- | --- |
| [#107](../../issues/107) | Minimal: one mermaid sequence diagram generated from the graph. Renders in GitHub, every IDE and any browser — the demo costs no viewer. |
| [#113](../../issues/113) | Go mermaid parser. CI cannot shell out to a JS runtime. |
| [#111](../../issues/111) | Anchors, `diagram verify`, drift as a failing check. |

**This is the launch asset.** *"Your architecture diagram fails CI when the
code moves"* describes something no competitor does: archify's own design doc
lists parsing and drift under "Skip"; LSP has no persistence to diff against;
Structurizr says outright it cannot know whether the model matches the code.

**GATE — Show HN, Tuesday–Thursday, 9am–12pm ET. Answer every comment for the
first hour. The documented failure modes are: something nobody can run, a
title that reads like an ad, and asking for upvotes.**

---

## Phase 3 — Reach, and the on-ramp

| | |
| --- | --- |
| [#170](../../issues/170) | SCIP persistence. One path, nine languages — C#, C, C++, Rust, Scala, Kotlin, PHP, Ruby, Java, Python, TypeScript all have indexers already. Stops this being Go-only. |
| [#118](../../issues/118) | Agent-proposed annotations. The annotation tax is the adoption risk; an agent proposing and a human approving turns "write them all" into "confirm these twelve". |
| [#160](../../issues/160) · [#163](../../issues/163) | The MCP surface, as distribution rather than as the product. MCP passed 17,000 public servers in year one — being one is a channel, not a position. |

---

## Phase 4 — Package the evidence

**After adoption, not before.**

Same product, different wrapper. [#161](../../issues/161) receipts become audit
artifacts; [#106](../../issues/106) the derived matrix;
[#117](../../issues/117) Gherkin binding; [#147](../../issues/147) published
claims that fail CI when they stop being true.

The compliance buyer pays the most and waits the longest. By this point the
tool is already inside the company, which is the only way that sale is short.

---

## Deliberately not doing

| | why |
| --- | --- |
| [#102](../../issues/102) desktop shell, [#124](../../issues/124) UI v2 | Seven screens for users who do not exist. Mermaid gives the demo free. |
| [#114](../../issues/114) SSH, [#115](../../issues/115) multi-repo | Scale problems at zero scale. |
| [#94](../../issues/94) OTel, [#95](../../issues/95) mutation signal | Good work. Not urgent until somebody trusts the existing numbers. |
| [#105](../../issues/105) Tier 2 sidecar | Dead. `github/stack-graphs` was archived September 2025 and it would have added zero languages over SCIP. |
| [#157](../../issues/157) | Measured at 6% against a predicted 2×, and it cost three unresolvable queries in our own data layer. Documented, parked. |
| Navigation verbs | Ceded to LSP, which does them better and is maintained by language vendors. Refusing to maintain a commodity is not a retreat. |

---

## Monetisation constraint

**Never add a licence check to the local binary.**
[docs/security.md](docs/security.md) leads with *"nothing leaves your
machine"*, enforced by `packages/redact/egress_test.go` failing the build on
any outbound network capability. A licence check is an outbound call, so a
paywall on the CLI would monetise by deleting exactly what the compliance
buyer pays for.

Charge for what genuinely needs a server: cross-repo aggregation, hosted
history, the signed evidence bundle. Open core, which is the dominant devtool
model.

---

## Stop conditions

Written down because eight months of sunk cost needs something to argue
against.

| Signal | Read it as |
| --- | --- |
| Three trials, nobody re-runs it | **Stop.** More engineering will not reach the problem. |
| Run once, praised, never again | The gate is missing or too hard to adopt. Fix Phase 1 first. |
| *"I didn't know that code was untested"* | **Go hard.** The remaining problem is distribution. |
| Someone asks for it as an MCP server | The agent path is live. Promote Phase 3. |
| A regulated team asks about evidence export | Phase 4 early. |
