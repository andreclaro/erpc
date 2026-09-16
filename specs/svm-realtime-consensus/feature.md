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
- Under end-state defaults, agreement requires the same payload at the same
  `context.slot` (slot is part of the hash digest).
- Winner selection is **count-first**, with highest slot only among equal top
  counts (§3.1) — a smaller group at a higher slot never beats a larger group.
- Slot-pinned strict consensus (`getBlock`, `getTransaction`, …) is unchanged.

### Goals

- Response-side pinning via `context.slot` (no request-side slot rewrite).
- End-state enveloped defaults + count-first / wait / misbehavior rules (§3).
- Optional **§4.1 paired finality** with/after §3; **§4.2 slot-aware cache**
  deferred until soak + neverCache open topic (§8) settled.
- Docs recommend raising `agreementThreshold` above 2 for financial methods
  when the upstream set is large enough (§3.3) — no product-default change.

### Non-goals

- Do **not** use `preferHighestValueFor` / `agreementThreshold: 1` on
  moving-head reads (tip routing, not consensus).
- Do **not** treat `minContextSlot` as a pin (floor only); do **not** invent
  a request-side historical slot the wire protocol does not provide.
- Out of scope ([svm-consensus-gaps.md](./svm-consensus-gaps.md)): SVM
  block-head leader behaviors, nested `preferHighestValueFor` paths,
  bare-`0` emptyish semantics. Operator failsafe / helm wiring is out of
  scope — this feature is executor behavior once a rule already matches.

---

## 2. Background (why today fails)

Why flat hash consensus cannot work for enveloped moving-head reads, using
`getBalance` as the running example.

### Moving-head reads

A moving-head read names *what* to look up (e.g. a pubkey) but not *which
slot* — each upstream answers at its current bank and reports that bank as
`context.slot`. Methods whose result *can* carry that envelope:
`contextSlotMethods` in
[`hooks.go`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)
(full list in §5).

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

Under `commitment: finalized` that bank is still the latest **rooted** tip —
it advances roughly once per slot — so two healthy nodes routinely return the
**same `value` at adjacent slots** (e.g. `12345@1000` and `12345@1001`).

