# Custom Consensus Policies Engine — Implementation Plan

**Status**: Proposed phased delivery (for core maintainer review)
**Last revised**: 2026-09-16
**Spec**: [feature.md](./feature.md) — **single source of truth for behavior**
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)

Companion to [feature.md](./feature.md). This plan is delivery sequence +
acceptance gates only. Do not restate waiver / seal / hedge / PreferNonEmpty /
`extends` semantics here — cite the feature sections below.

---

## Locked decisions

Short index into the feature contract (full text lives there):

| Topic | Where |
|-------|--------|
| Pre-round JS → policy **name**; fail-closed inline default; `return "default"` | feature §1–§3, §4.1 |
| Auth via `ctx.user`; no claim→policy allowlist; IdP claim shapes open | feature §4.3, §13 |
| Cordon classes + sitout on `health.Tracker` | feature §4.4–§4.5 |
| Role-gated fallback / historical grades | feature §6–§7 |
| MissingData + empty-outside-retention (v1.1) waiver rules | feature §7.1, R1–R12 |
| Metrics / header | feature §8 |
| Decision cache not in v1 | feature §5 |
| `extends` / dump / simulate = nice-to-have | feature §12 |
| Non-goals (post-round JS, mid-round switch, …) | feature §1 Non-goals |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/custom-consensus-policies/feature.md`
- [x] Write this plan
- [ ] Land for review; link from #1088

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Standalone selector engine

Package: `internal/consensus/policy/` — no imports from `consensus/` executor.
Implements feature §3–§4.6 (stdlib, sandbox, pool with low-water refill).

- Compile-once Sobek pool: pre-warm 8, **low-water** async refill at ≤2 free,
  sync create if empty — **never** `pool_exhausted` bypass (feature §4.6)
- `EvalContext` + stdlib helpers from feature §3.1
- Fallthrough tests first (nil user, timeout, throw, empty set)
- Micro-benchmark; if p99 ≪ 1ms, leave decision cache unbuilt (feature §5)

**Acceptance**: `go test ./internal/consensus/policy/...` green; zero executor
imports; benchmark posted; no fail-closed path for empty pool.

---

## Phase 2 — Config schema

Implements feature §2 field surface (named `policies`, `customPolicy`, MissingData
waiver flag). Empty-waiver fields wait for Phase 8.

- Validation: reserved `"default"`, no nested selection, never-waivable floor (R9)
- Inline block = anonymous default; `evalTimeout` default `50ms`
- Tygo regen

**Acceptance**: existing configs load unchanged; fixture with `policies` +
`customPolicy` validates.

---

## Phase 3 — MissingData waiver

Implements feature §7.1.2 + R1/R5/R9/R10 (MissingData path only).

- Wire `waiveAgreementOnMissingData` in `enforceWinnerComposition`
- Wait-cap **seal** + re-run winner; `fireAndForget` must not seal (§7.1 / R1)
- Characterization tests per feature §7.2 matrix rows that apply

**Acceptance**: UC3 (role-gated historical) passes; seal / no-seal paths covered;
behavior matches feature §7.1.2 (do not invent a second rule set here).

---

## Phase 4 — Auth + health plumbing

Implements feature §4.3–§4.5, §6 gates, R7–R8.

- `User.Roles` + JWT `rolesClaimName` (claim→Roles mapping stays **open** — §13)
- Typed `cordonClass` + per-class tracker bits; sitout / rate limiter on tracker
- `anyPunished` / `anyOperatorCordon` over **network** set (incl. selection-dropped)
- `blockNumber` for eval ctx

**Acceptance**: matches feature §6 table (punish/admin → default; availability →
`fallback` when authorized). Role E2E may wait on §13.

---

## Phase 5 — Executor wiring

Implements feature §4.1, §8, §3 `"default"` resolve.

- Pre-round resolve → run existing executor under selected config
- Fail closed on error / timeout / unknown name (not on pool capacity — §4.6)
- `return "default"` ≡ null (not `unknown_name`)
- Output-only `X-ERPC-Consensus-Policy` + policy metrics (§8)
- Load benchmark: selector on vs off; p99 delta ≪ 1ms

**Acceptance**: UC1–UC3 fixtures; no mid-round switch; `"default"` path covered.

---

## Phase 6 — Decision cache (optional)

Only if Phase 1 forces it. Design: feature §5.

**Acceptance**: per feature §5 (hits, invalidation, cardinality guard).

---

## Phase 7 — Docs + E2E

- Operator docs (`consensus.policies` / `customPolicy` / waivers); hedge exemplar
- E2E for UC1–UC3 + §7.2 matrix
- Update #1088

**Acceptance**: docs build; E2E green; issue updated.

---

## Phase 8 — v1.1 empty-outside-retention waiver

Implements feature §7.1.3–§7.1.5 + R1/R5/R11/R12.

- Config + validation for empty-waiver fields (§2 / R1)
- Waiver + block-proof order (§7.1.3)
- `promoteAbstentionWinner`, hedge consensus-slot empty-keep, PreferNonEmpty
  count gate (§7.1.3–5, R11–R12)
- Observability: `empty_outside_retention` + `waiver_unproven_total` (§8)

**Acceptance**: §7.2 rows for null prune + seal/hedge/PreferNonEmpty cases green;
semantics match feature §7 (cite, don’t duplicate).

---

## Phase 9 — Nice-to-haves (optional DX)

Implements feature §12 if chosen. **`extends` is nice-to-have**, not required.

- One-level `extends` (§12.1)
- Resolved-policy dump (§12.3); optional selector simulate (§12.4)
- `"default"` resolve already required in Phase 5 / §3

**Acceptance** (if built): per feature §12.

---

## Out of scope

See feature §1 Non-goals and §12.5. Plan does not add a second list — open
IdP mapping stays feature §13.

---

## Risks / watch-items

Build hazards already specified in the feature — track in review, don’t re-spec:

| Risk | Feature pointer |
|------|-----------------|
| Wait-cap seal vs `fireAndForget` | §7.1.2, R1 |
| Hedge empty-as-slot-vote | §7.1.4, R11 |
| PreferNonEmpty count gate vs PreferLarger | §7.1.5, R12 |
| Waiver floor ≥ `minAgreement` (not unanimous) | §7.1.2–3, §11.5 |
| Sparse named policies without `extends` (stock defaults ≠ live hygiene) | §2 “v1 config cost”, §12.1 |
| Cordon / punish pin over network set | §3.1, §4.5, R7–R8 |
| Empty-waiver head trust (served-tip / min-of-two) | §7.1.3 |
| Roles vs `claimMatchers` / `scp` | §13 |
| Decision-cache cardinality (if Phase 6) | §5 |
| Scope creep → post-round JS | §1 Non-goals |
