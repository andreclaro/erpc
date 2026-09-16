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
  before a result is served under `returnError`
- Among slots that meet `agreementThreshold`, the **highest agreed slot**
  (freshest *agreed* tip) wins
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

### Non-goals

- Do **not** use `preferHighestValueFor` / `agreementThreshold: 1` on
  moving-head reads (that is tip routing, not consensus).
- Do **not** rewrite requests via `minContextSlot` as if it were a pin (it is
  a floor only).
- Do **not** invent a request-side historical slot for `getBalance` — the
  wire protocol does not provide one (optional `minContextSlot` is a floor).
- Out of scope for this feature (tracked in [svm-consensus-gaps.md](./svm-consensus-gaps.md)):
  SVM block-head leader behaviors, nested `preferHighestValueFor` paths,
  bare-`0` emptyish semantics.
  Operator failsafe / helm wiring is out of scope here — this feature is the
  consensus executor behavior once a rule already matches.

---

## 2. Background (why today fails)

### Moving-head reads (`contextSlotMethods`)

Canonical example — `getBalance` (same shape as other enveloped methods in
`contextSlotMethods`
[`hooks.go`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)):

**Request** — names a pubkey (and optional commitment), **not** a historical
slot:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "getBalance",
  "params": [
    "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
    { "commitment": "finalized" }
  ]
}
```

**Response** — Solana `RpcResponse<u64>`: bank tip stamped as `context.slot`,
balance in `value`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "context": { "slot": 1000, "apiVersion": "2.0.15" },
    "value": 12345
  }
}
```

This is a **moving-head** read: each upstream answers “balance at *my*
current bank” and stamps that bank. Under `commitment: finalized` that bank
is still the latest **rooted** tip — it advances ~every 400ms — so two
healthy nodes routinely return the **same `value` at adjacent slots**
(e.g. `12345@1000` and `12345@1001`).

`GetFinality` correctly classifies `getBalance` **realtime** at every
commitment (`architecture/svm/finality.go` — also in `neverCacheMethods`).
That is a cacheability fact, not a consensus strategy. Slot-pinned methods
(`getBlock`, `getTransaction`) are out of this problem: the request already
names the slot or signature, so strict hash consensus compares one question.

### Why naive consensus fails on them

Default envelope `ignoreFields` strip `context.slot` / `context.apiVersion`
from the **value** hash (`common/defaults.go`) so the slot is not part of
the payload digest. Flat agreement still mixes **different questions** —
e.g. `getBalance` results at slot 1000 and 1001 land in one bucket. Adjacent
rooted tips then yield **false disputes** (transient balance splits during
tip advance) or a **stale majority** (older slot outvotes a fresher tip)
under `returnError` / `agreementThreshold ≥ 2`.

Cross-slot lag is expected SVM behavior, not misbehavior. Same-slot
**`value`** split (two balances at slot 1000) is a real dispute.

### Why EVM’s approach does not transfer

| | EVM | SVM `getBalance` (and other `contextSlotMethods`) |
|---|---|---|
| Pin location | Request (`latest`/`finalized` → block **number**) | No slot in `params` to rewrite |
| `minContextSlot` | N/A | Floor only, **not** a pin |
| Self-pin | Block number in request | **`result.context.slot` in the response** |

Response-side slot grouping is the weakest correct fix: partition
`getBalance` answers by `context.slot`, hash `value` within a slot, pick the
highest slot that meets `agreementThreshold`.

---

## 3. Solution — slot-grouped consensus

`getBalance` responses already self-pin via `result.context.slot` (see §2).
`value @ rooted-slot-N` is immutable for that N. Compare answers only when
they answer the **same** question (same `context.slot`).

### 3.1 Algorithm

1. Fan out to `maxParticipants` upstreams (existing executor).
2. For each successful enveloped response, read `result.context.slot`
   (e.g. `1000` from the `getBalance` example above).
3. **Partition** non-infrastructure responses by `context.slot`.
4. Within each slot partition, group by **value** hash
   (`ignoreFields` / defaults strip `context.*` from the hash — slot is only
   the partition key; for `getBalance` the digest is over `value` / lamports).
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

Example round for one `getBalance` (lamports @ slot):

```
t=0:  QN=12345@1000, Alchemy=12350@1002, Helius=12345@1000
      → agreed 12345@1000 (2); pending 12350@1002 (1)
t=+Δ: Helius → 12350@1002
      → agreed 12350@1002 (2) → return freshest agreed tip
```

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
consensus unchanged. The hot path discovers the slot from the response body
(not a method-name switch). The known inventory of methods whose result *can*
carry that envelope is `contextSlotMethods` in
[`architecture/svm/hooks.go`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)
(Solana `RpcResponse<T>` = `{context:{slot,…}, value:…}`). Failsafe
`matchMethod` lists which of those operators enable under consensus; methods
outside the table never produce a slot and stay on today’s path.

No config knob whose “off” setting re-enables never-right naive hashing for
enveloped responses under `returnError`.

**Key rule under `agreementThreshold ≥ 2`:** a tip slot with one vote does not
qualify; “most updated” alone is still single-provider trust.

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

