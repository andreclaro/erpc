# SVM Slot-Grouped Consensus for Moving-Head Reads — Specification

**Last revised**: 2026-09-16

Companions: [plan.md](./plan.md) · [svm-consensus-gaps.md](./svm-consensus-gaps.md)

---

## 1. Purpose

Enable **multi-provider consensus** on Solana moving-head reads
(`getBalance`, `getAccountInfo`, `getTokenAccountBalance`, …) without the
false disputes that naive hash agreement produces when healthy upstreams
answer at adjacent rooted slots.

After this feature:

- Financially critical enveloped reads no longer trust a single upstream.
- Two providers must agree on the **same value at the same `context.slot`**
  before a result is served under `returnError` / mix-quorum policies.
- Among slots that meet `agreementThreshold`, the **highest agreed slot**
  (freshest *agreed* tip) wins — a lone tip provider never wins alone.
- Slot-pinned strict consensus (`getBlock`, `getTransaction`, …) is unchanged.

### Goals

- Response-side pinning via `context.slot` (Solana has no request-side slot
  rewrite analogous to EVM tag→block-number).
- Slot-grouped voting in the consensus executor; value hashed with
  `context.slot` / `context.apiVersion` ignored (slot is the *group key*,
  not part of the value hash).
- Non-zero wait so a second provider can catch up to a tip slot
  (`maxWaitOnResult` must not be `0` on this path).
- Misbehavior only for **same-slot value dissent** — cross-slot lag is not
  misbehavior.
- Optional Phase 2: when `context.slot ≤ poller.finalizedSlot` (and the
  request’s effective commitment permits), classify the response `finalized`
  and cache under `(method, params, slot)`.
- Deployment: a **new** failsafe rule matching `matchFinality: [realtime]` for
  enveloped methods — **not** widening the existing strict slot-pinned rule.

### Non-goals

- Do **not** add `realtime` to the strict slot-pinned rule’s `matchFinality`.
- Do **not** use `preferHighestValueFor` / `agreementThreshold: 1` on
  moving-head reads (that is tip routing, not consensus).
- Do **not** rewrite requests via `minContextSlot` as if it were a pin (it is
  a floor only).
- Do **not** invent a request-side historical slot for `getBalance` /
  `getAccountInfo` — the wire protocol does not provide one.
- Out of scope for this feature (tracked in [svm-consensus-gaps.md](./svm-consensus-gaps.md)):
  SVM block-head leader behaviors, nested `preferHighestValueFor` paths,
  bare-`0` emptyish semantics.

---

## 2. Background (why today fails)

### Finality classification

`architecture/svm/finality.go` `GetFinality`:

1. `neverCacheMethods` (incl. `getBalance`, `getTokenAccountBalance`) → **realtime**
2. `alwaysFinalizedMethods` → finalized
3. Not in `slotPinnedMethods` (incl. `getAccountInfo`, …) → **realtime**
4. Only then: `getBlock` / `getTransaction` use commitment

**Commitment does not change** realtime for moving-head methods — including
`commitment: finalized`. That commitment means “state at the latest rooted
slot,” which advances ~every 400ms; the request names no slot.

`IsFinalizedCommitment` is a different predicate (routing / slot-lag filter)
and must not be conflated with `matchFinality`.

### Deployment failsafe today

A typical SVM strict failsafe rule lists several moving-head methods but
`matchFinality: [unfinalized, finalized, unknown]` — so **realtime never
matches** and those methods fall through to the catchall (no consensus).
That exclusion is intentional for the *strict* rule; the fix is a separate
rule after this code lands, not widening the slot-pinned rule.

### Why EVM’s approach does not transfer

| | EVM | SVM moving-head |
|---|---|---|
| Pin location | Request (`latest`/`finalized` → block **number**) | No slot param to rewrite |
| `minContextSlot` | N/A | Floor only, **not** a pin |
| Self-pin | Block number in request | **`context.slot` in the response** |

---

## 3. Solution — slot-grouped consensus

Moving-head responses already self-pin:

```json
{ "context": { "slot": 1000, "apiVersion": "…" }, "value": 12345 }
```

