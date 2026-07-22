# Consensus Fallback Policy

**Status:** proposed — implementation plan for a single PR
**Motivation thread:** follow-up to #1008 (consensus winner-composition quota via `minAgreement`)

## 1. Problem

Mixed-node consensus (#1008) enforces per-tag composition quotas on the winning response group.
A typical BRP deployment requires `provider:internal ≥ 1` AND `provider:external ≥ 1` to agree
before a response is returned. When Circle-operated internal nodes are unavailable — due to a
deployment, an outage, or `punishMisbehavior` placing them in sitout — the standard policy
hard-fails with `ErrConsensusCompositionDispute` even when multiple independent external
providers return identical responses.

No mechanism exists today to allow external-only quorum as a controlled, observable, and
access-gated fallback.

## 2. Prerequisite

`minAgreementFallback` is only valid when `requiredParticipants` entries with `minAgreement`
are present. The config validator rejects `minAgreementFallback` on any entry whose owning
`requiredParticipants` block has no `minAgreement`. This fallback feature requires mixed-node
consensus (#1008) to be configured — it relaxes per-group thresholds; it does not replace
the threshold mechanism.

## 3. Design

### 3.1 Config shape

`minAgreementFallback` is added inline on each `requiredParticipants` entry.
`fallbackTrigger`, `fallbackWindow`, `fallbackThreshold`, and `fallbackAllowedUsers` are
added at the top-level `consensus` block.

```yaml
consensus:
  fallbackTrigger: circuit-breaker  # realtime | circuit-breaker
  fallbackWindow: 5m                # circuit-breaker only: rolling window
  fallbackThreshold: 0.8            # circuit-breaker only: composition-absent rate to trip
  fallbackAllowedUsers:             # omit to allow all authenticated callers
    - "service-a"
    - "service-b"
  requiredParticipants:
    - tag: "provider:internal"
      minParticipants: 1
      minAgreement: 1
      minAgreementFallback: 0       # 0 = group is optional in fallback mode
    - tag: "provider:external"
      minParticipants: 2
      minAgreement: 1
      minAgreementFallback: 2       # ≥2 independent external providers must agree
```

| Field | Type | Default | Description |
|---|---|---|---|
| `minAgreementFallback` | `int` | `minAgreement` | Relaxed per-group agreement quota for fallback evaluation. `0` = group optional. |
| `fallbackTrigger` | `"realtime" \| "circuit-breaker"` | — | Required when any group has `minAgreementFallback`. Pick-one. |
| `fallbackWindow` | `Duration` | TBD | Circuit-breaker: rolling window for failure-rate tracking. |
| `fallbackThreshold` | `float` | TBD | Circuit-breaker: composition-absent rate (`0..1`) to trip the breaker. |
| `fallbackAllowedUsers` | `[]string` | `nil` (all) | `userId` values permitted to activate fallback. Omit = all authenticated callers. Unauthenticated requests are blocked when this list is set. |

### 3.2 Evaluation order

1. **`lowParticipantsBehavior` checked first.** If fewer valid participants responded than
   `agreementThreshold`, the existing low-participants behavior fires and fallback is not
   considered.
2. **Standard policy evaluated.** If it passes, return result with
   `X-ERPC-Consensus-Policy: standard`.
3. **Fallback eligibility check.** Fallback proceeds only when all of the following hold:
   - Standard policy failed with a composition dispute **and** every group whose
     `minAgreementFallback` is `0` had zero responding participants (absent-group failure,
     not a genuine dispute between responding nodes — see §3.5).
   - Caller's `userId` is in `fallbackAllowedUsers`, or the list is empty.
   - `X-ERPC-Skip-Consensus-Fallback: true` was not sent.
   - `fallbackTrigger: circuit-breaker` — breaker is tripped for this network (see §3.4).
     `fallbackTrigger: realtime` — no additional gate.
4. **Fallback policy evaluated.** Apply `minAgreementFallback` quotas. If satisfied, return
   result with `X-ERPC-Consensus-Policy: fallback`.
5. **Both fail.** Return error. `X-ERPC-Consensus-Policy: standard` (last attempted policy,
   or the policy in effect when fallback was blocked before being attempted).

`eth_sendRawTransaction` is exempt from `minAgreementFallback` enforcement, inheriting the
same exemption already applied to `minAgreement` in #1008.

`X-ERPC-Consensus-Policy` is absent from the response when consensus is bypassed via
`X-ERPC-Skip-Consensus`.

### 3.3 Trigger modes

**`realtime`** — fallback eligibility is re-evaluated on every request independently.
Stateless. No per-network state is maintained. Use for testing or simple deployments where
a sustained-failure gate is not needed.

**`circuit-breaker`** — tracks a per-network rolling rate of _composition-absent failures_
(standard policy failed because an optional group had zero responding participants) over
`fallbackWindow`. Once the rate crosses `fallbackThreshold`, the network enters fallback
mode. While tripped, requests skip standard evaluation and evaluate against fallback quotas
directly — subject to `fallbackAllowedUsers` and `X-ERPC-Skip-Consensus-Fallback` as always.
The breaker resets when standard policy success rate recovers above threshold. Fallback
successes do not count toward reset. Circuit-breaker is the recommended mode for production.

### 3.4 Circuit-breaker counter semantics

The circuit-breaker increments its failure counter only on **composition-absent failures**
— where the standard policy fails because an optional group had zero responding participants.
It does not increment on:

- Genuine disputes (tagged upstreams present but disagreeing) — fallback would not resolve
  these; tripping would be counterproductive.
- `lowParticipantsBehavior` errors.
- Infrastructure / timeout errors.

This requires the ability to distinguish absence from dispute at the error level. The
implementation must add a cause flag or a new error subtype on `ErrConsensusCompositionDispute`
that records whether the failing group had zero participants or had participants that disagreed.
See §6 (open questions) for the exact mechanism decision.

### 3.5 Misbehaving upstreams and sitout

When an internal upstream consistently returns wrong data, `punishMisbehavior` applies a
rate-limited penalty and eventually places the upstream in sitout (`sitOutPenalty` duration).
A sitout upstream is excluded from consensus rounds — it counts as absent for its tag group,
enabling fallback to activate. This is the intended recovery path when internal nodes are
present but misbehaving: dispute → punish → sitout → absent-group failure → fallback.

The minimum external quorum required while the internal group is absent is controlled by
`minAgreementFallback` on the external group entry.

### 3.6 Headers

| Header | Direction | Values | Notes |
|---|---|---|---|
| `X-ERPC-Skip-Consensus-Fallback` | Request | `true` | Disables fallback for this request. Omitted = fallback allowed. Follows the `X-ERPC-Skip-*` directive convention. |
| `X-ERPC-Consensus-Policy` | Response | `standard` \| `fallback` | Present only when consensus is active and a policy was attempted. `standard` when fallback was blocked or not attempted. Absent when `X-ERPC-Skip-Consensus` bypasses consensus entirely. |

`X-ERPC-Skip-Consensus-Fallback` is subject to `allowClientDirectives` gating in the same
way as other `X-ERPC-Skip-*` directives.

## 4. Invariants (each backed by a test)

- **I1 — Standard-only by default.** When no group has `minAgreementFallback` set (or all
  entries default to `minAgreement`), behavior is byte-identical to #1008 with no fallback
  ever attempted.
- **I2 — Absent-group-only activation.** Fallback never activates when a tagged upstream is
  present but wrong — only when the group has zero responding participants. A genuine dispute
  with internal nodes responding must not activate fallback.
- **I3 — Directive suppression.** `X-ERPC-Skip-Consensus-Fallback: true` always prevents
  fallback evaluation, regardless of trigger mode or breaker state. The caller receives
  `X-ERPC-Consensus-Policy: standard` and the standard policy error.
- **I4 — User gate.** When `fallbackAllowedUsers` is set, a caller whose `userId` is not in
  the list never activates fallback. Unauthenticated callers (empty `userId`) are blocked.
  Omitting `fallbackAllowedUsers` allows all authenticated callers.
- **I5 — `eth_sendRawTransaction` exemption.** The method is exempt from `minAgreementFallback`
  enforcement, identical to the `minAgreement` exemption in #1008. No fallback attempt is made.
- **I6 — `lowParticipantsBehavior` priority.** The low-participant check fires before fallback
  eligibility is considered. A low-participant failure does not trigger or increment the
  circuit-breaker counter.
- **I7 — Circuit-breaker counter precision.** The failure counter increments only on
  absent-group composition failures. Genuine disputes and infrastructure errors do not
  increment the counter.
- **I8 — Breaker reset only on standard success.** A fallback-mode success does not decrement
  the counter or advance breaker reset. Only a standard policy success while the breaker is
  tripped resets it.
- **I9 — Header absent on skip.** `X-ERPC-Consensus-Policy` is absent when
  `X-ERPC-Skip-Consensus` bypasses consensus. No header is written.
- **I10 — Prerequisite enforcement.** Config load fails (startup error) when any entry has
  `minAgreementFallback` set but the corresponding `requiredParticipants` block has no
  `minAgreement`, or when `fallbackTrigger` is absent while any entry has `minAgreementFallback`.

## 5. Backward compatibility

- **No config changes for existing deployments.** `minAgreementFallback` defaults to
  `minAgreement` when omitted — fallback evaluation applies standard quotas, producing
  identical behavior to #1008.
- **Wire surface unchanged.** `X-ERPC-Skip-Consensus-Fallback` and `X-ERPC-Consensus-Policy`
  are new headers; no existing header is renamed or removed.
- **Error surface unchanged.** `ErrConsensusCompositionDispute` (HTTP 409) is the error code
  for both standard and fallback failures. The cause field added for §3.4 is internal; no
  wire-visible error code changes.
- **`punishMisbehavior` unchanged.** The sitout/rate-limiter mechanism is untouched; fallback
  simply treats a sitout upstream's absence as the absent-group condition.

## 6. Open questions

1. **Cause flag vs. new error subtype for absent-group detection (§3.4).** Two options:
   - Add a `Cause` field to the existing `ErrConsensusCompositionDispute` error value,
     populated by `groupSatisfiesAgreementQuotas` with `"absent_group"` or `"dispute"`.
   - Define `ErrConsensusCompositionDisputeAbsentGroup` as a wrapped sentinel, allowing
     `errors.Is` matching.
   Both are implementation-only changes with no wire or config impact. The cleaner option
   for the circuit-breaker counter is the cause-field approach — one error type, rich
   classification. Decision deferred to implementation PR.

2. **`fallbackWindow` and `fallbackThreshold` defaults.** Values affect breaker sensitivity
   and recovery speed. To be decided with Blockchain Infrastructure based on internal-node
   failure patterns. Candidates: `5m` / `0.8`.

## 7. Test matrix

| Area | Cases |
|---|---|
| Standard-only (I1) | No `minAgreementFallback` → identical to #1008 in all scenarios |
| Absent-group activation (I2) | Internal group has 0 participants → fallback evaluates; internal group has mismatching participants → fallback blocked |
| Directive suppression (I3) | `X-ERPC-Skip-Consensus-Fallback: true` with realtime and circuit-breaker modes |
| User gate (I4) | Allowlist set: callers in/out/unauthenticated; allowlist omitted: all authenticated pass |
| `eth_sendRawTransaction` (I5) | Absent internal group → no fallback, no composition error adjustment |
| `lowParticipantsBehavior` priority (I6) | Low-participant threshold not met → fires before fallback; does not increment breaker counter |
| Counter precision (I7) | Absent-group failure increments counter; genuine dispute does not; infrastructure error does not |
| Breaker reset (I8) | Standard success while tripped → resets; fallback success while tripped → no reset |
| Header absent on skip (I9) | `X-ERPC-Skip-Consensus` set → no `X-ERPC-Consensus-Policy` header in response |
| Prerequisite validation (I10) | `minAgreementFallback` without `minAgreement` → startup error; `minAgreementFallback` without `fallbackTrigger` → startup error |
| Realtime mode end-to-end | Standard fails absent-group → fallback succeeds → `X-ERPC-Consensus-Policy: fallback` |
| Circuit-breaker trip | Rate crosses threshold → mode flips; subsequent requests skip standard |
| Circuit-breaker recovery | Standard succeeds while tripped → mode resets |
| Sitout → fallback path | `punishMisbehavior` sitout removes internal upstream → absent-group → fallback activates |
| Race | `go test -race` on consensus suite with concurrent requests and breaker state transitions |

## 8. Explicit non-goals

- **No new endpoint or routing surface.** Fallback is a policy applied within the existing
  consensus execution path, not a separate proxy tier or duplicate endpoint.
- **No fallback without mixed-node consensus configured.** Fallback is not a stand-alone
  mode; it requires `requiredParticipants` with `minAgreement`.
- **No relaxation of `agreementThreshold`.** The overall agreement count is unchanged;
  only per-group composition quotas are relaxed.
- **No client-controlled fallback threshold.** `fallbackAllowedUsers` is an operator list;
  callers cannot add themselves. The only client directive is opt-out, not opt-in or
  threshold adjustment.

## 9. Acceptance

- Full test matrix (§7) green, including race runs.
- Config-load validation rejects every invalid combination (I10).
- `X-ERPC-Consensus-Policy` header present and correct on every consensus-evaluated response
  in the integration suite.
- Circuit-breaker counter verified to not increment on genuine disputes across the test suite.
- PR description includes grep-level proof that `minAgreementFallback` evaluation is gated
  behind the single absent-group condition check — no shadow paths that activate fallback
  on genuine disputes.