**Slot duration note:** mainnet slot time is **~300ms today** (staged
reduction from the historical **400ms**; target **200ms**). See
[Reduced Slot Times](https://solana.com/upgrades/reduced-slot-times)
(SIMD-0525). Do not hard-code 400ms into wait budgets — derive from observed
inter-provider root lag / adaptive caps.

`GetFinality` correctly classifies `getBalance` **realtime** at every
commitment (`architecture/svm/finality.go` — also in `neverCacheMethods`).
That is a cacheability fact, not a consensus strategy. Slot-pinned methods
(`getBlock`, `getTransaction`) are out of this problem: the request already
names the slot or signature.

### Why naive consensus fails

Today’s defaults ignore **both** `context.slot` and `context.apiVersion` in
the value hash (`common/defaults.go`). Ignoring `apiVersion` is right
(mixed validators). Ignoring **`context.slot`** collapses adjacent tips into
one bucket: flat agreement mixes **different questions**. That yields **false
disputes** when tip churn briefly splits values, or a **stale majority** when
an older slot outvotes a fresher tip, under `returnError` /
`agreementThreshold ≥ 2`.

Cross-slot lag is expected SVM behavior, not misbehavior. Same-slot
**`value`** split is a real dispute. Fix: end-state defaults + count-first
winner (§3) — dropping `context.slot` from ignores alone is not enough.

### Why EVM’s approach does not transfer

| | EVM | SVM `getBalance` (and other enveloped methods) |
|---|---|---|
| Pin location | Request (`latest`/`finalized` → block **number**) | No slot in `params` to rewrite |
| `minContextSlot` | N/A | Floor only, **not** a pin |
| Self-pin | Block number in request | **`result.context.slot` in the response** |

---

## 3. Solution — moving-head consensus

Consensus grouping is unchanged at the mechanism layer: hash each successful
response with that method’s `ignoreFields`, then apply threshold / dispute /
prefer rules. This feature changes the **defaults** for enveloped SVM methods
and the **winner policy** when those responses carry `context.slot`.

`getBalance` responses already self-pin via `result.context.slot`.
`value @ rooted-slot-N` is immutable for that N. Under end-state defaults,
identical lamports at different slots are **different** hashes.

### 3.0 Hashing and `ignoreFields`

Consensus digests the **entire JSON-RPC result** after removing paths listed
in `ignoreFields[method]` (`CanonicalHashWithIgnoredFields`). That map is
**per-method** and operator-overridable (set replacement, not merge).

**Today** (`common/defaults.go`):

```text
ignoreFields[method] = ["context.slot", "context.apiVersion"]
```

**End state:** remove `context.slot` from that default list:

```text
ignoreFields[method] = ["context.apiVersion"]
```

For default `getBalance`, the digest therefore includes `context.slot` and
`value`, but not `apiVersion`. This alone is insufficient without §3.1–3.6.

### 3.1 Algorithm

How one consensus round decides a winner for an enveloped read such as
`getBalance` under end-state defaults:

1. Fan out to `maxParticipants` upstreams (existing executor).
2. Hash each successful response with that method’s `ignoreFields` (end-state
   default for enveloped SVM: ignore only `context.apiVersion`).
3. A hash group **qualifies** when `count ≥ agreementThreshold`.
4. **Winner (count-first, slot-tiebreak):**
   - Let `C` = maximum `count` among qualifying groups.
   - Let `S` = qualifying groups with `count == C` that expose a parseable
     `context.slot`.
   - If `S` is non-empty → winner = member of `S` with the **highest**
     `context.slot` (freshest among equal top counts).
   - Else → today’s count-based winner among all qualifying groups (no slot
     ranking).
   - **Mixed slotted / non-slotted:** if any qualifying group has a
     parseable slot, only slotted groups compete for the slot-tiebreak path
     above; a non-slotted group never wins while a slotted group also
     qualifies (even with equal count). Non-slotted winners only when **no**
     slotted group qualifies.
5. Same slot + different values at/above threshold with no unique winner →
   real dispute → `disputeBehavior` (production: `returnError`).
6. Different slots alone are **not** a dispute; keep collecting until
   wait-caps fire or all participants answer.
7. If no group qualifies by round end (`maxWaitOnResult` / collection done) →
   `returnError` under production policy (or low-participants if
   `validParticipants < agreementThreshold`).

**Security note:** a minority at a fabricated high slot **cannot** beat a
larger honest group at a lower slot. Example: 3× `V@1000` and 2× `V'@1050`
(both ≥ threshold) → winner is `V@1000` (count 3 > 2). Slot only breaks ties
when counts are equal (e.g. 2× `V@1000` vs 2× `V@1002` → `V@1002`).

Example round for one `getBalance` (lamports @ slot), equal counts:

```
t=0:  QN=12345@1000, Alchemy=12350@1002, Helius=12345@1000
      → 12345@1000 (2) qualifies; 12350@1002 (1) does not
t=+Δ: Helius → 12350@1002
      → both groups count=2 → pick highest slot → 12350@1002
```

### 3.2 Wait semantics

When a top-count group already qualifies, a higher slot may still reach the
**same** count with remaining participants — wait (within the cap) before
locking the lower slot among equal counts.

Default: **wait** up to `maxWaitOnResult` while remaining participants could
still form another qualifying group with `count == C` (current max qualifying
count) at a **higher** `context.slot`. When the wait cap fires (or all
participants have answered), apply §3.1 on what is collected.

Rationale: among equal counts, prefer the fresher tip when the wait budget
allows; waiting bounds p99 (~inter-provider root lag; often ~1–2 slots ≈
0.3–0.6s at today’s ~300ms slot time — tune from soak). Do **not** wait to
let a *smaller* group at a higher slot overturn a larger older majority.

`maxWaitOnResult: 0` / `maxWaitOnEmpty: 0` means **no time cap** (collect
until all participants answer or short-circuit). That is valid — often more
patient than a short cap. It is **not** “return on first response.” Prefer a
bounded non-zero wait when p99 must be capped.

### 3.3 Activation

Enter slot-grouped mode when **all** of:

- A consensus policy is active for the request, and
- At least one collected successful response has a parseable `context.slot`.

Fallthrough (no parseable `context.slot` on any success): existing hash
consensus unchanged. The hot path discovers the slot from the response body
(not a method-name switch). Failsafe `matchMethod` chooses which enveloped
methods (§5) operators enable under consensus.

No config knob whose “off” setting re-enables never-right naive hashing for
enveloped responses under `returnError`. Roll out with a **binary / network
canary**; rollback is redeploy of the previous binary (not a “restore ignore
`context.slot`” flag).

**Key rule under `agreementThreshold ≥ 2`:** a tip slot with one vote does not
qualify; “most updated” alone is still single-provider trust.

**Docs recommendation (no product-default change):** operators may raise
`agreementThreshold` above 2 for `getBalance`, `getAccountInfo`,
`getTokenAccountBalance`, `getMultipleAccounts`, and other financial /
priority-soak enveloped methods when the upstream set is large enough.

### 3.4 Misbehavior

| Situation | Misbehavior? |
|---|---|
| Same `context.slot`, different `value` vs winning group | **Yes** |
| Different `context.slot` (lag / ahead of winner) | **No** |
| Infrastructure / consensus-valid errors | Existing rules (errors ≠ data misbehavior) |

`punishMisbehavior` / cordon only applies to same-slot value dissenters when
the winning group has a clear majority (`count > validParticipants/2` within
the **winning slot cohort**, not the whole round).

### 3.5 Composition quotas

Mix quotas (`requiredParticipants` / `minAgreement`) still apply, but only
among upstreams that agreed on the winning value **at the winning slot**.
Cross-slot participants do not count toward the quota.

`eth_sendRawTransaction` / SVM send broadcast exemption unchanged (not this
path).

### 3.6 Short-circuit

Do **not** short-circuit while `R` remaining participant responses could
still raise some hash group’s count to the current top qualifying count `C`
at a **higher** `context.slot` than the provisional winner (or could create a
new top count). Only participant **count** enters the predicate — not observed
inter-provider lag.

- Do **not** short-circuit to a lone tip-slot response (`count < agreementThreshold`).
- Existing unassailable-lead / error-threshold short-circuits otherwise apply
  when the rule above says no higher equal-count tip can still form.

---

## 4. Follow-ons — paired finality vs cache

These are **two separate layers**. §3 consensus is correct without either.

| Layer | Decides | Consumers |
|---|---|---|
| **§4.1 Paired finality** | Is this response immutable at `context.slot`? → `DataFinalityState` | `matchFinality`, metrics, anything keyed on finality |
| **§4.2 Slot-aware cache** | Given finality + policies, how to **store/lookup** | `SvmJsonRpcCache` Get/Set only |

Caching is an *effect* of finality + policy (+ optional key shape). Do not
define pairing as “make it cacheable.”

**Gating:** §4.1 may ship with or after §3. **§4.2 is deferred** until §3
soak looks healthy and the neverCache open topic (§8) is settled.

### 4.1 Paired finality

**Today:** every moving-head enveloped read is `realtime` for `GetFinality`
(including `commitment: finalized`), because the request names no slot.

**After §4.1:** classify the response **`finalized`** (immutable **at that
slot**) when **all** hold:

1. A successful response (consensus winner or single success) has parseable
   `result.context.slot` = `N`.
2. `N ≤` the network’s served finalized tip — prefer
   `Network.SvmHighestFinalizedSlot` (`PickServedTip` / majority-style over
   upstream pollers), not a single upstream’s poller alone (exact wiring in
   [plan.md](./plan.md)).
3. The request’s **effective commitment** is `finalized`
   (`IsFinalizedCommitment` / same predicate as injection).

**Must not:** promote `commitment: confirmed` / `processed` solely because
`context.slot ≤` tip.

**Example:** caller asks with `commitment: finalized`; answer has
`context.slot: 1000`; network served finalized tip is `1005` → response
finality = **`finalized`** (at slot 1000). This does **not** by itself write
the cache (see §4.2 and `neverCacheMethods`).

### 4.2 Slot-aware cache (separate)

**Today** (`getAccountInfo`, not `getBalance`):

- Finality `realtime` → matches `finality: realtime` policies.
- Partition key `networkId:*` (or `minContextSlot` if present) — **no**
  poller tip, **no** `PickServedTip`, **no** response `context.slot`.
- Staleness = policy **TTL** only.
- `getBalance` / `getTokenAccountBalance` are hard-skipped by
  `neverCacheMethods` even under a realtime policy.

**§4.2 (optional):** once §4.1 can mark an answer `finalized`, cache
policies with `finality: finalized` can match. Get/Set should key the slot
dimension from the **network served finalized tip** and/or the response
`context.slot` on Set — **not** from client params. Example:

```text
Request 1 (tip still 1000): Get(…, slot=1000) MISS → upstream → §4.1
  finalized → Set(…, slot=1000)
Request 2 (identical curl, tip still 1000): Get(…, slot=1000) HIT
Request 3 (tip now 1001): Get(…, slot=1001) MISS → refetch → Set(…, 1001)
```

Entry at 1000 must not answer tip 1001. `neverCacheMethods` still wins unless
revisited ([§8](#8-open-topics)). §4.1 can still classify `getBalance` as
finalized for non-cache consumers.

### Ship order

Prefer §3 first (canary binary/network). §4.1 optional with/after §3. §4.2
only after soak + §8 neverCache decision. Prefer §4.1 before §4.2.

---

## 5. In scope / out of scope

### Envelope inventory

Known Solana `RpcResponse<T>` set in `contextSlotMethods`
([`hooks.go` L654–L672](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)):

`getAccountInfo`, `getBalance`, `getBlockProduction`, `getFeeForMessage`,
`getLargestAccounts`, `getLatestBlockhash`, `getMultipleAccounts`,
`getProgramAccounts` (envelope only with `withContext:true`),
`getSignatureStatuses`, `getStakeMinimumDelegation`, `getSupply`,
`getTokenAccountBalance`, `getTokenAccountsByDelegate`,
`getTokenAccountsByOwner`, `getTokenLargestAccounts`, `getTokenSupply`,
`isBlockhashValid`, `simulateTransaction`.

Same methods use enveloped `ignoreFields` defaults; end state is §3.0.

### Enable under slot-grouped consensus (failsafe)

Operators choose `matchMethod` coverage; soak the financial moving-head set
first.

**Priority soak:** `getAccountInfo`, `getBalance`, `getTokenAccountBalance`,
`getMultipleAccounts`.

**Also suitable** (same moving-head class): token/program account reads
(`getTokenAccountsByOwner` / `ByDelegate`, `getTokenLargestAccounts`,
`getProgramAccounts` with context), `getSupply` / `getTokenSupply`,
`getStakeMinimumDelegation`, `isBlockhashValid`, etc. — enable via
`matchMethod` once soak looks healthy.

**Usually keep off this rule:** `getLatestBlockhash` (fastest-wins),
`getFeeForMessage`, `getSignatureStatuses`, `getBlockProduction` /
`getLargestAccounts` (volatile), `simulateTransaction`.

### Out of scope (non-envelope / already special-cased)

- Bare / non-envelope: `getBlocks`, `getSignaturesForAddress`, `getHealth`,
  bare integers (`getSlot`, `getBlockHeight`, …).
- Already handled elsewhere: `getSlot` / `getBlockHeight` (freshest-wins),
  `getLatestBlockhash` (fastest-wins — even though enveloped, tip policy
  stays), `sendTransaction` (broadcast), slot-pinned strict (`getBlock`,
  `getTransaction`, `getBlockTime`, …).

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

1. Same lamports, different `context.slot`, equal counts at threshold → **no**
   dispute; highest slot wins among equal counts (§3.1) — never punish for lag.
2. Same `context.slot`, different `value`, threshold unmet / tied → dispute
   under `returnError`.
3. **Count-first security:** 3× `V@1000` and 2× `V'@1050` (both ≥ threshold)
   → winner `V@1000`.
4. Tip with count below the current top count does not overturn; wait only
   while remaining participants can still form an **equal** top count at a
   higher slot (§3.2 / §3.6).
5. Mixed slotted + non-slotted qualifying groups → slotted path wins; non-
   slotted only if no slotted group qualifies.
6. Mix `minAgreement` enforced on winning **slot** cohort.
7. Slot-pinned strict path for `getBlock` / `getTransaction` unchanged.
8. §4.1 (if shipped): `commitment: confirmed` is **not** classified
   `finalized` solely because `context.slot ≤` tip; finalized commitment +
   slot ≤ `SvmHighestFinalizedSlot` may be.
9. §4.2 (when un-deferred): unpinned `getAccountInfo` Get uses served
   finalized tip as slot key; tip advance → miss.

---

## 8. Open topics

1. **`getBalance` vs `getAccountInfo` cacheability (§4.2 only).** Today both
   are moving-head / `realtime` for finality, but `getBalance` (and
   `getTokenAccountBalance`) are in `neverCacheMethods` (hard Get/Set skip),
   while `getAccountInfo` is not. §4.1 can still mark either `finalized`.
   Open for **cache**:
   - Keep status quo (`getAccountInfo` TTL-/finalized-cacheable; balances
     never stored)?
   - Put account reads on never-cache too?
   - Allow balances out of never-cache under slot-keyed finalized Get/Set?
   Not blocking §3 or §4.1; **blocks un-deferring §4.2**.

---

## 9. Related

- Gaps inventory: [svm-consensus-gaps.md](./svm-consensus-gaps.md)
- Implementation plan: [plan.md](./plan.md)
- Envelope inventory: [`architecture/svm/hooks.go#L654-L672`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)
- SVM finality: `architecture/svm/finality.go`
- Consensus executor: `consensus/executor.go`, `consensus/analysis.go`
- Envelope ignore defaults: `common/defaults.go` (§3.0)