`value @ rooted-slot-N` is immutable for that N. Compare answers only when they
answer the **same** question (same `context.slot`).

### 3.1 Algorithm

1. Fan out to `maxParticipants` upstreams (existing executor).
2. For each successful enveloped response, read `context.slot`.
3. **Partition** non-infrastructure responses by `context.slot`.
4. Within each slot partition, group by **value** hash
   (`ignoreFields` / defaults strip `context.*` from the hash — slot is only
   the partition key).
5. A slot partition **qualifies** when some value-group in it has
   `count ≥ agreementThreshold`.
6. Among qualifying slots, pick the **highest slot**; that partition’s
   winning value-group is the consensus winner.
7. Same slot + different values at/above threshold with no unique winner →
   real dispute → `disputeBehavior` (production: `returnError`).
8. Different slots alone are **not** a dispute; keep collecting until
   wait-caps fire or all participants answer.
9. If no slot qualifies by round end (`maxWaitOnResult` / collection done) →
   `returnError` under production policy (or low-participants if
   `validParticipants < agreementThreshold`).

```
t=0:  QN=100@1000, Alchemy=105@1002, Helius=100@1000
      → agreed 100@1000 (2); pending 105@1002 (1)
t=+Δ: Helius → 105@1002
      → agreed 105@1002 (2) → return freshest agreed tip
```

**Key rule:** a tip slot with one vote never wins. “Most updated” alone is
still single-provider trust.

### 3.2 Wait semantics (locked)

Default: **wait** up to `maxWaitOnResult` for a *higher* qualifying slot before
returning a lower agreed slot. Only when the wait cap fires (or all
participants have answered) return the highest *already-qualified* slot.

Rationale: returning the older agreed slot immediately maximizes false
“freshness” regressions for financial callers; waiting bounds p99 to the
configured cap (~inter-provider root lag, typically 0.4–0.8s).

`maxWaitOnResult: 0` / `maxWaitOnEmpty: 0` is **invalid** for this path
(deployment must not copy the slot-pinned rule’s zero wait).

### 3.3 Activation (locked)

Enter slot-grouped mode when **all** of:

- A consensus policy is active for the request, and
- At least one collected successful response has a parseable `context.slot`.

Fallthrough (no parseable `context.slot` on any success): existing hash
consensus unchanged. Discovery via response shape — not a hard-coded method
enum in the hot path (method lists remain a helm concern).

No config knob whose “off” setting re-enables never-right naive hashing for
enveloped responses under `returnError`.

### 3.4 Misbehavior (locked)

| Situation | Misbehavior? |
|---|---|
| Same `context.slot`, different `value` vs winning group | **Yes** |
| Different `context.slot` (lag / ahead of winner) | **No** |
| Infrastructure / consensus-valid errors | Existing rules (errors ≠ data misbehavior) |

`punishMisbehavior` / cordon only applies to same-slot value dissenters when
the winning group has a clear majority (`count > validParticipants/2` within
the **winning slot cohort**, not the whole round).

### 3.5 Composition quotas (`requiredParticipants` / `minAgreement`)

Unchanged contract, applied to the **winning slot cohort’s** agreeing
participants: if mix consensus requires ≥1 `type:internal` and ≥1
`type:external` in the winner, those tags must appear among the upstreams that
voted for the winning value **at the winning slot**. Cross-slot participants
do not count toward the quota.

`eth_sendRawTransaction` / SVM send broadcast exemption unchanged (not this
path).

### 3.6 Short-circuit

- Do **not** short-circuit to a lone tip-slot response.
- Unassailable lead / error-threshold short-circuits apply **within** a slot
  partition only when no higher slot can still qualify given remaining
  participants and wait budget (conservative: prefer waiting while
  `maxWaitOnResult` can still arm a higher slot).

---

## 4. Phase 2 — paired finality / cache (separate ship)

If `context.slot ≤ SvmStatePoller.FinalizedSlot` (network / upstream view TBD
in plan) **and** the request’s effective commitment is `finalized` (or the
method has no weaker commitment dimension that would make the slot
unrooted-relative):

- Classify response finality as **`finalized`** (immutable at that slot).
- Cache key includes `context.slot`: `(method, params, slot)` — not blanket
  realtime TTL.

