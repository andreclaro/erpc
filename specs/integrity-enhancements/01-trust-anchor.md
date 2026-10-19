# 01 — Trust Anchor (P1): Consensus-Attested Headers

## 1. Purpose

Replace the chain follower's root of trust — today: RPC-supplied blocks linked by parent hash (`architecture/evm/integrity_chainfollower.go:165-182`) — with headers **attested by the chain's consensus**. Once the anchor supplies canonical headers, every existing integrity check (hash/root recomputation, bloom, sender recovery) and every new proof check (`02`) becomes trustless: an upstream serving a fabricated chain conflicts with the anchor, which is *machine-checkable proof of lying*.

Without this pillar, all other checks are self-consistency only. This is the load-bearing change.

## 2. Bounded interface (design razor)

Chains are an open set; anchor mechanisms are not enumerated per chain in core code. Any chain resolves into exactly one **anchor kind** — a small closed set:

| Kind | Root of trust | Used for |
|---|---|---|
| `syncCommittee` | Altair-style sync committee BLS signatures (≥2/3 of 512 validators) | Ethereum mainnet, Gnosis, other beacon-spec chains |
| `l1Committed` | Commitments (output roots / assertions / finalized state roots) stored in an L1 contract, read via `eth_getProof` against our own L1 anchor | L2s (OP Stack, Arbitrum, zk rollups) — see `04` |
| `quorum` | Cross-upstream agreement (existing `consensus` failsafe) | **Default fallthrough** for any chain with no better mechanism (BSC, Solana, unknown chains) |

Unknown/unconfigured chain → `quorum`. That is the weakest safe tier and must work with zero per-chain code. `syncCommittee` and `l1Committed` adapters plug in at the edge via config + capability detection, never via chain-ID switches in core packages.

## 3. Delivery strategy: two phases, one interface

Both phases implement the same internal interface, so Phase B is a drop-in replacement:

```go
// HeaderOracle is the anchor's output. ChainView consumes only this.
type HeaderOracle interface {
    // Latest anchored (finalized) execution header.
    LatestFinalized(ctx context.Context) (*types.Header, error)
    // Anchored header by number; returns finality status.
    HeaderByNumber(ctx context.Context, n uint64) (hdr *types.Header, finalized bool, err error)
    // Optimistic (signature-attested but unfinalized) head, for the provisional tier.
    OptimisticHead(ctx context.Context) (*types.Header, error)
}
```

### Phase A — Helios sidecar (fast path)

