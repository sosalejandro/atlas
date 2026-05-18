# Horizon 2 — deferred Atlas work

This file accumulates the items intentionally left out of a given phase so
we don't lose track of them between sprints. Each entry records WHY it was
deferred (so the deferral decision is auditable) and the rough effort tier.

## Phase 6f — parser-based EDA pattern recognisers (landed 2026-05-18)

All three Phase 6f recognisers landed (`outbox-append`,
`event-recorder-embed`, `canonical-service`); none of them blew through
the 1.5-day calibration budget, so nothing was DROPPED from the phase.

Items moved to Horizon 2 from the Phase 6f scope per the original spec:

### Saga step ordering recogniser

Detect saga / process-manager step shapes — the cross-handler ordering of
state transitions in a workflow. Requires:

- multi-aggregate awareness (a saga touches multiple aggregates)
- temporal sequencing (idempotency keys, retry markers)
- recognition of canonical step-completion event names

Effort: moderate-to-large. Cannot reuse the simple struct-embed +
single-method shape of Phase 6f's recognisers — needs cross-file walk and a
notion of "what happens next" that the call-graph alone doesn't carry.

### Consumer subscription recogniser (Redis stream specific)

Detect `XREADGROUP`-style consumer registrations and pair them with the
producer's `XADD`. The producer side is partly visible today via
`outbox-append`, but the consumer side has no recogniser at all. Requires:

- recognising consumer-group registration patterns
- pairing consumer ↔ stream name ↔ producer

Effort: moderate. The shapes are stable in nutrition-v2-go's
`shared/events` adapter, so a recogniser could be tightly scoped.

### LSP-style annotation suggestions

When a recogniser identifies a struct as an aggregate root structurally
but the struct has no `@atlas:feature` annotation, surface a suggested
annotation diff. This is the natural sibling to Phase 6e's
annotation-driven awareness — Phase 6f detects, Horizon 3 fixes.

Effort: small-to-moderate. Needs an LSP harness wired up; the underlying
data (recogniser hits + missing annotations) is already in the DB after
Phase 6f.

### Cross-file pattern chains

Detect a full handler → service → aggregate chain in a single recogniser.
Today the three recognisers each see only ONE construct (call site,
struct, or method), so a chain has to be reassembled from edges in the
audit layer. A native cross-file recogniser would short-circuit that.

Effort: moderate. Requires resolving the surrogate IDs of related symbols
inside the recogniser — currently each match carries a single SymbolID.
