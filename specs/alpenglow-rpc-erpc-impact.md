# Alpenglow: Impact on the Solana RPC Protocol and eRPC

> Research note — 2026-10-08
> Sources: solana.com/upgrades (Agave 4.2/4.3 + Alpenglow pages), Helius Alpenglow deep-dive, Anza v4.3 schedule, SIMD-0326/0357/0387.

## 1. What Alpenglow is (30-second version)

Alpenglow is Solana's first full consensus replacement: TowerBFT + Proof of History are replaced by **Votor** (off-chain BLS-aggregated voting, one or two rounds) and later **Rotor** (single-relay block propagation replacing Turbine). Finality drops from **~12.8s to a target of ~150ms**. The SVM, transaction format, fees, and account model are untouched — execution does not change; block production, streaming, and finality do.

Rollout status as of this writing:

| Milestone | Status |
|---|---|
| SIMD-0326 governance vote | Passed Sept 2025 (98.27%) |
| BLS pubkey registration (SIMD-0387) | Mainnet active since Jul 8, 2026 |
| Validator Admission Ticket (SIMD-0357) | Mainnet active since Jul 22, 2026 |
| Alpenglow code | Feature-complete in Agave 4.2 (switch off) |
| Feature gate `A1pengvuM6JEcyNuTnMqepBKhwHE3N6PmUrdATGawhJS` | **Active on testnet/devnet; mainnet pending (Agave 4.3, targeted Oct 2026)** |
| Rotor | Separate, later SIMD — not scheduled |

Feature detection must be runtime-based, never date-based: call `getAgGenesisCert` (see §3).

## 2. What changes at the RPC layer

### 2.1 Commitment levels collapse
`processed` / `confirmed` / `finalized` all still work on activation day. But under Votor, `confirmed` and `finalized` describe the **same state** (~150ms after the block), and `confirmed` is slated for removal in a later release. Nothing new appears between `processed` and `finalized`.

**Do not** switch to `finalized` before activation — on TowerBFT it still means a 12.8s wait.

### 2.2 Vote transactions leave the block
~75% of current on-chain transactions are votes. Under Alpenglow they are direct validator messages aggregated into ~1KB BLS certificates; only the certificate header is anchored on-chain.

- Transaction counts / TPS dashboards drop sharply — an accounting change, not a throughput regression. Re-baseline everything.
- Filtering the Vote program becomes a no-op.
- Validator participation data moves to the **block footer** certificates (`notar_reward_cert`, `skip_reward_cert`, `block_final_cert`) via Geyser's opt-in `notify_block_footer`. Yellowstone's gRPC footer does **not** forward certificates yet.

### 2.3 New RPC method: `getAgGenesisCert`
Added in Agave 4.3. Returns `null` on TowerBFT, or the Alpenglow genesis certificate (first slot/block under new consensus + aggregate BLS signature + validator bitmap) after migration.

- Older nodes answer `-32601 Method not found` — treat that as "node not upgraded", distinct from "still on TowerBFT".
- CLI equivalent: `solana alpenglow-genesis-info`. `@solana/kit` exposes it from 8.3.0; other SDKs need a raw request.

### 2.4 Geyser/gRPC: `bank_id` and multiple banks per slot
A slot can carry more than one candidate bank (equivocation today; fast leader handoff in 4.4, expected <1% of slots). Agave 4.3 tags every Geyser/gRPC event with `bank_id`:

- Buffer per `(slot, bank_id)`, seal against block meta counts, keep the bank that reaches `Confirmed`, drop the rest.
- **JSON-RPC and WebSocket are unchanged** — `bank_id` exists only on Geyser/gRPC.
- `bank_id` is a **node-local counter**: meaningless across connections. Merging multiple providers must reconcile on blockhash only. Merging on `(slot, bank_id)` across streams fails silently.
- Legacy Geyser callbacks (`update_account`, `notify_transaction`, `notify_entry`, `notify_block_metadata`) are deprecated in 4.3, removal in next major. Replacements carry `bank_id`; two new UpdateParent callbacks name banks to discard.