Run [Helios](https://github.com/a16z/helios) (Rust light client; Ethereum + OP Stack + Linea) as a per-chain sidecar; eRPC's `HeaderOracle` reads headers from its local RPC.

- Pros: weeks to integrate; production-hardened sync protocol; multi-chain.
- Cons: formally unaudited ("as is"); extra deploy unit per chain; Rust black box.
- Mitigations: pin + cross-validate its checkpoint (§5); on startup, sanity-compare the Helios finalized header against 2 independent beacon sources; treat divergence as fatal for the anchor (fail-closed); plan Phase B regardless.

### Phase B — Native Go sync-committee verifier (target)

New package `architecture/evm/integrity/anchor/` (no dependency on Helios):

```
anchor/
  checkpoint.go   — CheckpointManager: bootstrap trust, cross-validation, rotation, age policy
  sources.go      — BeaconSourcePool: multi-endpoint fetch of standardized light-client objects
  verifier.go     — SyncVerifier: consensus-specs validation of bootstrap/updates
  store.go        — AnchorStore: persisted committees + finalized/optimistic headers
  oracle.go       — HeaderOracle implementation consumed by ChainView
```

## 4. SyncVerifier — protocol requirements (Altair light client sync)

Reference: `consensus-specs/specs/altair/light-client/sync-protocol.md`, `light-client/p2p-interface.md`. Constants: `SYNC_COMMITTEE_SIZE = 512`, `EPOCHS_PER_SYNC_COMMITTEE_PERIOD = 256` (~27.3 h).

**Data sources** (standardized `beacon-APIs`, self-verifying — source need not be trusted, only available):

- `GET /eth/v1/beacon/light_client/bootstrap?block_root={root}`
- `GET /eth/v1/beacon/light_client/updates?start_period={p}&count={n}`
- `GET /eth/v1/beacon/light_client/finality_update`
- `GET /eth/v1/beacon/light_client/optimistic_update`

**Bootstrap (cold start only):**

1. Input: trusted checkpoint = beacon block root (§5).
2. Fetch `LightClientBootstrap(root)` → header + current sync committee + committee Merkle branch.
3. Verify committee branch against `header.stateRoot`; verify `hash_tree_root(header)` derives the checkpoint root. Reject otherwise.

**Update processing (steady state), per `process_light_client_update`:**

1. Validate slot ordering (signature slot > attested slot ≥ finalized slot) and period consistency.
2. Require participation: finality-claiming updates need a ≥2/3 supermajority of the 512-bit sync-aggregate bitfield.
3. Verify the **aggregate BLS12-381 signature** over the attested-header signing root (`DOMAIN_SYNC_COMMITTEE`, fork version per chain fork schedule, `genesis_validators_root`) against the active committee's aggregate pubkey.
4. If the update carries the next sync committee: verify its branch against attested `stateRoot`, then rotate committees at period boundary.
5. Verify the finalized-header branch; then verify the **execution payload branch** (beacon body root → `ExecutionPayloadHeader`) so the oracle emits *execution* headers (what EVM integrity checks need).
6. Optimistic updates: same signature validation, no finality claim → feed `OptimisticHead` only. **Never** promotes to verified.

**Libraries:** BLS via `github.com/supranational/blst` (Go bindings, consensus-specs standard) or herumi; SSZ/hash-tree-root via existing Go consensus libs (e.g. prysm's packages as reference or `fastssz`); execution headers via go-ethereum `core/types`.

## 5. CheckpointManager — weak subjectivity, handled honestly

The checkpoint is the *only* out-of-band trust input. Rules:

1. **Acquisition:** config-pinned beacon block root, cross-validated before first use: fetch the same root from ≥2 independent sources (operator-provided list, e.g. own beacon node + a public explorer API) and require agreement on root **and** that it is a finalized checkpoint. Disagreement → refuse to start anchor (fail-closed, alarm).
2. **Age policy:** checkpoint must be well inside the weak-subjectivity period (mainnet ≈ 2 weeks). Default `maxCheckpointAge: 168h` (7 d). Stale checkpoint → anchor cold-start blocked, hard alert (long-range-attack surface). Configurable per chain — it is protocol data, not a behavioral knob.
3. **Rotation:** after bootstrap, the anchor is self-sustaining (stored committees + finalized headers need no further trust). Persist anchor state (`AnchorStore`) and re-anchor from disk on restart; only a *cold* (no-state) start consumes the checkpoint. Optionally emit a fresh operator-visible checkpoint from the current verified finalized header to simplify future cold starts.
4. **No silent fallback:** if the anchor cannot initialize or liveness-fails (§7), eRPC never silently re-anchors to RPC-supplied headers; networks degrade per configured policy with explicit labeling (`03` §3).

## 6. ChainView integration

- ChainView (`integrity_chainview.go`, `integrity_chainfollower.go`) gains the `HeaderOracle` as a **privileged anchor source**. Semantics:
  - Follower-built chain must *descend from* anchored headers. Blocks consistent with the anchor keep flowing through existing recompute checks at ingest.
  - **Conflict = evidence:** if an upstream's block/header contradicts an anchored header (wrong hash at anchored height, or a branch that cannot descend from the anchored finalized head), record an evidence bundle `{request, upstream response, anchored header, fetched proof context}` → hard cordon (skip the consensus dispute token bucket; use existing sit-out + `misbehaviorsDestination` export).
- Finality labels (`finalized`/`safe`) derive **only** from the anchor, never from upstream block tags. This closes finality-mislabeling attacks.
- Reorgs: unfinalized suffix remains follower-tracked and reorg-aware as today; the anchored finalized prefix is immutable by definition.

## 7. Liveness & failure modes

| Failure | Behavior |
|---|---|
| Beacon endpoints down/malicious-withholding | Pool of ≥2 sources, round-robin + backoff; anchor keeps serving persisted state; `strict` mode fails closed only after `anchorMaxStaleness` (default: 2 slots past expected update cadence) |
| Hard fork (fork version change) | Fork schedule table (chain config data — protocol facts, not heuristics) with pre-staged future epochs; unknown fork epoch → anchor pauses + alert, never guesses |
| Clock skew | Slot-time validation tolerates ±`clockTolerance` (default 30 s); NTP assumed (threat model §3) |
| Anchor store corruption | Treated as cold start → checkpoint path with age policy enforced |
| Long sync after downtime | Backfill `updates?start_period=…&count=…` sequentially, verifying each step |

## 8. Config schema (new; ships with `erpc.yaml` schema version marker)

```yaml
networks:
  - evm:
      chainId: 1
    integrity:
      enabled: true
      anchor:                      # NEW section
        kind: syncCommittee        # syncCommittee | l1Committed | quorum (default when unset)
        syncCommittee:
          checkpoint: "0x…"        # trusted beacon block root (cold start only)
          maxCheckpointAge: 168h
          beaconEndpoints:         # ≥2 recommended; liveness only, not trust
            - "https://beacon-a.example"
            - "https://beacon-b.example"
          helios:                  # Phase A alternative to the native verifier
            enabled: true
            endpoint: "http://127.0.0.1:8545"
        anchorMaxStaleness: 5m
```

## 9. Metrics

- `erpc_integrity_anchor_updates_total{network, kind}` — processed updates
- `erpc_integrity_anchor_finalized_height{network}` — gauge
- `erpc_integrity_anchor_staleness_seconds{network}` — gauge (alert > `anchorMaxStaleness`)
- `erpc_integrity_anchor_checkpoint_age_seconds{network}` — gauge (alert > policy)
- `erpc_integrity_anchor_verification_failures_total{network, reason}` — bad signature/branch/participation (potential attack on the beacon pool → page)
- `erpc_integrity_anchor_conflicts_total{network, upstream}` — upstream vs anchor conflicts (= proven lies; pairs with cordon)

## 10. Testing

- **Spec vectors:** official `consensus-spec-tests` light-client update fixtures (valid + every invalid mutation).
- **Mainnet fixtures:** recorded real `bootstrap`/`updates` covering ≥2 committee periods and a hard-fork boundary.
- **Adversarial:** fabricated fork from mock upstream vs anchored ChainView (must cordon + export evidence); stale-checkpoint cold start (must refuse); forged update with valid committee but <2/3 participation (must reject).
- Repo conventions: `util.ConfigureTestLogger()` in test files; gock mocks before component init; no `t.Parallel()` with gock; zerolog; `%w` error wrapping.
