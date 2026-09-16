# Custom Consensus Policies Engine — Implementation Details

**Status**: Implemented (v1 MVP + v1.1 empty-outside-retention waiver)
**Companion**: [feature.md](./feature.md) (design contract) · [plan.md](./plan.md) (phased delivery)
**Synced to**: `crcl-main/erpc` PR
[#136](https://github.com/crcl-main/erpc/pull/136) as of `70e16699`
**Shipped operator docs**:
`docs/pages/config/failsafe/consensus-policies.mdx`

This file holds the wire-level and executor-level detail that would bloat
`feature.md`: exact waiver semantics, block-proof order, the wait-cap seal, the
hedge consensus-slot vote, the PreferNonEmpty tie rule, the selector sandbox,
metric names/labels, the header contract, and the failure modes. Every claim is
grounded in code with a file reference. When code and this file disagree, the
code wins — fix this file.

---

## 1. Waiver threshold — `≥ minAgreement`, not unanimous

The single biggest correction versus the pre-implementation spec: a waiver
releases a quota on **at least `minAgreement` distinct tag-matching
abstentions**, not on a unanimous "every matching participant abstained". The
`minAgreement` count is the same floor the quota enforces, so releasing it is
symmetric with meeting it.

### 1.1 MissingData waiver (`missingDataWaivedQuotas`, `consensus/executor.go`)

An entry's `minAgreement` quota is waived for the round when **all** of:

- the entry opts in (`waiveAgreementOnMissingData`) and carries a real quota
  (`minAgreement > 0`);
- **at least `minAgreement`** distinct tag-matching participants returned
  `ErrEndpointMissingData` (normalized code `ErrCodeEndpointMissingData`);
- **no** matching participant returned data — a matching `r.Err == nil` sets
  `dataMatch` and holds the quota. A real value vote is disagreement, not an
  abstention, and waiving over it would mask it.

Sibling **transport / infra errors do not block** the count and do not count as
abstentions — they are simply ignored. The scan is over **all groups** (the
whole round, deduped by upstream ID), not just the winning group.

### 1.2 Empty-outside-retention waiver (v1.1, `emptyOutsideRetentionWaivedQuotas`)

Same shape, keyed on empty results instead of MissingData errors, plus a block
proof:

- opts in (`waiveAgreementOnEmptyOutsideRetention`), `minAgreement > 0`,
  `retentionBlocks > 0`;
- **at least `minAgreement`** distinct tag-matching participants returned an
  **empty** result (`r.Err == nil && CachedResponseType == ResponseTypeEmpty`);
- **no** matching non-empty result (a matching `ResponseTypeNonEmpty` sets
  `nonEmptyMatch` and holds the quota — same "matching data holds" rule);
- the winner's own block is **proven outside retention** (§2).

Both waivers are **round-complete**: each returns no waivers while
`analysis.hasRemaining()` is true, so a slower tagged upstream can still turn an
abstention into a data vote. `waivedQuotas` merges the two; when an entry
qualifies for both, `missing_data` is reported (the stronger explicit signal).

### 1.3 Never-decisive-masking

`recordCompositionWaivers` only logs/counts an entry whose quota **would have
failed strictly** — i.e. releasing it is what made the winner decisive. A waiver
that wasn't needed is silent. So a waiver can never flip a genuine
disagreement; it only rescues a round that abstentions alone were blocking.

---

## 2. Empty-waiver block proof — order and trust model

Proof is evaluated over the counted empties only, **preferring the first source
that succeeds** (`emptyOutsideRetentionWaivedQuotas` → `availabilityBound…` /
`chainHead…`):

1. **Availability lower bound (preferred).**
   `availabilityBoundProvesOutsideRetention`: every counted empty upstream
   exposes a **finite** `EvmBlockAvailabilityBounds` lower bound (≠
   `math.MinInt64`, typically `poller.latest − latestBlockMinus`) **and**
   `winnerBlock < lower` for each. This needs **no** network tip — the internals
   themselves declare the retained window.
2. **Chain-head depth (fallback).** `chainHeadProvesOutsideRetention`:
   `head − winnerBlock > retentionBlocks`.

The `head` used by the fallback (`executor.go` `chainHead`) is trust-gated:

- Use the **majority network served tip** (`EvmHighestLatestBlockNumber`) only
  when `evm.servedTip.enabledFor` includes `"latest"`
  (`majorityServedLatestEnabled`). That value is majority-corroborated across
  eligible upstreams.
- Otherwise fall back to `participantLatestBlock`: the **MIN** of the poller
  `EvmEffectiveLatestBlock` across **≥ 2 distinct** EVM participants with a
  positive latest. Fewer than two → `0` (fail closed).
- **Default max-mode served tip is not trusted** — a single inflator could raise
  it and make a recent winner look pruned, reopening the fabrication hole.

The winner's block comes from `winnerBlockEvidence`: the first field in
`blockEvidenceFields[method]` that resolves to a number on the winning group's
result. A method **not listed** → nil block → no proof → dispute.

**Anti-fabrication result:** a `null` on a *recent* block (inside retention /
bounds) fails the proof and holds the quota. Correlated externals cannot outvote
an internal on data the internal should still serve; the waiver only tolerates
emptiness the retention model *predicts*.

### 2.1 `promoteAbstentionWinner` — synthesized empty-vs-archive ties

The default mixed-node shape produces a tie the waiver machinery would otherwise
never see: a lone non-empty group (e.g. 2 agreeing archives) ties an
equally-sized empty group (2 pruned internals) at `agreementThreshold`. The
rules engine synthesizes an `ErrConsensusDispute` for that tie, and a
synthesized error has **no backing group** (`groupOf(winner) == nil`), so
`enforceWinnerComposition`'s waiver path never runs on it.

`promoteAbstentionWinner` (`consensus/executor.go`) rescues exactly this case
from the synthesized-error branch of `enforceWinnerComposition`, under **every**
guard:

- opt-in present (`anyEmptyOutsideRetentionOptIn`) — waiver-free configs are
  completely unaffected;
- **exactly one** non-empty group meets `agreementThreshold` (two disagreeing
  non-empty groups is a real dispute — keep it);
- the same `waivedQuotas` fires for that candidate (reuses the round-complete +
  block-proof + `minAgreement`-empty guards, so mid-round or without proof it
  returns nil and the dispute stands);
- releasing the waived quotas makes the candidate satisfy composition.

On success it records the waivers and returns the candidate's largest result.

---

## 3. Wait-cap seal — how waivers evaluate with unanswered slots

A composition dispute is **provisional** while responses can still arrive:
`shouldShortCircuit` returns `("", false)` for `ErrConsensusCompositionDispute`
while `analysis.hasRemaining()`, so the round does **not** short-circuit and
keeps waiting. That is what lets a late data vote or a late abstention change the
verdict — but it also means the deferred waiver never fires on its own.

The wait-cap closes the loop (`consensus/executor.go` collection loop +
`analysis.go`):

- `maxWaitOnResult` / `maxWaitOnEmpty` fire → `cancelRemaining()` + per-attempt
  cancels stop the in-flight slots (skipped under `fireAndForget`).
- After the loop, if the round didn't short-circuit, the analysis is rebuilt;
  when `waitCapped && !fireAndForget`, `analysis.sealCollection()` sets
  `collectionSealed = true`, then `determineWinner` re-runs.
- `hasRemaining()` returns **false** once sealed even if `collectedResponses <
  maxParticipants` — cancelled slots cannot return data — so the MissingData /
  empty-outside-retention waivers finally evaluate on a round-complete bag.

`fireAndForget` deliberately does **not** cancel or seal: its unanswered slots
are still running, so sealing would let a waiver fire on a partial round. It
leaves the bag unsealed (fail closed). (Guard test:
`TestWaitCap_FireAndForgetDoesNotSealWaivers`.)

This is what makes the `maxParticipants: 4` + one dead internal case work: three
answers, wait-cap cancels the fourth, seal, waiver fires — instead of the round
disputing forever waiting for a participant that will never answer.

### 3.1 Mid-round deferral is debug-only

`logEmptyWaiverUnproven` emits at **Debug** with reason `round_incomplete` while
`hasRemaining()` (it fires once per collected response — Info/metrics would
drown real events). Only genuine terminal no-proof states log at **Info** and
increment `consensus_composition_waiver_unproven_total`:

- `no_winner_block` — no extractable winner block (`blockEvidenceFields`
  missing/unmatched for the method);
- `no_avail_bound` — matching empties lack finite availability lower bounds and
  the head path is unavailable;
- `no_head` — no majority tip and fewer than two participant pollers.

---

## 4. Hedge consensus-slot empty-keep

Cross-cutting fix without which the empty-waiver never sees internal empties.
In `runHedge` (`erpc/network_executor.go`), the `keep` closure normally
**rejects** an emptyish `{"result": null}` from a hedge leg for lookup methods
(`eth_getBlockByNumber` / `eth_getTransactionByHash` /
`eth_getTransactionReceipt`) so the hedge keeps racing for a non-empty sibling —
unless the method is in `emptyResultAccept`.

Consensus slots are the exception. When `consensusSlot == true`, an emptyish
result is **kept** (`kept = true; return true`): `tryOneUpstream` already picked
one upstream, and that empty **is the slot's vote** — the empty-outside-retention
waiver keys on it. Racing another `NextUpstream()` would replace the vote with a
different node or an `n/a ErrUpstreamsExhausted`, so the tagged upstream would
never appear in `analysis.groups` and the waiver could never fire.

This regressed briefly in `6ac0f798` and was restored in `70e16699` (branch
HEAD).

---

## 5. PreferNonEmpty tie rule (map-order flake fix)

The `accept-most-common + prefer-non-empty` rule (`consensus/rules.go`) chooses a
non-empty group even when an empty / consensus-error group meets threshold — but
the gate is on **counts**, not `getBestByCount()`:

- compute `bestEmptyOrErrAbove` (best empty/error group count that is
  `≥ agreementThreshold`) and `bestNonEmptyCount`;
- fire only when `bestEmptyOrErrAbove > 0 && bestNonEmptyCount > 0 &&
  bestEmptyOrErrAbove >= bestNonEmptyCount`.

Gating on `getBestByCount()` flaked because Go map iteration can return the
non-empty group as "best" on equal counts, skipping the rule and letting the
generic threshold winner randomly pick empty. The count gate also **stops firing
once a non-empty group strictly leads** — otherwise it would pick that group by
count and override `PreferLargerResponses`, which the later rules honour.

---

## 6. Selector engine (`internal/consensus/policy/`)

- **Compile once, pool of 8.** `Compile` compiles the operator expression to a
  `sobek.Program` and pre-warms `poolSize = 8` VMs (`pool.go`, `selector.go`).
  Borrows are **non-blocking**: an empty pool returns `ErrPoolExhausted` (a
  bypass) rather than queueing the request path.
- **Sandbox.** `newSandboxRuntime` is a bare `sobek.New()` with the JSON field
  mapper only — **no `env`, `process.env`, or `console`**, unlike the shared
  `common.NewRuntime()`. `evalFunction` is operator-writable config, so it never
  gets host-process env access.
- **Bounded build.** The one-time evaluation of the operator function
  *expression* during pool construction is bounded by `runProgramBounded` using
  the operator's `evalTimeout`, so a config like `(() => { while(true){} })()`
  fails config load loudly instead of hanging startup.
- **Per-eval timeout.** `Evaluate` runs wrap+call in a timed goroutine; on
  overrun it interrupts and **discards** the poisoned VM (rebuilt async by
  `pool.discard`); a plain throw leaves the VM clean and it is returned to the
  pool. `resultName`: `null`/`undefined` → `""` (default), a string → the name,
  anything else → `ErrEvalThrew`.
- **No JS globals for host callables.** `fn` and `wrap` are held in Go (`vm`
  struct), not on JS globals, so operator code cannot install setters on those
  slots to run outside the timeout.

The package imports nothing from `consensus/`, `health/`, or `auth/`; the
executor builds the `EvalContext` and hands in plain values.

### 6.1 Selector wiring (`consensus/selector.go`, `consensus/consensus.go`)

`resolvePolicy` maps eval outcomes to fail-closed reasons:

| Outcome | Metric | Resolves to |
|---|---|---|
| `ErrPoolExhausted` | `eval_bypassed_total{reason="pool_exhausted"}` | default |
| `ErrEvalTimeout` | `eval_failed_total{reason="timeout"}` | default |
| other error / non-string | `eval_failed_total{reason="throw"}` | default |
| `name == ""` (null return) | *(none — not a failure)* | default |
| unknown name | `eval_failed_total{reason="unknown_name"}` | default |
| known name | `policy_selected_total{consensus_policy=name}` | that policy |

`defaultPolicyName == common.ConsensusDefaultPolicyName == "default"` (reserved).
`markSelected` also records the name on `ExecState` (→ response header).

**Cordon pin.** `buildEvalContext` sets `PunishedOrOperator` from
`req.NetworkUpstreams()` filtered by `hasPunishmentOrOperator`, separate from the
candidate `Upstreams` (`req.Upstreams()`). Selection drops punished/cordoned
nodes (`removeCordoned`) **before** eval, so `anyPunished()` / `anyOperatorCordon()`
must scan the network set to still see them — otherwise a punished node dropped
by selection would be invisible and could not pin the request to `standard`.
`toEvalUpstreams` maps cordon classes via `tr.CordonClasses(up, method)`; a
non-empty class set → `StateCordoned` (MVP: `StateUnhealthy` is reserved, no
observed case forces it yet), and block-availability bounds map through the
`MinInt64/MaxInt64` open sentinel to a nil range.

---

## 7. Header contract

`X-ERPC-Consensus-Policy` is **output-only**. It is written on the response in
`erpc/http_server.go` from the `ExecState` snapshot (`SetConsensusPolicy`), and
is emitted **only when a selector is configured**; plain inline-consensus setups
never emit it. It is never read from the request — a client cannot influence the
policy through it. The value is `"default"` when the round ran under the inline
default (null return or fail-closed).

---

## 8. Auth / roles

`common.User.Roles []string` (`common/user.go`) is populated only by the JWT
strategy (`auth/strategy_jwt.go`) from `RolesClaimName` (default `"roles"`).
`extractRoles` accepts an array, an array of `{value: …}` objects, or a single
comma-separated string, normalized and deduplicated. All other strategies
(`secret`, `network`, `database`, `siwe`) leave `Roles` empty, so `hasRole`
returns false. Roles never come from client-controlled headers, query params, or
body fields.

---

## 9. Config & validation surface

Config (`common/config.go`):

- `ConsensusCustomPolicyConfig { EvalFunction, EvalTimeout }` — `EvalTimeout`
  default **50ms** (`common/defaults.go`).
- `ConsensusPolicyConfig.Policies map[string]*ConsensusPolicyConfig`
- `ConsensusPolicyConfig.BlockEvidenceFields map[string][]string`
- `ConsensusRequiredParticipant.{WaiveAgreementOnMissingData,
  WaiveAgreementOnEmptyOutsideRetention, RetentionBlocks}`
- `JwtStrategyConfig.RolesClaimName`

Startup validation (`common/validation.go`) rejects:

- `policies["default"]` — reserved (labels the fail-closed inline default).
- a named policy that itself defines `customPolicy` or `policies` — selection is
  top-level only, flat map.
- any waiver opt-in without a **never-waivable** entry carrying
  `minAgreement > 0` (the floor). Applies to **both** waivers.
- `waiveAgreementOnEmptyOutsideRetention` without `retentionBlocks > 0` on that
  entry **and** a non-empty policy-level `blockEvidenceFields` — otherwise the
  waiver could never prove anything and would silently never fire.
- two policies sharing an S3 `misbehaviorsDestination.path` unless every
  sharing policy's `filePattern` contains `{timestampMs}`.
- a `customPolicy.evalFunction` that fails to smoke-compile.

---

## 10. Metrics (all `erpc_`-prefixed, `telemetry/metrics.go`)

| Metric | Type | Labels | Fires |
|---|---|---|---|
| `erpc_consensus_policy_selected_total` | Counter | `project, network, consensus_policy` | Per served round — the chosen policy name. |
| `erpc_consensus_policy_eval_failed_total` | Counter | `project, network, reason` | Eval **ran** and failed. `reason`: `timeout`, `throw`, `unknown_name`. |
| `erpc_consensus_policy_eval_bypassed_total` | Counter | `project, network, reason` | Eval **never ran**. `reason`: `pool_exhausted`. |
| `erpc_consensus_policy_eval_duration_seconds` | Histogram | `project, network` | Per-eval wall-clock. |
| `erpc_consensus_composition_waived_total` | Counter | `project, network, tag, reason` | Quota waived. `reason`: `missing_data`, `empty_outside_retention`. |
| `erpc_consensus_composition_waiver_unproven_total` | Counter | `project, network, reason` | Empty-waiver held for missing proof. `reason`: `no_winner_block`, `no_avail_bound`, `no_head`. Mid-round deferral is debug-only, not counted. |

Note the split the pre-implementation spec got wrong: `pool_exhausted` is a
**bypassed** reason (separate metric), not a `eval_failed_total` reason, because
the operator function never ran.

---

## 11. Deferred / not fully exercised in the trial

Honest status so this file doesn't over-claim:

- **JWT `hasRole` gating** — the plumbing (`User.Roles`, `rolesClaimName`,
  `extractRoles`) shipped and the selector reads it, but the staging trial
  exercised selectors **ungated** (health / block-range / cordon predicates),
  not live role-gated grades. The role path is code-complete but not
  battle-tested end-to-end in the trial.
- **Decision cache** — still future work (feature.md §5 / plan.md Phase 6).
  Measured eval cost has not forced it; not built.
