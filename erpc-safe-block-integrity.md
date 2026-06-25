# eRPC "safe" Block Integrity — Long-term Design

## TL;DR

`eth_getBlockByNumber("safe")` is forwarded verbatim to every upstream. Each provider interprets "safe" by its own `OP_NODE_VERIFIER_L1_CONFS` setting — the Company's 1P nodes use 12 L1 confirmations, while dRPC uses 4 and Blockdaemon/Alchemy use 0. With `agreementThreshold=2`, two 3P providers can win a consensus round with an aggressively-defined "safe" block, silently violating the Company's guarantee. Internal services using `FINALIZECHECKPOINT=safe` are exposed.

**Three structural gaps in eRPC:**

1. `"safe"` is forwarded verbatim to upstreams (unlike `"latest"`/`"finalized"` which have translation paths). Each upstream answers according to its own definition.
2. `enforceHighestBlock()` handles `"latest"` and `"finalized"` but has no case for `"safe"` — responses are never validated against an authoritative ceiling.
3. `GetFinality()` classifies all block tags — including `"safe"` and `"finalized"` — as `DataFinalityStateRealtime`, so the selection policy cannot differentiate `"safe"` from `"latest"` or `"pending"`.

**Note:** `servedTip` does not solve this. It addresses availability lag (upstreams that haven't seen a block yet). This is a definitional disagreement — different providers define "safe" at different L1 confirmation depths.

---

## How "latest" and "finalized" avoid this problem

`resolveBlockTagToHex()` in `architecture/evm/json_rpc.go` already translates `"latest"` and `"finalized"` to concrete hex block numbers **before forwarding to upstreams**. Both 1P and 3P therefore receive `eth_getBlockByNumber(0x64, ...)` — the same concrete number — so consensus can agree on the response.

| Tag | Translated? | How | Direction |
|-----|-------------|-----|-----------|
| `"latest"` | Yes | `EvmHighestLatestBlockNumber()` | max across upstreams |
| `"finalized"` | Yes | `EvmHighestFinalizedBlockNumber()` | max across upstreams |
| `"safe"` | No | passed verbatim — *"we don't have the necessary state information"* (code comment) | — |

For `"latest"` and `"finalized"`, using the **max** is correct: if upstream A has block 1005 and B has 1000, eRPC asks both for 1005. If B doesn't have it yet, `enforceHighestBlock()` catches the stale response and re-requests excluding that upstream.

For `"safe"`, the right direction is the **min** (most conservative). The Company's 1P node has the lowest safe block by design (12 L1 confs), so using the minimum enforces the guarantee. Using max would pick 3P's aggressive safe block — the opposite of what we want.

The code comment in `resolveBlockTagToHex()` says *"safe: Represents a specific consensus state between finalized and latest that we don't track."* This is exactly what **Axis 1** fixes — once `PollSafeBlockNumber()` exists, the state is available and the comment is no longer true.

---

## Long-term Solution — Four Axes

### Axis 1 — Track "safe" block per upstream (foundational)

Everything else depends on having per-upstream safe block data. The state poller currently only polls `"latest"` and `"finalized"`.

**Files:** `architecture/evm/evm_state_poller.go`, `upstream/upstream.go`, `erpc/networks.go`

- Add `safeBlockShared` counter to `EvmStatePoller`
- Add `PollSafeBlockNumber(ctx)` calling `eth_getBlockByNumber("safe", false)`
- Add `SafeBlock()` accessor and `EvmEffectiveSafeBlock()` on `Upstream`
- Network-level: `EvmLowestSafeBlockNumber(ctx)` — min effective safe block across all non-syncing upstreams

### Axis 2 — `enforceLowestBlock` for "safe" (response-time ceiling)

Even if routing is imperfect, the response can be clamped to the authoritative ceiling. Analogous to the existing `enforceHighestBlock()` — but enforcing a *maximum* on the safe block rather than a minimum. Fail closed if no safe block is available from any upstream.

**File:** `architecture/evm/eth_getBlockByNumber.go` — new `enforceLowestSafeBlock()` function called after `enforceHighestBlock()`

```
ceilingSafe := network.EvmLowestSafeBlockNumber(ctx)
if ceilingSafe == 0 → return error (fail closed — no ceiling available)
if respBlockNumber > ceilingSafe:
    re-request at concrete block number ceilingSafe (excluding the responding upstream)
```

### Axis 3 — Reclassify "safe" finality (selection-time routing, ~5 lines)

Currently `GetFinality()` classifies every block tag as `DataFinalityStateRealtime`, making `"safe"` and `"latest"` indistinguishable to the selection policy. Mapping `"safe"` to `DataFinalityStateFinalized` immediately enables `evalScope: "network-finality"` to route it separately.

**File:** `erpc/networks.go` — `GetFinality()`

```go
if blockRef == "safe" || blockRef == "finalized" {
    return DataFinalityStateFinalized
}
return DataFinalityStateRealtime
```

With this change operators can write:

```yaml
selectionPolicy:
  evalScope: "network-finality"
  evalFunc: |
    if (finality === 'finalized') return upstreams.filter(u => u.tags['tier'] === 'internal')
    return upstreams
```

### Axis 4 — `latestBlockMinus` upper bound per upstream for "safe" (operator-tunable)

Gives operators per-upstream control over how conservative a "safe" claim must be. A 3P upstream with `safeUpper.latestBlockMinus: 12` would have its effective safe block capped at `(its_latest - 12)`, matching the Company's L1-conf requirement.

**Files:** `common/config.go`, `common/validation.go`, applied in `EvmEffectiveSafeBlock()`

```yaml
upstreams:
  - id: blockdaemon-base
    evm:
      blockAvailability:
        safeUpper:
          latestBlockMinus: 12
```

---

## Minimum Solution (for mixed consensus)

**Context:** Axis 3 alone (routing "safe" to 1P only) breaks consensus setups that require at least one 1P *and* at least one 3P upstream to agree. For those deployments, the minimum viable fix is **Axis 1 + filling the gap in `resolveBlockTagToHex`**, with Axis 2 as defense-in-depth.

### Why mixed consensus breaks without a pre-forward rewrite

With `agreementThreshold=2` and exactly 1P + 3P as participants: 1P returns `safe=100`, 3P returns `safe=120`. Different block numbers → different response hashes → no quorum → **consensus fails with an error**. `enforceLowestSafeBlock` (Axis 2) never runs because it fires post-consensus, after a successful response. The client gets a consensus error — safe by accident, but unavailable.

### The fix: translate "safe" before forwarding (mirrors "latest" / "finalized")

Fill the missing `case "safe":` in `resolveBlockTagToHex()` (`architecture/evm/json_rpc.go`), calling `EvmLowestSafeBlockNumber()`. Both upstreams then receive the same concrete block number, consensus can agree, and the Company's guarantee is enforced at translation time.

```go
case "safe":
    if bn := network.EvmLowestSafeBlockNumber(ctx); bn > 0 {
        if hx, err := common.NormalizeHex(bn); err == nil {
            return hx, true
        }
    }
```

This mirrors exactly how `"finalized"` works — the only difference is `EvmLowest` instead of `EvmHighest`, because for "safe" the most conservative (lowest) value is the correct one.

### Full minimum solution: Axis 1 + tag rewrite + Axis 2

| Step | What | Effect |
|------|------|--------|
| 1 | **Axis 1** — poll "safe" per upstream, expose `EvmLowestSafeBlockNumber()` | Provides the state needed for everything else |
| 2 | **Tag rewrite** — `case "safe":` in `resolveBlockTagToHex()` | Translates "safe" to concrete block before forwarding; fixes mixed-consensus disagreement |
| 3 | **Axis 2** — `enforceLowestSafeBlock()` post-forward | Defense-in-depth: catches any response that still exceeds the ceiling |

**Fail-closed:** If `EvmLowestSafeBlockNumber()` returns 0 (no upstream has polled a safe block yet), `resolveBlockTagToHex` falls through and "safe" is passed verbatim — same behavior as today. Axis 2 then catches it post-forward and errors. No wrong data leaks through.

**Limitation:** When all 1P upstreams are down but 3P are up, `EvmLowestSafeBlockNumber()` returns the min across 3P providers — not the Company's 12-conf value. True fail-closed under 1P outage requires tag-based authoritative upstream filtering (follow-up work).

---

## Files Changed

| File | Change |
|------|--------|
| `common/architecture_evm.go` | Add `PollSafeBlockNumber`, `SafeBlock` to `EvmStatePoller` interface; `EvmEffectiveSafeBlock` to `EvmUpstream` interface |
| `common/network.go` | Add `EvmLowestSafeBlockNumber` to `Network` interface |
| `common/upstream_fake.go` | Implement new interface methods on fakes |
| `architecture/evm/evm_state_poller.go` | `safeBlockShared` field + `PollSafeBlockNumber()` + `SafeBlock()` + goroutine in `Poll()` |
| `upstream/upstream.go` | `EvmEffectiveSafeBlock()` |
| `erpc/networks.go` | `EvmLowestSafeBlockNumber()` |
| `architecture/evm/json_rpc.go` | `case "safe":` in `resolveBlockTagToHex()` |
| `architecture/evm/eth_getBlockByNumber.go` | New `enforceLowestSafeBlock()` + call in `networkPostForward_eth_getBlockByNumber` |

---

## Verification

1. Unit test: three upstreams with safe blocks {100, 120, 130}; assert `resolveBlockTagToHex("safe")` returns `0x64` (100).
2. Unit test: mixed consensus (1P safe=100, 3P safe=120); both receive concrete block 100; consensus agrees; response passes `enforceLowestSafeBlock` unchanged.
3. Unit test: safe block = 0 for all upstreams → "safe" passed verbatim → Axis 2 returns error.
4. Unit test: `PollSafeBlockNumber` skips after 10 consecutive failures (mirrors finalized block pattern).
5. `make test-fast` — confirms all existing tests pass and interfaces are satisfied.

---

## Implementation Order (full solution)

| # | What | Code size | Value |
|---|------|-----------|-------|
| 1 | Axis 1 — safe block tracking | ~100 lines | Foundational; required for all other steps |
| 2 | Tag rewrite in `resolveBlockTagToHex` | ~5 lines | Fixes mixed-consensus disagreement at the source |
| 3 | Axis 2 — `enforceLowestSafeBlock` | ~40 lines | Defense-in-depth; fail-closed guarantee |
| 4 | Axis 3 — reclassify "safe" finality | ~5 lines | Enables selection policy routing |
| 5 | Axis 4 — `latestBlockMinus` for safe | ~30 lines + config schema | Per-upstream operator tuning knob |
