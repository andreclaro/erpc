# SVM Slot-Grouped Consensus for Moving-Head Reads — Specification

**Last revised**: 2026-09-16

Companions: [plan.md](./plan.md) · [svm-consensus-gaps.md](./svm-consensus-gaps.md)

---

## 1. Purpose

This feature makes multi-provider consensus safe for Solana moving-head reads
without treating healthy tip lag as disagreement.

Enable **multi-provider consensus** on Solana moving-head reads
(`getBalance`, `getAccountInfo`, `getTokenAccountBalance`, …) without the
false disputes that naive hash agreement produces when healthy upstreams
answer at adjacent rooted slots.

After this feature:

- Financially critical enveloped reads no longer trust a single upstream.
- Two providers must agree on the **same value at the same `context.slot`**
  before a result is served under `returnError` (under end-state defaults,
  slot is part of the hash digest).
- Among qualifying groups, **count is primary**; when multiple groups share
  the top count, the **highest `context.slot`** wins (freshest among equals).
  A smaller group at a higher slot never beats a larger group at a lower slot.
- Slot-pinned strict consensus (`getBlock`, `getTransaction`, …) is unchanged.

### Goals

What we intend to ship in the consensus executor (and optionally later in
cache):

- Response-side pinning via `context.slot` (Solana has no request-side slot
  rewrite analogous to EVM tag→block-number).
- Consensus still hashes the **full response** minus that method’s
  `ignoreFields` (per-method, operator-overridable). End-state **defaults**
  for enveloped SVM methods drop `context.slot` from the ignore list (keep
  only `context.apiVersion`), so adjacent tips no longer collapse into one
  bucket.
- **Count-first winner selection:** among groups with `count ≥
  agreementThreshold`, take those with the **maximum count**; among that
  set, if they expose `context.slot`, pick the **highest slot**. Slot never
  outranks a larger agreeing group (blocks “2 fake tip beats 3 honest”).
- Prefer waiting long enough for a second provider to catch a tip slot
  (bounded `maxWaitOnResult`, or `0` = wait until all participants /
  short-circuit — not “return on first response”).
- Misbehavior only for **same-slot value dissent** — cross-slot lag is not
  misbehavior.
- Optional **§4.1 paired finality** with/after §3; **§4.2 slot-aware cache**
  deferred until soak + neverCache open topic settled.

### Non-goals

Deliberately out of this change so the design stays weak and reviewable:

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

Before the algorithm: why flat hash consensus cannot work for enveloped
moving-head reads, using `getBalance` as the running example.

### Moving-head reads

A moving-head read names *what* to look up (e.g. a pubkey) but not *which
slot* — each upstream answers at its current bank and reports that bank as
`context.slot`. The methods whose result *can* carry that envelope are listed
as `contextSlotMethods` in
[`hooks.go`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672);
`getBalance` is the running example below.

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
is still the latest **rooted** tip — it advances roughly once per slot — so
two healthy nodes routinely return the **same `value` at adjacent slots**
(e.g. `12345@1000` and `12345@1001`).

