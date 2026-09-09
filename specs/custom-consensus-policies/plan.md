# Custom Consensus Policies Engine — Implementation Plan

**Status**: Draft — for review
**Last revised**: 2026-09-09
**Branch**: `feat/custom-consensus-policies-spec` (spec PR); implementation on a
follow-on branch
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; the selector engine and MissingData waiver are the headline
correctness pieces.

---

## Locked decisions

| Decision | Choice |
|----------|--------|
| When JS runs | **Pre-round** only — selects which policy to run |
| What JS returns | Policy **name** (primary); inline object as escape hatch |
| Auth / roles | Inside `customPolicy.evalFunction` via `ctx.user` — no separate claim→policy allowlist |
| Round grading | Stays in declarative `ConsensusPolicyConfig` / executor |
| Mid-round switch | **Never** — fail-visible under the selected policy |
| Historical safety net | `waiveAgreementOnMissingData` on `requiredParticipants[]` |
| Empty/null pruning | Spec for v1.1 (`waiveAgreementOnEmptyOutsideRetention`); **not** in v1 milestones |
| `blockAvailability` | Existing config + `EvmAssertBlockAvailability` — no new dynamic model |
| Fallback policy shape | Plain `{ maxParticipants, agreementThreshold }` — no tag quotas |
| Decision cache | Auto-derived keys from accessed paths; gen-counter invalidation |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/custom-consensus-policies/feature.md`
- [x] Write this plan
- [ ] Land on `andreclaro/erpc` for review; link from #1088

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Standalone selector engine

Package: `internal/consensus/policy/` — no imports from `consensus/` executor.

1. **Sobek pool** — compile once at startup (`sobek.Program`); pre-warm VMs;
   borrow per eval; `evalTimeout` hard cap.
2. **`EvalContext`** types — request, user, upstream refs (id, tags, health,
   blockAvailability), network.
3. **Stdlib v1** — `withTag`, `healthy`, `anyPunished`, `canServeBlock` (wraps
   `EvmAssertBlockAvailability`), `hasRole`.
4. **API** — `Compile(js) (*Policy, error)`, `Evaluate(ctx) (name string, err error)`.
   Empty / null → `""` (default). Unknown-name resolution is the caller's job.
5. **Unit tests first on fallthrough** — nil user, empty upstreams, empty
   eval, timeout, throw → error (caller fails closed). Then happy-path name /
   object returns and each stdlib helper.

**Acceptance**: `go test ./internal/consensus/policy/...` green; package has
zero imports from `consensus/` executor.

---

## Phase 2 — Config schema

1. Extend consensus config with `policies map[string]*ConsensusPolicyConfig`
   and `customPolicy { evalFunction, evalTimeout }`.
2. Add `waiveAgreementOnMissingData bool` on `ConsensusRequiredParticipant`
   (default false, opt-in).
3. Validation: named policies validate as today's
   `ConsensusPolicyConfig.Validate()`; `customPolicy.evalFunction` smoke-compiles;
   unknown cross-refs rejected at load where statically knowable.
4. Defaults: inline `consensus:` with no `policies` → anonymous default policy.
   `evalTimeout` default (suggest `50ms`, match selection-policy order of
   magnitude).
5. Tygo regen for TypeScript config types.

**Acceptance**: existing configs load unchanged; a `policies` + `customPolicy`
fixture validates and compiles.

---

## Phase 3 — Executor wiring + MissingData waiver

1. **Pre-round resolution** in the consensus path: build `EvalContext` from
   auth + upstream registry + request; evaluate (or cache lookup); resolve
   name → config; run existing executor under that config.
2. **Fail closed** — eval error / timeout / unknown name → default policy +
   log.
3. **Header + metric** — `X-eRPC-Consensus-Policy`, `consensus_policy` label
   (lifted from #1041).
4. **Waiver** in `enforceWinnerComposition`: if
   `waiveAgreementOnMissingData` and every tag-matching participant returned
   `ErrEndpointMissingData`, skip that quota. Emit
   `consensus_composition_waived_total{tag,reason="missing_data"}` + log.
5. **Characterization tests** — one per edge-matrix row in feature.md §7.2
   (v1 rows only; null-shape stays dispute). Existing consensus tests run
   unchanged against the default policy (zero regression).

**Acceptance**: UC1 (standard mixed-node), UC2 (role-gated fallback), UC3
(historical via waiver) pass as config-level fixtures; no mid-round switch
behavior exists.

---

## Phase 4 — Auth + health plumbing

1. Expose JWT roles/claims on `ctx.user` (reuse existing auth resolution).
2. Expose healthy vs punished/sitout distinctly on upstream health refs (R7).
3. `blockNumber` extraction for eval ctx (numeric request params).
4. Fallback eval refuse-to-fire when `anyPunished()` is true — tested.

**Acceptance**: punished internals never select `fallback`; unauthorized
callers never get `fallback` / `generous-dev`.

---

## Phase 5 — Decision cache

1. Tracking-ctx first eval per (network, method) to derive key schema.
2. Health-tracker generation counter invalidation.
3. `blockNumber` bucketing by `blockAvailability` boundaries + cardinality
   guard.
4. Uncacheable detection (time/random).
5. Metrics: hits / misses / bypassed{reason}.
6. Fallback to operator-declared `cacheKeys` only if getter tracking proves
   impractical (STOP and report — do not invent a third key model).

**Acceptance**: role-only evals hit cache across requests; health state
transitions invalidate; flapping upstream tradeoff documented in metric
bypass/miss rates.

---

## Phase 6 — Docs + E2E

1. Docs page under `docs/pages/config/` for `consensus.policies` /
   `customPolicy` / waiver fields (agent-first: Config schema table, Edge
   cases, Observability — match `failsafe/hedge.mdx` exemplar).
2. E2E fixtures for UC1–UC3 + matrix rows.
3. Update #1088 with implementation status.

**Acceptance**: docs build; E2E green; issue updated.

---

## Out of scope for v1 (explicit)

- `waiveAgreementOnEmptyOutsideRetention` + `blockEvidenceFields` (v1.1 —
  build when dispute traces show the null shape).
- Mid-round policy switching / post-round JS grading.
- #1069 bounded-deviation value logic.
- Separate claim→policy allowlist outside JS.

---

## Risks / watch-items

- **Punished vs unhealthy indistinguishability** — if health refs collapse
  sitout into "unhealthy", fallback becomes an attacker-forced downgrade. R7
  is load-bearing; block Phase 4 on a distinct signal.
- **Decision-cache key explosion** — freeform JS over `blockNumber` without
  bucketing. Cardinality guard must disable caching, not OOM.
- **Sobek getter tracking** — if impractical, STOP and fall back to declared
  `cacheKeys`; do not ship a half-broken auto-key.
- **Waiver over-breadth** — waive only when *all* matching participants
  returned MissingData; a single value vote holds the quota (security).
- **Scope creep into post-round grading** — any temptation to expose
  `valueGroups` / `agreeing` to JS is a different product; reject and point
  at #1069 / executor config.
