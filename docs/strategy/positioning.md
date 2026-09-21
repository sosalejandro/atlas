# Positioning

> What this project is, who it is for, and the one sentence that survives
> every competitor. Written September 2026, after examining four competitors
> in their source rather than their marketing.
>
> This document exists because the engineering was never the constraint. The
> constraint is that this project has never been contradicted by a user, and
> most of the decisions below are designed to arrange that cheaply.

## The sentence

**Grunnr produces evidence. Everything else produces either an opinion or a
link somebody has to maintain.**

That is the only positioning found that survives all four competitors at
once, and it is the one this codebase already implements — resolution tiers,
`indexfresh`, the unattributed counters, the `0/1/2/3` exit contract, and
`--stable`.

## Code-up, not requirements-down

Every incumbent in traceability works **requirements-down**: a human writes
requirements in Jama, Polarion, Visure or Codebeamer, and hand-links them to
code and tests. That link is authored by a person and maintained by a person.

Manual links rot. It is why "automated RTM generation" is every vendor's
headline feature, and why teams still assign somebody to maintain the matrix
anyway.

**This project works code-up.** It derives the map from the code and refuses
to answer when the index is stale. A derived link cannot rot, because there is
nothing to maintain.

That is the whole product. Everything else is implementation.

## What we are not competing with

| | What it does better than us | Why it cannot do our job |
| --- | --- | --- |
| **LSP** (gopls et al.) | Definitions, references, call hierarchy — live, type-accurate, free, maintained by language vendors | Every operation needs `file + line + character`. No persistence, no aggregate, no feature concept, no coverage, no exit code to gate on |
| **SCIP / Sourcegraph** | Cross-language indexes for a dozen languages | No notion of a feature, no coverage, and no statement of what it could not establish |
| **archify** | Beautiful deterministic diagrams | No parser, no repo crawler, no drift detection — its own design doc lists these under "Skip" |
| **Codecov / SonarQube** | Coverage reporting at scale, PR integration | Coverage per file. No concept of a capability, so they cannot tell you a *feature* is untested |

**The consequence, stated plainly:** the indexing layer is a commodity and
getting more so. We should consume LSP and SCIP rather than compete with them
(#132, #170), and stop investing in navigation features that LSP does better.
Refusing to maintain a commodity is not a retreat.

## Who pays, and in what order

Three markets were scored. The compliance market wins on willingness to pay,
product fit and defensibility, and loses only on sales friction — which is the
one problem open-source distribution is good at reducing.

**So: give away the gate, sell the evidence.**

A developer adopts `grunnr cov diff --fail-under` because it catches untested
code in their pull request — a problem they already know they have. Later
their company needs an audit trail, and the tool producing it is already in
their CI. That is bottom-up adoption triggering a top-down conversation, which
is the documented devtool motion, pointed at a market that already pays for a
worse, hand-maintained version of this.

Do **not** lead with compliance. Do **not** lead with "MCP server" — MCP
passed 17,000 public servers in its first year, so that is a distribution
channel, not a position.

## The monetisation constraint

**Never add a licence check to the local binary.**

`docs/security.md` leads with *"nothing leaves your machine"*, enforced by
`packages/redact/egress_test.go` failing the build on any outbound network
capability. A licence check is an outbound call. A paywall on the CLI or the
MCP server therefore means either phone-home licensing — which breaks the
guarantee, fails the test, and destroys exactly what the compliance buyer is
paying for — or offline keys, which are crackable against public source.

Charge instead for what genuinely needs a server, where the charge is natural
and enforceable and the local binary's guarantee is untouched:

- cross-repo aggregation (#115)
- hosted history and trends — the audit trail over time
- the signed evidence bundle with a provenance chain

This is open core, which is the dominant devtool model. It also matches what
open-source buyers ask for — lower lock-in, budget predictability, SBOMs, data
sovereignty — all of which this project already has.

## Stop conditions

Committing to these in advance is the only defence against sunk cost.

| Signal | Read it as |
| --- | --- |
| Three trials, nobody re-runs it | **Stop.** The framing is wrong and more engineering will not reach the problem. |
| Run once, praised, never again | The gate is missing or too hard to adopt. Fix that before anything else. |
| *"I didn't know that code was untested"* | **Go hard.** The product is real; the remaining problem is distribution. |
| Someone asks for it as an MCP server | The agent path is live. Promote it. |
| A regulated team asks about evidence export | The compliance buyer found us early. Package it. |

## What this changes about the roadmap

1. **The gate is the wedge**, not the agent surface and not the diagrams.
2. **Diagrams are the demo and the moat, in that order** — differentiated by
   drift detection (#111), not by the pictures. Build the gate; mermaid
   renders in GitHub, so the demo costs no viewer.
3. **Agents are the on-ramp, not the consumer.** The registry has to exist
   before an agent can use it, but an agent can *propose* the annotations
   (#118), which turns the adoption cost from "write them all" into "confirm
   these twelve".
4. **Deferred until somebody asks:** the desktop shell (#102), UI v2 (#124),
   SSH (#114), multi-repo (#115), OTel (#94), mutation signal (#95).

## Sources

Market research, September 2026: MCP ecosystem scale; devtool GTM and Show HN
mechanics; commercial open-source monetisation models; IEC 62304 / DO-178C
traceability vendor landscape; the small-team-versus-enterprise split in
coverage tooling. Competitor findings come from reading the projects' source.
