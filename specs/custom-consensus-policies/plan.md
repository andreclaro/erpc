# Custom Consensus Policies Engine — Implementation Plan

**Status**: Proposed phased delivery (for core maintainer review)
**Last revised**: 2026-09-16
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; the selector engine and MissingData waiver are the headline
correctness pieces. Phase checkboxes are the proposed delivery sequence — not
a status report of a fork.

Lessons from an internal prototype are folded into acceptance criteria and
risks (waiver ≥ `minAgreement`, wait-cap seal, hedge empty-as-slot-vote,
PreferNonEmpty count gate, config repetition → § enhancements).

---

## Locked decisions

| Decision | Choice |
|----------|--------|
| When JS runs | **Pre-round** only — selects which policy to run |
| What JS returns | Policy **name** only; no inline object in v1 |
| `"default"` string | Intentional select of **inline** policy (like `null`); not `unknown_name`. `policies["default"]` still reserved / rejected |
| Auth / roles | Inside `customPolicy.evalFunction` via `ctx.user` — no separate claim→policy allowlist. IdP claim→`Roles` mapping is an **open topic** (feature.md §13) |
| Round grading | Stays in declarative `ConsensusPolicyConfig` / executor |
| Mid-round switch | **Never** — fail-visible under the selected policy |
| Historical safety net | Role-gated: default (no waiver), `standard-waive-missing` (waiver), `historical` (external-only for known-old blocks) |
| Empty/null pruning | **v1.1** (`waiveAgreementOnEmptyOutsideRetention` + `retentionBlocks` + `blockEvidenceFields`); block proof by avail lower bound then chain-head depth |
| Waiver abstention floor | **≥ `minAgreement`** distinct tag-matching abstentions; matching data / non-empty holds; sibling transport errors do not block |
| `blockAvailability` | Existing config + `EvmAssertBlockAvailability` — no new dynamic model |
| Fallback policy shape | Plain `{ maxParticipants, agreementThreshold }` — no tag quotas (v1 copy hygiene explicitly until `extends`) |
| Decision cache | **Not in v1** — add only if measured eval cost forces it |
| Sitout state | Moved to `health.Tracker` (per-class cordon flags + rate limiter) so selector reads it without importing `consensus/`; exporter stays per-policy |
| Header + metric | Reimplemented here (`X-ERPC-Consensus-Policy`, `consensus_policy` label); not dependent on #1041 |
| Default policy | Must be the strictest; fail-closed target = inline `consensus:` block |
| Policy inheritance | **Not in MVP** — post-MVP one-level `extends` (feature.md §12) |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/custom-consensus-policies/feature.md`
- [x] Write this plan
- [ ] Land on `andreclaro/erpc` for review; link from #1088

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Standalone selector engine

Package: `internal/consensus/policy/` — no imports from `consensus/` executor.

1. **Sobek pool** — compile once at startup (`sobek.Program`); pre-warm 8 VMs;
   borrow per eval; `evalTimeout` hard cap. Pool exhaustion fails closed to
   default policy + `erpc_consensus_policy_eval_bypassed_total{reason="pool_exhausted"}`.
2. **`EvalContext`** types — request, user, upstream refs (id, tags, health,
   blockAvailability), network.
3. **Stdlib v1** — `withTag`, `healthy`, `anyPunished`, `anyOperatorCordon`,
   `allUnavailable` (fail-closed availability whitelist; **false on an empty
   set**), `canServeBlock` (wraps `EvmAssertBlockAvailability`), `hasRole`.
4. **API** — `Compile(js) (*Policy, error)`, `Evaluate(ctx) (name string, err error)`.
   Empty / null → `""` (default). Unknown-name resolution is the caller's job.
5. **Sandbox** — bare Sobek runtime (no `env` / `process.env` / `console`);
   one-time expression eval at pool build bounded by `evalTimeout`.
6. **Micro-benchmark** — steady-state / cold vs warm / concurrent / timeout.
   Decision rule: p99 steady-state ≪ 1ms → decision cache stays unbuilt.
7. **Unit tests first on fallthrough** — nil user, empty upstreams, empty
   eval, timeout, throw → error (caller fails closed). Then happy-path name
   returns and each stdlib helper.

**Acceptance**: `go test ./internal/consensus/policy/...` green; package has
zero imports from `consensus/` executor; benchmark result posted.

---

## Phase 2 — Config schema

1. Extend consensus config with `policies map[string]*ConsensusPolicyConfig`
   and `customPolicy { evalFunction, evalTimeout }`.
2. Add `waiveAgreementOnMissingData bool` on `ConsensusRequiredParticipant`
   (default false, opt-in).
3. Validation: named policies validate as today's
   `ConsensusPolicyConfig.Validate()`; `customPolicy.evalFunction` smoke-compiles;
   reject reserved name `default`, nested `customPolicy`/`policies` on a named
   policy, waiver configs without a never-waivable floor.
4. Defaults: inline `consensus:` with no `policies` → anonymous default policy.
   `evalTimeout` default `50ms`.
5. Tygo regen for TypeScript config types.

**Acceptance**: existing configs load unchanged; a `policies` + `customPolicy`
fixture validates and compiles.

---

## Phase 3 — MissingData waiver

1. **Waiver** in `enforceWinnerComposition`: if `waiveAgreementOnMissingData`
   and **≥ `minAgreement`** distinct tag-matching participants returned
   `ErrEndpointMissingData` **and none returned data**, skip that quota
   (sibling transport errors do not block; a matching data vote holds it).
   Emit `erpc_consensus_composition_waived_total{…,reason="missing_data"}` + log.
2. **Round-complete + wait-cap seal** — composition disputes do **not**
   short-circuit while `hasRemaining`. When wait-cap fires, **seal** the
   collection and re-run `determineWinner` so waivers evaluate with cancelled
   slots; `fireAndForget` does not seal.
3. **Load-time validation** — R9 / R10 (never-waivable floor; S3 path uniqueness).
4. **Characterization tests** — one per edge-matrix row in feature.md §7.2.

**Acceptance**: UC3 passes; mixed MissingData + one value is a composition
dispute; seal path covered; `fireAndForget` does not seal.

---

## Phase 4 — Auth + health plumbing

1. Expose JWT roles on `ctx.user` (`common.User.Roles`; JWT
   `rolesClaimName`; other strategies leave empty). **IdP claim-shape mapping
   (`roles` vs `scp` vs `claimMatchers`) is an open topic** — see feature.md §13;
   do not over-commit a mapping in this phase.
2. Required `cordonClass` on `Cordon` / `Uncordon` across interfaces + call sites.
3. Per-class bitmask on `TrackedMetrics` (order-independent).
4. Move consensus sitout + misbehavior rate limiter to `health.Tracker`.
5. Selector reads class set; `anyPunished` / `anyOperatorCordon` scan the
   **network** registry (including nodes selection already dropped).
6. `blockNumber` extraction for eval ctx.

**Acceptance**: punished / admin-cordoned anywhere keeps default; availability
cordon allows `fallback` for authorized roles; empty-tag `allUnavailable()` is
false. Role E2E may remain deferred pending §13.

---

## Phase 5 — Executor wiring

1. **Pre-round resolution**: build `EvalContext` → evaluate → resolve name →
   run existing executor under that config.
2. **Fail closed** — eval error / timeout / unknown name / pool exhaustion →
   default + log.
3. **`return "default"`** — resolve like empty/null (intentional); metric/header
   `default`; **do not** increment `unknown_name`.
4. **Header + metric** — `X-ERPC-Consensus-Policy` **output-only**;
   `erpc_consensus_policy_selected_total`, `_eval_failed_total`,
   `_eval_bypassed_total`, `_eval_duration_seconds`.
5. **Load benchmark** — selector on vs off; p99 delta ≪ 1ms.

**Acceptance**: UC1–UC3 as config fixtures; no mid-round switch; `"default"`
string path covered.

---

## Phase 6 — Decision cache (optional, future)

Only if Phase 1 benchmark shows eval cost matters. Design in feature.md §5.

**Acceptance**: role-only evals hit cache; health transitions invalidate;
cardinality guard disables caching rather than OOMing.

---

## Phase 7 — Docs + E2E

1. Docs under `docs/pages/config/` for `consensus.policies` / `customPolicy` /
   waiver fields (agent-first; match `failsafe/hedge.mdx`).
2. E2E fixtures for UC1–UC3 + matrix rows.
3. Update #1088 with implementation status.

**Acceptance**: docs build; E2E green; issue updated.

---

## Phase 8 — v1.1 empty-outside-retention waiver

Required once dispute traces show null-shaped prune
(`eth_getTransactionByHash` on pruned history).

1. **Config** — `waiveAgreementOnEmptyOutsideRetention` + `retentionBlocks` +
   `blockEvidenceFields`; validation requires `retentionBlocks > 0` + fields.
2. **Waiver** — ≥ `minAgreement` empties, no matching non-empty, round-complete,
   plus block proof (avail lower bound preferred; else chain-head depth with
   served-tip trust rules in feature.md §7.1).
3. **`promoteAbstentionWinner`** — rescue synthesized empty-vs-archive ties so
   the waiver path can run.
4. **Hedge consensus-slot empty-keep** — keep emptyish result as the slot vote
   when `consensusSlot` is set.
5. **PreferNonEmpty count gate** — fire only while empty/error ≥ threshold is at
   least tied with leading non-empty (no map-order flake; no PreferLarger
   override).
6. **Observability** — `empty_outside_retention` reason; `waiver_unproven_total`;
   mid-round deferral debug-only.

**Acceptance**: characterization + E2E for empty-outside-retention; wait-cap
seal; hedge keep; PreferNonEmpty ties.

---

## Phase 9 — Enhancements (post-MVP DX)

See feature.md §12. Optional after the core contract lands:

1. **One-level `extends`** — `extends: default` (inline) or `extends: <named>`;
   shallow overlay; `requiredParticipants: []` clears quotas; then
   `SetDefaults`; no silent inherit without `extends`.
2. Confirm **`return "default"`** resolve rule if not already in Phase 5.
3. **Resolved-policy dump** on `erpc config validate`.
4. Optional: selector simulate (synthetic request → policy name).

**Acceptance**: multi-grade map shares hygiene via `extends: default`; sparse
`fallback` keeps ignoreFields/punish from base; dump shows resolved config.

---

## Out of scope for v1 (explicit)

- Multi-hop / mixin `extends` graphs (Phase 9 is one-level only).
- Silent inherit from inline without `extends`.
- Decision cache (Phase 6 — only if measured cost forces it).
- Inline policy object return from eval (name only).
- Mid-round policy switching / post-round JS grading.
- #1069 bounded-deviation value logic.
- Separate claim→policy allowlist outside JS.
- Locking IdP `scp` / `claimMatchers` → `Roles` mapping (open topic).

---

## Risks / watch-items

### Must handle in the build (prototype-validated)

- **Unanswered required slots + composition disputes.** Disputes must not
  short-circuit while `hasRemaining`; wait-cap must **seal** so waivers
  evaluate (e.g. mp=4 with one dead internal). `fireAndForget` must not seal.
- **Hedge replacing the internal empty vote.** Consensus-slot hedge must keep
  JSON null as the slot vote or the empty-waiver never sees internals.
- **PreferNonEmpty map-order flake.** Gate on counts, not `getBestByCount()`,
  and stop when non-empty already leads (PreferLarger).
- **Waiver floor.** Use ≥ `minAgreement` abstentions with “matching data holds”
  — not unanimous abstention.
- **Sparse named policies without `extends`.** Stock `SetDefaults()` ≠ live
  inline hygiene. Until Phase 9, copy shared fields explicitly onto every grade
  that should keep them (especially `fallback`).

### Still open / watch

- **Punished vs unhealthy** — typed cordon classes + network-wide
  `anyPunished` / `anyOperatorCordon` (including selection-dropped nodes).
  Keep the availability whitelist fail-closed for any future class.
- **Roles / IdP claim shapes** — open topic (feature.md §13). Do not assume
  `claimMatchers` or `scp` populate `hasRole`.
- **Empty-waiver proof trust** — majority served-tip only under
  `enabledFor: latest`; else min of ≥2 participant pollers; residual
  two-colluder inflation on the poller fallback.
- **Decision-cache key explosion** — if Phase 6 is built: cardinality guard
  must disable caching, not OOM.
- **Scope creep into post-round grading** — reject; point at #1069 / executor
  config.