**Must not** promote `commitment: confirmed` / `processed` answers to
immutable finalized cache solely because `context.slot ≤ finalized root`.

This is the SVM analogue of EVM tag→number rewrite, done on the response.
Ship after slot-grouped voting is correct and soaked.

---

## 5. Deployment failsafe contract

After eRPC code lands:

1. **Do not** add `realtime` to the strict slot-pinned rule’s `matchFinality`.
2. **Remove** moving-head method names from that rule’s `matchMethod` (they never
   matched anyway; avoid silent land if finality ever changes).
3. **Add** a dedicated failsafe rule, e.g.:

```yaml
- matchMethod: "getAccountInfo|getBalance|getTokenAccountBalance|getMultipleAccounts|…"
  matchFinality: [realtime]
  timeout: { duration: 30s }
  consensus:
    maxParticipants: 6              # ≥4 when mix quotas need 2+2 pool
    agreementThreshold: 2
    disputeBehavior: returnError
    lowParticipantsBehavior: returnError
    preferLargerResponses: false
    maxWaitOnResult: { quantile: 0.5, min: 200ms, max: 1s }  # non-zero
    maxWaitOnEmpty: { quantile: 0.9, min: 50ms, max: 2s }
    # when mixed internal + external upstreams:
    requiredParticipants:
      - { tag: "type:internal", minParticipants: 2, minAgreement: 1 }
      - { tag: "type:external", minParticipants: 2, minAgreement: 1 }
    punishMisbehavior: { disputeThreshold: 500, disputeWindow: 5m, sitOutPenalty: 5m }
```

4. Prefer **3+** upstreams before prod so same-slot disputes have a majority.
5. Soak first on priority methods; watch dispute rate, composition
   disputes, p99 latency.

---

## 6. In scope / out of scope

### In scope (context-enveloped)

Priority: `getAccountInfo`, `getBalance`,
`getTokenAccountBalance`, `getMultipleAccounts`.

Any method whose successful result carries parseable `context.slot` may
participate once a failsafe rule matches it.

### Out of scope

- Bare / non-envelope: `getBlocks`, `getSignaturesForAddress`, `getHealth`,
  bare integers without envelope.
- Already handled: `getSlot` / `getBlockHeight` (freshest-wins),
  `getLatestBlockhash` (fastest-wins), `sendTransaction` (broadcast),
  slot-pinned strict (`getBlock`, `getTransaction`, `getBlockTime`, …).

---

## 7. Observability

| Signal | Purpose |
|---|---|
| Existing `erpc_consensus_*` with `finality=realtime` | Baseline volume / dispute / low_participants |
| New (recommended): `erpc_consensus_slot_groups` / winning `context.slot` span attr | Prove grouping; debug false disputes |
| Misbehavior metric must not spike on cross-slot lag | Regress if 3.4 is broken |
| `erpc_consensus_wait_capped_total{trigger}` | Tune wait vs root lag |

---

## 8. Acceptance criteria

1. Two upstreams, same `value`, different `context.slot` → **no** dispute;
   wait / return highest agreed slot per §3.2 — never punish for lag.
2. Two upstreams, same `context.slot`, different `value`, threshold unmet /
   tied → dispute under `returnError`.
3. Tip slot with 1 vote + older slot with 2 agreeing votes → wait; if tip
   gets a second agreeing vote before cap → return tip; else return older
   agreed.
4. Mix `minAgreement` enforced on winning **slot** cohort.
5. Strict slot-pinned rule behavior for `getBlock` unchanged; realtime still
   excluded from that rule.
6. Phase 2 (if shipped): confirmed-commitment enveloped responses are not
   cached as finalized solely via slot ≤ finalized root.

---

## 9. Related

- Gaps inventory: [svm-consensus-gaps.md](./svm-consensus-gaps.md)
- Implementation plan: [plan.md](./plan.md)
- Existing SVM finality: `architecture/svm/finality.go`
- Consensus executor: `consensus/executor.go`, `consensus/analysis.go`
- Envelope ignore defaults: `common/defaults.go` (`context.slot`,
  `context.apiVersion`)
