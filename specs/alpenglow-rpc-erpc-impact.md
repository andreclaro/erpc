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

## 3. Impact on eRPC — summary

Full engineering detail — per-file source changes, configuration changes, decisions, phased checklist — lives in **[erpc-alpenglow-required-change.md](erpc-alpenglow-required-change.md)** (same verification: main @ `14c268f0`). Headlines:

- **State poller** — keep dual `getSlot(processed)`/`getSlot(finalized)` tracks; make poll cadence and the time-flavored thresholds (shred-insert lag, finalized-slot lag) follow *measured slot duration*, since 400→200ms slots silently halves the wall-clock meaning of every slots-expressed constant.
- **finality.go cache classes** — classifications stay correct; re-scale the ~400ms staleness math; `slotPinnedMethods` promotion arrives ~150ms post-block instead of ~12.8s — widen cache use only when per-upstream `getAgGenesisCert` says Alpenglow is live, never on a date.
- **Failsafe `matchCommitment` (#1181)** — unchanged; post-activation `confirmed`≈`finalized` is a simplification opportunity, runtime-gated.
- **`getAgGenesisCert`** — allowlist with three-state handling (`-32601` / `null` / certificate); never fail over on `-32601` for this method.
- **Metrics** — `getRecentPerformanceSamples` tx counts re-baseline (votes leave the block, ~−75%); `getVoteAccounts` says less about live participation over time.
- **Ops** — SVM cache TTLs below slot time; WS fan-out capacity-planning for push finality; no misbehavior scoring penalties for upstream version skew.
- **What does NOT change** — tx execution/fees/account model, `processed` commitment, JSON-RPC/WS schemas, genesis hash, signatures. Geyser is the only breaking-schema surface.

## 4. Action checklist

Moved to [erpc-alpenglow-required-change.md](erpc-alpenglow-required-change.md) §3 (post-activation checklist) to keep this doc protocol-focused.

## 5. Sources
- Solana upgrades hub: https://solana.com/upgrades (Agave 4.2 shipped Aug 2026; 4.3 planned Oct 2026)
- Alpenglow official page + migration checklists: https://solana.com/upgrades/alpenglow
- BLS/VAT: https://solana.com/upgrades/bls-pubkey-vat
- Helius deep-dive (RPC provider impact): https://www.helius.dev/blog/alpenglow
- Chainstack status/technical guides (SIMD-0326, Votor/TowerBFT)
- Solana Compass: v4.3 general adoption / feature-gate rollout reporting (Sept 2026)
- SIMD-0326 (Alpenglow), SIMD-0357 (VAT), SIMD-0387 (BLS pubkeys), SIMD-0525 (200ms slots)