For a `getBalance` (or other enveloped) winner: if
`context.slot ≤ SvmStatePoller.FinalizedSlot` (network / upstream view TBD
in plan) **and** the request’s effective commitment is `finalized` (or the
method has no weaker commitment dimension that would make the slot
unrooted-relative):

- Classify response finality as **`finalized`** (immutable at that slot).
- Cache key includes `context.slot`: `(method, params, slot)` — not blanket
  realtime TTL. (`getBalance` remains hard-skipped by `neverCacheMethods`
  today; Phase 2 applies to enveloped methods that *are* cacheable, or if
  that hard-skip is revisited.)

**Must not** promote a `getBalance` with `commitment: confirmed` /
`processed` to immutable finalized cache solely because
`context.slot ≤ finalized root`.

This is the SVM analogue of EVM tag→number rewrite, done on the response.
Ship after slot-grouped voting is correct and soaked.

---

## 5. In scope / out of scope

### Envelope inventory (`contextSlotMethods`)

Canonical list of methods whose result shape *can* carry `context.slot`
([`hooks.go` L654–L672](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)):

`getAccountInfo`, `getBalance`, `getBlockProduction`, `getFeeForMessage`,
`getLargestAccounts`, `getLatestBlockhash`, `getMultipleAccounts`,
`getProgramAccounts` (envelope only with `withContext:true`),
`getSignatureStatuses`, `getStakeMinimumDelegation`, `getSupply`,
`getTokenAccountBalance`, `getTokenAccountsByDelegate`,
`getTokenAccountsByOwner`, `getTokenLargestAccounts`, `getTokenSupply`,
`isBlockhashValid`, `simulateTransaction`.

Same set is mirrored in consensus `ignoreFields` defaults
(`common/defaults.go`: strip `context.slot` / `context.apiVersion` from the
value hash). Slot-grouping uses the slot as a **partition key** only; the
hash remains value-only.

### Enable under slot-grouped consensus (failsafe)

**Priority soak:** `getAccountInfo`, `getBalance`, `getTokenAccountBalance`,
`getMultipleAccounts`.

**Also suitable** (same moving-head class): token/program account reads
(`getTokenAccountsByOwner` / `ByDelegate`, `getTokenLargestAccounts`,
`getProgramAccounts` with context), `getSupply` / `getTokenSupply`,
`getStakeMinimumDelegation`, `isBlockhashValid`, etc. — enable via
`matchMethod` once soak looks healthy.

**Usually keep off this rule** (already have tip / other policies, or poor
agreement fit): `getLatestBlockhash` (fastest-wins), `getFeeForMessage`,
`getSignatureStatuses`, `getBlockProduction` / `getLargestAccounts`
(volatile), `simulateTransaction`.

### Out of scope (not in `contextSlotMethods`)

- Bare / non-envelope: `getBlocks`, `getSignaturesForAddress`, `getHealth`,
  bare integers (`getSlot`, `getBlockHeight`, …).
- Already handled elsewhere: `getSlot` / `getBlockHeight` (freshest-wins),
  `getLatestBlockhash` (fastest-wins — even though it *is* enveloped, tip
  policy stays), `sendTransaction` (broadcast),
  slot-pinned strict (`getBlock`, `getTransaction`, `getBlockTime`, …) —
  request-pinned; **not** the moving-head issue.

---

## 6. Observability

| Signal | Purpose |
|---|---|
| Existing `erpc_consensus_*` with `finality=realtime` | Baseline volume / dispute / low_participants |
| New (recommended): `erpc_consensus_slot_groups` / winning `context.slot` span attr | Prove grouping; debug false disputes |
| Misbehavior metric must not spike on cross-slot lag | Regress if 3.4 is broken |
| `erpc_consensus_wait_capped_total{trigger}` | Tune wait vs root lag |

---

## 7. Acceptance criteria

Framed on `getBalance` (same rules for other enveloped moving-head methods):

1. Two upstreams, same lamports `value`, different `context.slot` → **no**
   dispute; wait / return highest agreed slot per §3.2 — never punish for lag.
2. Two upstreams, same `context.slot`, different `value`, threshold unmet /
   tied → dispute under `returnError`.
3. Tip slot with 1 vote + older slot with 2 agreeing votes → wait; if tip
   gets a second agreeing vote before cap → return tip; else return older
   agreed.
4. Mix `minAgreement` enforced on winning **slot** cohort.
5. Slot-pinned strict path for `getBlock` / `getTransaction` unchanged
   (request-pinned; not slot-grouped moving-head).
6. Phase 2 (if shipped): a `getBalance` with `commitment: confirmed` is not
   cached as finalized solely because `context.slot ≤` finalized root.

---

## 8. Related

- Gaps inventory: [svm-consensus-gaps.md](./svm-consensus-gaps.md)
- Implementation plan: [plan.md](./plan.md)
- Envelope method inventory (`contextSlotMethods`):
  [`architecture/svm/hooks.go#L654-L672`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)
- Existing SVM finality: `architecture/svm/finality.go`
- Consensus executor: `consensus/executor.go`, `consensus/analysis.go`
- Envelope ignore defaults: `common/defaults.go` (`context.slot`,
  `context.apiVersion`)