### 2.5 Time changes
- **Ticks leave the ledger.** PoH no longer paces blocks, so the 64 tick entries per slot disappear. Anything inferring intra-slot progress from ticks (e.g. leader-routing timing) needs a local slot clock instead.
- **`Clock.unix_timestamp` gets a new source.** No longer the stake-weighted median of vote timestamps; the leader sets it, bounded by `[parent_ts, parent_ts + 2 × elapsed_slot_duration]`. Monotonic, coarse, and leader-trusted within that envelope — skew window grows with skipped slots (e.g. parent+400ms on 200ms slots, parent+2s after 4 skips). No code change to read it, but re-check any logic tuned to the old drift behavior.

### 2.6 Operational shifts for RPC providers (Helius summary)
- **Simplified commitment logic**: two-step "spinner until finalized" UX collapses to one certificate check.
- **Reduced ledger size**: ~75% less ledger growth; smaller snapshots/archives.
- **WebSocket fan-out becomes the bottleneck**: polling for finality stops making sense at 150ms; finality via push channels = hundreds of thousands of concurrent sockets instead of millions of tiny HTTP polls.
- **Cache freshness**: any cache holding account data longer than ~250ms can serve stale state. Edge caches / L7 proxies need sub-second TTLs or certificate-aware purges.

## 3. Impact on eRPC specifically

Verified against `andreclaro/erpc` **main @ `14c268f0`** (2026-10-08).

### 3.1 On main today — finality-classified caching, state poller, failsafe matching

**`architecture/svm/finality.go` — cache policy classification.**
- `neverCacheMethods` justifies realtime treatment as "go stale in under one slot (~400ms)". Slot time halves to 200ms (SIMD-0525), and post-activation confirmed==finalized at ~150ms — the classification stays correct (conservative), but every numeric TTL/staleness assumption built on ~400ms slots must be re-scaled.
- `slotPinnedMethods` (getBlock/getTransaction/…) promote to immutable-cacheable at `commitment == finalized`. Today that promotion waits ~12.8s after block production; post-activation it arrives ~150ms later. Recent-block reads become cacheable almost immediately — a real cache-hit win — but only *after* `getAgGenesisCert` says Alpenglow is live on that upstream (pre-activation, finalized still means 12.8s).
- `alwaysFinalizedMethods` (`getInflationReward`, `getBlockTime`): remain stable-once-exists and cacheable. Caveat: `getBlockTime`'s value gets a new producer (leader-set, bounded by 2× elapsed slot time) — stability unaffected, trust model changes; re-check any consumer tuned to stake-median drift.

**`architecture/svm/svm_state_poller.go` — dual slot tracking.**
- Polls `getSlot(processed)` and `getSlot(finalized)` per upstream; the finalized tip doubles as the getBlock guard bound at finalized commitment.
- Post-activation the two tracks converge (finalized lag drops from ~12.8s to ~150ms) — the poller keeps working, but cadence/tolerance tuning assumes TowerBFT: the traffic-gate windows and large-rollback tolerances should be revisited. Slot rate doubling (400→200ms) also means a per-second rollback allowance covers half the slot depth it used to.

