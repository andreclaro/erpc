# 06 — Implementation Plan

## 1. Phases (each phase = a PR series, one logical change per PR, conventional commits)

| Phase | Deliverable | Exit criteria | Est. |
|---|---|---|---|
| **0 — Baseline** | Enable existing `integrity` module in observe mode on canary networks; fixture corpus (real blocks/receipts/proofs per chain); dashboards for existing metrics | Zero false-positive cordons on canary for 1 week | ~1 wk |
| **1 — Helios anchor** | `HeaderOracle` interface (`01` §3); Helios sidecar deployment per chain; ChainView anchor swap (`01` §6); checkpoint config + cross-validation | Fabricated-fork red-team test cordons the lying upstream with exported evidence; finality labels anchor-derived | 2–4 wks |
| **2 — ProofVerifier** | Full MPT verification (`02` §2); state-read request-path flow (`02` §3); `eth_getProof` passthrough verification; shallow-probe replacement | Wrong-balance and stale-replay red-team tests fail closed/open per mode; proof-support latch works across the provider pool | 3–5 wks |
| **3 — Native anchor** | `anchor/` package: SyncVerifier + CheckpointManager + BeaconSourcePool + AnchorStore (`01` §4-5); consensus-spec-tests conformance; Helios retired (kept as fallback option) | 100% spec-vector pass; 2-week soak on mainnet with zero anchor stalls; fork-boundary fixture passes | 6–10 wks |
| **4 — StatelessExecutor** | prestateTracer capture → proof set → local EVM re-execution → compare (`02` §6); capability detection; strict-mode gating | Fake-`eth_call` red-team test detected; abort paths (unproven read, tracer unsupported) degrade cleanly | 4 wks research + 8–12 wks |
| **5 — L2 anchors** | `l1Committed` adapter framework + OP Stack adapter first, then Arbitrum / zk-family per demand (`04`) | L2 block verified against L1-anchored output root on testnet + mainnet canary | per family, ongoing |
| **6 — Signals (optional)** | dRPC signature adapter, scoring integration (`05`) | Async-only, zero added p99 latency; signed-but-wrong upstream cordoned | 1–2 wks |

Suggested order is dependency order: 0→1→2→3, with 4/5/6 parallelizable after 2.

## 2. Per-phase repo-rule checklist

- [ ] `make fmt` clean; `make test-fast` green; `make test` (race) green before merge
- [ ] Docs ride along in the same PR: page under `docs/pages/` per feature, `<AISection>` with exact config schema + defaults cited to `common/defaults.go`, numbered edge cases, exact metric names; `_meta.js` entry (llms.txt is generated — never hand-edit)
- [ ] Any `erpc.yaml` schema change ships with a version marker
- [ ] No new enums/method-lists/chain-switches in core packages without the razor review test answered in the PR description (*"what unseen-but-plausible input does this silently mishandle?"*)
- [ ] Zerolog only; errors wrapped with `%w`; `common.HasErrorCode` for error-type checks
- [ ] Goroutine/shared-state audit for anchor + verifier background loops (grab-under-lock-use-outside; atomics for hot counters)

## 3. Testing strategy

1. **Conformance:** official `consensus-spec-tests` light-client fixtures for the SyncVerifier (valid + every invalid mutation: bad signature, <2/3 participation, wrong period, bad branches).
2. **Fixtures:** recorded mainnet data — ≥2 full sync-committee periods, a hard-fork boundary, deep-trie `eth_getProof`s (existing/non-existent accounts, multi-slot), reorg windows.
3. **Adversarial gock mocks** (set up before any component init; no `t.Parallel()`): fabricated fork, wrong-value+valid-proof-for-other-account, block-N proof replayed at N+1, omitted logs, forged sender, fake `eth_call`, signed-but-anchor-conflicting responses (P6).
4. **Soak/canary:** observe mode on production-shadow traffic per phase; false-positive budget ~0 before advancing modes.
5. **Fuzzing:** RLP/proof parsers, SSZ decoders, slot/period arithmetic.
6. **Performance:** BLS verify cost (~ms, background) — assert anchor never on serving path; proof verification p99 vs cache-hit ratio; StatelessExecutor timeout behavior.

## 4. Rollout

Per network, per method class: `observe → balanced → strict`. Advance only after (a) 1+ week clean canary, (b) anchor staleness incidents at zero, (c) proof-support coverage across the pool measured (unsupported-latch rates per upstream in metrics). `strict` initially for finalized V-inclusion/V-state only; V-execution strict waits for Phase 4. Client communication: `X-ERPC-Verification` header documented as observability, and `strict`-mode typed errors documented for integrators.

## 5. Risks & open questions

| Risk | Mitigation |
|---|---|
| Helios unaudited (Phase 1) | Checkpoint cross-validation; conflict = fail-closed; Phase 3 replaces it |
| `eth_getProof` / `eth_getBlockReceipts` support variance across providers | Capability latch (#1133 pattern) + pool coverage metrics before `strict` |
| Deep-history state proofs need archive-capable upstreams | Detect state-missing errors, route to archive-capable upstreams, else degrade per policy |
| L2 contract layouts change (upgrades) | Layouts as chain-registry config data; verification fails closed to `quorum` on unknown layout |
| Beacon-endpoint availability | ≥2 sources; anchor serves persisted state; staleness alerts |
| Verification-cost DoS | Caps + budgets (`02` §7); verify-at-ingest amortization |
| Anchor bugs = systemic false cordons | Observe-first rollout; spec-vector conformance gate; evidence bundles human-reviewable before permanent bans |

## 6. Acceptance: red-team checklist (maps to invariants, `00` §5)

- [ ] **INV-1** fabricated self-consistent fork → rejected, upstream cordoned with evidence
- [ ] **INV-2** update with <2/3 participation / bad BLS → anchor rejects, metric fires
- [ ] **INV-3** upstream block conflicting with anchored header → never enters ChainView
- [ ] **INV-4** poisoned-cache attempt (unverified entry re-read as verified) → impossible by construction (unit test)
- [ ] **INV-5** strict mode + anchor stall → typed fail-closed error, no provisional leak
- [ ] **INV-6** cold start with stale checkpoint → bootstrap refused, alert fires
- [ ] Valid `eth_call` fabrication (post-P4) → mismatch → cordon
- [ ] Signed-but-wrong dRPC response (post-P6) → anchor wins, conflict metric fires
