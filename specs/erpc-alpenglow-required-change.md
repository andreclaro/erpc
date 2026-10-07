# eRPC Required Changes for Alpenglow

> Engineering companion to [alpenglow-rpc-erpc-impact.md](alpenglow-rpc-erpc-impact.md) — protocol context and the "why" lives there; required code/config changes live here.
> Verified against `andreclaro/erpc` **main @ `14c268f0`** (2026-10-08). Line references are that tree.

## 0. Decisions

- **D1 — Keep both poller tracks.** `getSlot(processed)` and `getSlot(finalized)` both stay (see impact doc §3 for reasoning: one can't be derived from the other; distinct consumers; the gap is a free health metric; per-track rollback tripwires; trivial cost). The change is in the *thresholds around the tracks*, not the tracks.
- **D2 — All changes activate off runtime signals.** Alpenglow state is probed per upstream (`getAgGenesisCert`); slot duration is measured from the chain. Land the code any time — every behavior switch flips only when the upstream actually migrates. Never a date, version string, or compiled-in constant (same philosophy as the integrity work's chain-safety invariant). No new required config.

## 1. Source code changes

### 1.1 `architecture/svm/svm_state_poller.go` — cadence and rollback dimension
- **`DefaultPollInterval = 400 * time.Millisecond` (line 23).** Hardcoded to one TowerBFT slot. At 200ms slots the poller ticks once per *two* slots and its own comment ("polling more often buys no fresher data") inverts. **Change:** tick at ½ × measured slot duration (floor at a sane minimum, e.g. 100ms). Slot duration is derived per network from successive `getSlot` observations (or `getEpochSchedule` + wall clock), cached and re-checked on an epoch-ish cadence — D2.
- **`DefaultToleratedSlotRollback = 1024` slots (line 19).** Slot-distance concept; survives the rate change unchanged. **Change:** none functional — add a comment pinning the dimension ("slot distance, deliberately NOT time-derived") so a future reader doesn't "fix" it.
- **Dual tracks (D1).** Keep both `latestShared`/`finalizedShared` counters and both `OnLargeRollback` hooks. **Change:** expose `LatestFinalizedGap()` (processed − finalized) as a poller metric; expected value is era-derived (≈32 slots TowerBFT, ≈1 slot Alpenglow — read from the same runtime state as 1.1). A gap that blows past the era's expectation is a stronger upstream-health signal than either track alone.

### 1.2 `common/defaults.go` — time-flavored thresholds
- **`StatePollerDebounce = 400ms` (≈line 2564, comment "400ms matches one Solana slot").** **Change:** derive from measured slot duration like 1.1; keep the YAML override as the escape hatch.
- **`MaxFinalizedSlotLag` default = `MaxShredInsertSlotLagThreshold` (100) (≈line 2576).** The comment "100 slots (~40s)" proves this is a *time* intent expressed in slots — halves to ~20s at 200ms slots without any code change, silently tightening the consensus slot-lag filter. **Change:** express the default internally as a duration (~40s), convert to slots at evaluation time using the measured rate. Backwards-compatible: an explicitly configured integer still means slots.
- **Post-activation tightening (opportunity, not correctness):** once finalized lag ≈ 1 slot, the ~40s-equivalent allowance is ~200 slots of slack — tighten the default for sharper anomaly detection, gated on the runtime Alpenglow signal (D2).

### 1.3 `common/architecture_svm.go` — shred-insert threshold
- **`MaxShredInsertSlotLagThreshold = 100` slots (line 56).** Same "~40s" time intent, same silent halving. **Change:** same duration-internal treatment as 1.2, sharing one slot-rate source. The cordon path (`IsHealthy`, `RecordBlockHeadLargeRollback`) consumes the converted value unchanged.

### 1.4 `architecture/svm/finality.go` — cache staleness math
- Comment at line 20: realtime methods "go stale in under one slot (~400ms)"; line 82: "the rooted slot advances roughly every 400ms". With 200ms slots the staleness class is still correct (all listed methods stay in the moving-head class) but any arithmetic built on "~400ms" (TTL floors, jitter budgets) is 2× off. **Change:** replace the literal with the measured slot duration; add a regression test pinning both eras' classifications.

### 1.5 `architecture/svm/slot_lag.go` — filter semantics
- `FilterByFinalizedSlotLag` stays as-is structurally. **Change:** none in the filter itself; it inherits the re-scaled `maxLag` from 1.2. Add the era-derived expected-lag documentation to its header comment.

### 1.6 failsafe `matchCommitment` (ae3641f5, #1181)
- Already commitment-aware: rules match on the caller's requested level as pinned on the wire. Survives Alpenglow untouched — both levels still parse and the check matches either. **Change:** none required. **Opportunity:** a per-network opt-in to normalize `confirmed` → `finalized` upstream post-activation (identical state, removes the class of "confirmed rejected after removal" failures). Only with the runtime gate.

### 1.7 Method handling — `getAgGenesisCert`
- Add to SVM method tables/allowlists. Three-state result handling: `-32601` (node not upgraded — valid, never a failover trigger for this method), `null` (TowerBFT), certificate (migrated). **Change:** wherever eRPC enumerates known SVM methods (routing, cache-class defaults, scoring), include it as a stateless read that must never be cached.

### 1.8 Tests
- Poller/slot-lag/finality tests embed the 400ms cadence and TowerBFT gap expectations. **Change:** inject the clock and the slot-duration source; run the suite under both era fixtures (400ms/32-slot finality, 200ms/1-slot). A test asserting "poll cadence follows measured slot time" and "time-flavored thresholds convert correctly in both eras" is the acceptance criterion for this whole document.

## 2. Configuration changes

- **No new required knobs (D2).** All changes default to auto-derived behavior.
- **`slotDuration`** (new optional, network scope): `auto` (default — measured per D2) or an explicit duration for private/unknown clusters. Mirrors the integrity work's per-check `expected` override pattern.
- **Duration-valued thresholds:** `maxFinalizedSlotLag` and the shred-insert threshold accept either a plain integer (slots, today's meaning, backwards-compatible) or a duration string (`"40s"`) meaning wall-clock intent converted at runtime. Documented in config reference with the era-dependence called out.
- **Operator guidance (docs, not code):**
  - SVM cache TTLs should sit below slot time on the way to 200ms; certificate-aware purges are the clean long-term answer.
  - Capacity-plan WS connections if proxying subscriptions — finality polling stops making sense at ~150ms; push fan-out moves the bottleneck from request rate to connection count.
  - `getRecentPerformanceSamples` tx counts drop ~75% post-activation (votes leave the block) — do not alarm on "activity collapse".

## 3. Post-activation checklist

1. Per-upstream `getAgGenesisCert` state verified — it is the switch that activates everything below.
2. Poll cadence following measured slot time (½ slot).
3. Duration-derived thresholds live: shred-insert lag and finalized-slot lag no longer halve their wall-clock meaning at 200ms slots.
4. `slotPinnedMethods` promotion window widened — getBlock/getTransaction cacheable ~150ms post-block; cache utilization re-measured.
5. Finalized-lag default tightened (era-appropriate; ~40s intent no longer means ~200 slots).
6. Metrics re-baselined: `getRecentPerformanceSamples` tx counts (~−75% is expected, not an outage), `getVoteAccounts` participation semantics, `LatestFinalizedGap` health metric with era expectation.
7. Read paths migrated to `finalized`; `confirmed`-removal handling on the radar for Anza's deprecation timeline.
8. Optional: `confirmed`→`finalized` upstream normalization in failsafe matching.
9. Geyser consumers (if any downstream): Yellowstone ≥ `v16.0.0-rc10+solana.4.3.0`, `(slot, bank_id)` buffering, blockhash-only merge reconciliation.
10. Dual-era test fixtures green in CI.

## 4. Explicitly deferred
- Modelling block footers / certificates in JSON-RPC paths (Geyser-only today; Skip per chain-safety invariant until they appear on RPC).
- `confirmed` removal handling — wait for Anza's deprecation timeline, then it becomes a config-validation error.
- `bank_id`-aware streaming proxy logic — only if/when eRPC proxies Geyser.