**Slot duration note:** mainnet slot time is **~300ms today** (staged
reduction from the historical **400ms**; target **200ms**). See
[Reduced Slot Times](https://solana.com/upgrades/reduced-slot-times)
(SIMD-0525). Do not hard-code 400ms into wait budgets — derive from observed
inter-provider root lag / adaptive caps. Wall-clock “one slot” shrinks as
gates activate; adjacent-slot false disputes only get *more* common, not less.

`GetFinality` correctly classifies `getBalance` **realtime** at every
commitment (`architecture/svm/finality.go` — also in `neverCacheMethods`).
That is a cacheability fact, not a consensus strategy. Slot-pinned methods
(`getBlock`, `getTransaction`) are out of this problem: the request already
names the slot or signature, so strict hash consensus compares one question.

### Why naive consensus fails on them

Today’s defaults ignore **both** `context.slot` and `context.apiVersion` in
the value hash (`common/defaults.go`). Ignoring `apiVersion` is right
(mixed validators). Ignoring **`context.slot`** collapses adjacent tips into
one bucket: flat agreement mixes **different questions** — e.g. `getBalance`
at slot 1000 and 1001 hash alike when lamports match. That yields **false
disputes** when tip churn briefly splits values, or a **stale majority** when
an older slot outvotes a fresher tip, under `returnError` /
`agreementThreshold ≥ 2`.

**End state:** stop ignoring `context.slot` in the enveloped-method defaults
(keep ignoring only `context.apiVersion`). Grouping stays the ordinary
canonical hash of the whole result after that method’s `ignoreFields` — there
is no separate hasher. For default `getBalance`, slot stays in the digest.
That alone is not enough — winner selection must be **count-first** (highest
slot only among equal top counts), wait when a higher equal-count tip can
still form, and treat cross-slot lag as non-misbehavior.

Cross-slot lag is expected SVM behavior, not misbehavior. Same-slot
**`value`** split (two balances at slot 1000) is a real dispute.

### Why EVM’s approach does not transfer

EVM can rewrite a block tag on the request; Solana moving-head reads cannot.

| | EVM | SVM `getBalance` (and other `contextSlotMethods`) |
|---|---|---|
| Pin location | Request (`latest`/`finalized` → block **number**) | No slot in `params` to rewrite |
| `minContextSlot` | N/A | Floor only, **not** a pin |
| Self-pin | Block number in request | **`result.context.slot` in the response** |

Response-side fix: drop `context.slot` from default `ignoreFields` (hash =
full result minus per-method ignores), then **count-first** among groups ≥
`agreementThreshold`, with highest `context.slot` only among equal top counts.

---

## 3. Solution — moving-head consensus

Consensus grouping is unchanged at the mechanism layer: hash each successful
response with that method’s `ignoreFields`, then apply threshold / dispute /
prefer rules. This feature changes the **defaults** for enveloped SVM methods
and the **winner policy** when those responses carry `context.slot`.

`getBalance` responses already self-pin via `result.context.slot` (see §2).
`value @ rooted-slot-N` is immutable for that N. Under end-state defaults,
identical lamports at different slots are **different** hashes; agreement
requires the same tip bank and the same payload (modulo ignored fields).

### 3.0 Hashing and `ignoreFields`

Consensus digests the **entire JSON-RPC result** after removing paths listed
in `ignoreFields[method]` (`CanonicalHashWithIgnoredFields`). That map is
**per-method** and operator-overridable (set replacement, not merge). Only
the ignore list changes what enters the digest.

**Today** (`common/defaults.go`): enveloped methods default to

```text
ignoreFields[method] = ["context.slot", "context.apiVersion"]
```

**End state:** remove `context.slot` from that default list:

```text
ignoreFields[method] = ["context.apiVersion"]
```

For default `getBalance`, the digest therefore includes `context.slot` and
`value`, but not `apiVersion`. Other methods keep whatever ignore list they
are configured with (EVM timestamp ignores, custom operator maps, etc.).

This defaults change alone is insufficient (see §3.1–3.6): without a
count-first + slot-tiebreak rule, a stale majority and a fresher minority
(or a fabricated high slot) are mishandled.

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
bounded non-zero wait when p99 must be capped; do not confuse `0` with
disabled waiting for agreement.

### 3.3 Activation

Slot-grouping turns on from the response shape under an active consensus
policy — not from a hard-coded method switch in the hot path.

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
enveloped responses under `returnError`. Roll out with a **binary / network
canary**; rollback is redeploy of the previous binary (not a “restore ignore
`context.slot`” flag).

**Key rule under `agreementThreshold ≥ 2`:** a tip slot with one vote does not
qualify; “most updated” alone is still single-provider trust. Operators may
raise the threshold further for financial methods — see §8.

### 3.4 Misbehavior

Only same-slot value dissenters are misbehaving; lagging or leading another
slot is expected.

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

Unchanged contract, applied to the **winning slot cohort’s** agreeing
participants: if mix consensus requires ≥1 `type:internal` and ≥1
`type:external` in the winner, those tags must appear among the upstreams that
voted for the winning value **at the winning slot**. Cross-slot participants
do not count toward the quota.

`eth_sendRawTransaction` / SVM send broadcast exemption unchanged (not this
path).

### 3.6 Short-circuit

Early exit must not crown a lone tip or skip a higher **equal-count** tip
that remaining participants can still form.

Concrete rule: do **not** short-circuit while `R` remaining participant
responses could still raise some hash group’s count to the current top
qualifying count `C` at a **higher** `context.slot` than the provisional
winner (or could create a new top count). Only participant **count** enters
the predicate — not observed inter-provider lag.

- Do **not** short-circuit to a lone tip-slot response (`count < agreementThreshold`).
- Existing unassailable-lead / error-threshold short-circuits otherwise apply
  when the rule above says no higher equal-count tip can still form.

---

## 4. Follow-ons — paired finality vs cache

These are **two separate layers**. §3 consensus is correct without either.
Splitting them is for clear ownership.

| Layer | Decides | Consumers |
|---|---|---|
| **§4.1 Paired finality** | Is this response immutable at `context.slot`? → `DataFinalityState` | `matchFinality`, metrics, anything keyed on finality |
| **§4.2 Slot-aware cache** | Given finality + policies, how to **store/lookup** | `SvmJsonRpcCache` Get/Set only |

Caching is an *effect* of finality + policy (+ optional key shape). Do not
define pairing as “make it cacheable.”

**Gating:** §4.1 may ship with or after §3. **§4.2 is deferred** until §3
soak looks healthy and the neverCache open topic (§8) is settled.

### 4.1 Paired finality

Response-side analogue of EVM tag→block-number rewrite: once a success
carries `context.slot`, we may know a stronger fact than “moving tip.”

**Today:** every moving-head enveloped read is `realtime` for
`GetFinality` (including `commitment: finalized`), because the request names
no slot.

**After §4.1:** when **all** of the following hold, classify the response
**`finalized`** (immutable **at that slot**):

1. A successful response (consensus winner or single success) has parseable
   `result.context.slot` = `N`.
2. `N ≤` the network’s served finalized tip — prefer
   `Network.SvmHighestFinalizedSlot` (`PickServedTip` / majority-style over
   upstream pollers), not a single upstream’s poller alone (exact wiring in
   [plan.md](./plan.md)).
3. The request’s **effective commitment** is `finalized`
   (`IsFinalizedCommitment` / same predicate as injection).

**Must not:** promote `commitment: confirmed` / `processed` solely because
`context.slot ≤` tip. Weaker commitment is not rooted-bank evaluation.

**Example (`getAccountInfo` or `getBalance` — finality only):**

Caller asks with `commitment: finalized`. Answer has `context.slot: 1000`.
Network served finalized tip is `1005`.

- → response finality = **`finalized`** (at slot 1000).
- Failsafe / metrics see `finality=finalized`.
- This does **not** by itself write the cache (see §4.2 and `neverCacheMethods`).

### 4.2 Slot-aware cache (separate)

**Today** (`getAccountInfo`, not `getBalance`):

- Finality `realtime` → matches `finality: realtime` policies.
- Partition key `networkId:*` (or `minContextSlot` if present) — **no**
  poller tip, **no** `PickServedTip`, **no** response `context.slot`.
- Staleness = policy **TTL** only (no block-timestamp age guard).
- `getBalance` / `getTokenAccountBalance` are hard-skipped by
  `neverCacheMethods` even under a realtime policy.

**§4.2 (optional):** once §4.1 can mark an answer `finalized`, cache
policies with `finality: finalized` can match. To avoid serving “account at
old tip” under `"*"`, Get/Set should key the slot dimension from the
**network served finalized tip** (same tip §4.1 compared against) and/or the
response `context.slot` on Set — **not** from client params (clients usually
send none). Example flow:

```text
Request 1 (tip still 1000): Get(…, slot=1000) MISS → upstream → §4.1
  finalized → Set(…, slot=1000)
Request 2 (identical curl, tip still 1000): Get(…, slot=1000) HIT
Request 3 (tip now 1001): Get(…, slot=1001) MISS → refetch → Set(…, 1001)
```

Entry at 1000 must not answer tip 1001.

`neverCacheMethods` still wins on Get/Set unless that list is revisited
([open topic](#8-open-topics)). §4.1 can still classify `getBalance` as
finalized for non-cache consumers.

### Ship order

Prefer §3 first (canary binary/network). §4.1 optional with/after §3. §4.2
only after soak + §8 neverCache decision. Prefer §4.1 before §4.2 (cache
without paired finality cannot safely treat moving-head as immutable).

---

## 5. In scope / out of scope

Which methods can carry `context.slot`, which to soak first under consensus,
and what this feature explicitly does not own.

### Envelope inventory

Full list of result shapes that *can* carry a context slot (discovery still
reads the body; this table documents the known Solana `RpcResponse<T>` set in
`contextSlotMethods` —
[`hooks.go` L654–L672](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)):

`getAccountInfo`, `getBalance`, `getBlockProduction`, `getFeeForMessage`,
`getLargestAccounts`, `getLatestBlockhash`, `getMultipleAccounts`,
`getProgramAccounts` (envelope only with `withContext:true`),
`getSignatureStatuses`, `getStakeMinimumDelegation`, `getSupply`,
`getTokenAccountBalance`, `getTokenAccountsByDelegate`,
`getTokenAccountsByOwner`, `getTokenLargestAccounts`, `getTokenSupply`,
`isBlockhashValid`, `simulateTransaction`.

Same methods appear in consensus `ignoreFields` defaults today with
`context.slot` **and** `context.apiVersion` stripped. **This feature’s end
state** removes `context.slot` from that default (keep only
`context.apiVersion`). Hashing remains “full result minus per-method
`ignoreFields`.” See §3.0.

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

**Usually keep off this rule** (already have tip / other policies, or poor
agreement fit): `getLatestBlockhash` (fastest-wins), `getFeeForMessage`,
`getSignatureStatuses`, `getBlockProduction` / `getLargestAccounts`
(volatile), `simulateTransaction`.

### Out of scope (non-envelope / already special-cased)

Bare results and already-special-cased methods stay on today’s paths — they
are not the moving-head false-dispute problem.

- Bare / non-envelope: `getBlocks`, `getSignaturesForAddress`, `getHealth`,
  bare integers (`getSlot`, `getBlockHeight`, …).
- Already handled elsewhere: `getSlot` / `getBlockHeight` (freshest-wins),
  `getLatestBlockhash` (fastest-wins — even though it *is* enveloped, tip
  policy stays), `sendTransaction` (broadcast),
  slot-pinned strict (`getBlock`, `getTransaction`, `getBlockTime`, …) —
  request-pinned; **not** the moving-head issue.

---

## 6. Observability

What to watch so soak can prove slot-grouping fixed false disputes without
punishing lag.

| Signal | Purpose |
|---|---|
| Existing `erpc_consensus_*` with `finality=realtime` | Baseline volume / dispute / low_participants |
| New (recommended): `erpc_consensus_slot_groups` / winning `context.slot` span attr | Prove grouping; debug false disputes |
| Misbehavior metric must not spike on cross-slot lag | Regress if 3.4 is broken |
| `erpc_consensus_wait_capped_total{trigger}` | Tune wait vs root lag |

---

## 7. Acceptance criteria

Concrete checks, framed on `getBalance` (same rules for other enveloped
moving-head methods):

1. Two upstreams, same lamports `value`, different `context.slot`, equal
   counts at threshold → **no** dispute; highest slot wins among equal
   counts (§3.1) — never punish for lag.
2. Two upstreams, same `context.slot`, different `value`, threshold unmet /
   tied → dispute under `returnError`.
3. **Count-first security:** 3× `V@1000` and 2× `V'@1050` (both ≥ threshold)
   → winner `V@1000` (larger count); fabricated higher slot does not win.
4. Tip with count below the current top count does not overturn; wait only
   while remaining participants can still form an **equal** top count at a
   higher slot (§3.2 / §3.6).
5. Mixed slotted + non-slotted qualifying groups → slotted path wins; non-
   slotted only if no slotted group qualifies.
6. Mix `minAgreement` enforced on winning **slot** cohort.
7. Slot-pinned strict path for `getBlock` / `getTransaction` unchanged
   (request-pinned; not moving-head).
8. §4.1 (if shipped): `commitment: confirmed` enveloped success is **not**
   classified `finalized` solely because `context.slot ≤` tip; finalized
   commitment + slot ≤ `SvmHighestFinalizedSlot` may be.
9. §4.2 (when un-deferred): cache Get for unpinned `getAccountInfo` uses
   served finalized tip as slot key; tip advance → miss.

---

## 8. Open topics

Decisions not forced by the false-dispute bug; settle before or during
§4.1 / §4.2.

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

2. **Recommended `agreementThreshold` for financial methods (docs only).**
   No product-default change in this feature. Document that operators may
   raise threshold above 2 for `getBalance` / `getAccountInfo` when the
   upstream set is large enough — out of band from this code change.

---

## 9. Related

Pointers into gaps, plan, and the code that already defines envelopes,
finality, and consensus.

- Gaps inventory: [svm-consensus-gaps.md](./svm-consensus-gaps.md)
- Implementation plan: [plan.md](./plan.md)
- Envelope method inventory (`contextSlotMethods`):
  [`architecture/svm/hooks.go#L654-L672`](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672)
- Existing SVM finality: `architecture/svm/finality.go`
- Consensus executor: `consensus/executor.go`, `consensus/analysis.go`
- Envelope ignore defaults: `common/defaults.go` — today strips `context.slot`
  + `context.apiVersion`; end state strips **only** `context.apiVersion` (§3.0)