**Failsafe commitment matching (merged as #1181).**
- Requests are matched on the caller's commitment. Post-activation, `confirmed` and `finalized` requests become equivalent for matching/caching — a simplification opportunity, not a break. Gate it on per-upstream `getAgGenesisCert`, not a date.

**Metrics/diagnostics on main.**
- `getRecentPerformanceSamples` (in neverCache): performance sample tx counts re-baseline — votes gone, counts drop ~75% without any real activity change.
- `getVoteAccounts` keeps working (vote *accounts* still exist on-chain) but says less about live participation over time — that evidence moves to footer certificates.

### 3.2 Request routing and method support
- **`getAgGenesisCert`**: add to allowed-method lists for SVM networks. During the 4.2→4.3 rollout, upstreams will disagree: `null` (upgraded, TowerBFT), certificate (upgraded, migrated), or `-32601` (not upgraded). eRPC should treat these as valid, distinct outcomes — do **not** fail over on `-32601` for this method; it is node-version signal, not upstream failure.
- **Mixed-version upstream pools**: expect heterogeneous behavior around commitment handling and new methods for several weeks. eRPC's upstream scoring/misbehavior tracking must not penalize nodes for version skew on Alpenglow-adjacent methods.
- **Cache layer**: response caching keyed on commitment can simplify post-activation (`confirmed` ≈ `finalized`), but TTLs must shrink below slot time (heading to 200ms) or cached reads go stale relative to finality. Certificate-aware purge hooks are the clean long-term answer; short-term, drop cache TTLs for SVM networks.

### 3.3 Streaming / WebSocket proxying
If eRPC proxies WS subscriptions: finality polling gives way to long-lived push streams. Plan for connection-count scaling (hundreds of thousands of sockets per cluster), and note `bank_id` does **not** appear in JSON-RPC/WS — WS consumers see no schema change, only faster `finalized` notifications.

### 3.4 What does NOT change
- Transaction execution, fee mechanics, account model, program semantics.
- `processed` commitment semantics.
- JSON-RPC and WebSocket schemas (except the additive `getAgGenesisCert`).
- Genesis hash, blockhash format, ed25519 transaction signatures.
- Geyser is the only surface with breaking schema changes (callbacks + `bank_id`).

## 4. Action checklist for eRPC

**Before mainnet activation (now → Agave 4.3 window):**
1. Add `getAgGenesisCert` to SVM method allowlists; handle `-32601` (node not upgraded) vs `null` (TowerBFT) vs certificate (migrated) as three distinct valid outcomes — never fail over on `-32601` for this method.
2. **(main)** Re-scale slot-time assumptions: `finality.go` staleness math ("under one slot ~400ms") and state-poller cadence/rollback tolerances — 400→200ms slots means a per-second rollback allowance covers half the slot depth it used to.
3. **(main)** Prepare `slotPinnedMethods` for near-instant promotion: post-activation, getBlock/getTransaction at `finalized` become immutable ~150ms after production instead of ~12.8s — widen cache utilization then, gated on per-upstream `getAgGenesisCert`, never on a date.
4. **(main)** Re-baseline anything derived from `getRecentPerformanceSamples` tx counts — votes gone is an accounting change, not an activity collapse.
5. **(main/ops)** Re-check the `getBlockTime` trust model in consumers (`alwaysFinalizedMethods`): still stable-once-exists and cacheable, but leader-set with a drift envelope that grows with skipped slots.
6. Shrink SVM cache TTLs below slot time (heading to 200ms); certificate-aware purges are the clean long-term answer.
7. Keep chain-safety invariant everywhere: Alpenglow-only fields (footer, certificates, `bank_id`) → Skipped, never Reject, until modelled.

**During rollout:**
8. Treat upstream version skew as normal — no misbehavior scoring penalties for Alpenglow-adjacent method differences (`getAgGenesisCert` presence/absence, commitment quirks).
9. Monitor `getAgGenesisCert` per upstream — it is the authoritative per-node migration signal; drive all behavior switches off it.
10. Expect WS fan-out growth if proxying subscriptions (finality polling → push); capacity-plan connection counts.

**Post-activation:**
11. Migrate read paths to `finalized` — now ~150ms and strictly stronger than `confirmed`; plan for `confirmed`'s later removal.
12. If Geyser consumers exist downstream: Yellowstone ≥ `v16.0.0-rc10+solana.4.3.0`, buffer per `(slot, bank_id)`, reconcile multi-provider merges on blockhash only.

## 5. Sources
- Solana upgrades hub: https://solana.com/upgrades (Agave 4.2 shipped Aug 2026; 4.3 planned Oct 2026)
- Alpenglow official page + migration checklists: https://solana.com/upgrades/alpenglow
- BLS/VAT: https://solana.com/upgrades/bls-pubkey-vat
- Helius deep-dive (RPC provider impact): https://www.helius.dev/blog/alpenglow
- Chainstack status/technical guides (SIMD-0326, Votor/TowerBFT)
- Solana Compass: v4.3 general adoption / feature-gate rollout reporting (Sept 2026)
- SIMD-0326 (Alpenglow), SIMD-0357 (VAT), SIMD-0387 (BLS pubkeys), SIMD-0525 (200ms slots)
