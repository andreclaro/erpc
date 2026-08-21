# Group-Aware Served Tip — Specification

> Let operators declare upstream groups (via existing tag/id selectors) and
> compute the network-wide served `latest`/`finalized` tip as the **intersection
> of group-majorities**: the highest block that every required group can serve.
> This guarantees that later requests routed through consensus requiring one
> participant from each group can always be served by at least one upstream from
> every group.

## 1. Problem

eRPC's served tip is today a strict majority order statistic over all eligible
upstreams (`evm.PickServedTip`), or over a single selector-scoped subset for
`use-upstream` lanes. This works when all upstreams are roughly in sync, but
fails when one provider group consistently lags:

- A pool mixes `type:external` (quicknode, alchemy) with `type:internal`
  (internal-blue, internal-green).
- The external group sees block 200; the internal group is still at 100.
- The global majority over all four upstreams is 100 (only 2 of 4 have 200),
  which is correct for the untargeted case.
- But when the internal group is *just one block behind* (external 200,
  internal 199), the global majority becomes 200, even though the internal group
  cannot yet serve 200.
- Any request that later needs consensus across one external + one internal
  upstream will fail on the internal side for block 200.

The fix: let the operator declare which groups matter, and advertise only a
block that every group has reached by its own majority.

## 2. Goals & constraints

- **Group guarantee.** The served tip must be servable by at least one upstream
  from each required group.
- **Use existing selectors.** Groups are expressed with the same tag/id selectors
  already used by `use-upstream` (`common.UpstreamMatchesSelector`). No new
  grouping concept, no `family` requirement, no bespoke config dimension.
- **Backward compatible.** Empty `requiredGroups` means byte-identical behavior;
  existing configs keep working.
- **Monotonic & servable by construction.** Each group's contribution is the
  existing strict-majority picker; the global tip is their minimum.
- **Per-request cost.** O(`groups` × `pool`) with small constants; no new durable
  state on the hot path.
- **Play nice with existing guards.** Regression guard, trajectory referee,
  guaranteed-method floor, selector-scoped lanes, and metrics must all keep
  sensible semantics.

## 3. Core idea

For each configured group selector `G`:

1. Filter the policy-eligible pool to upstreams matching `G`.
2. Run the normal served-tip ballot (`evmTipBallot` + `evm.PickServedTip`) over
   that subset.
3. The group tip `tip_G` is the strict-majority value within `G`.

The network-wide tip is:

```
tip = min(tip_G for G in requiredGroups)
```

Because `tip_G` is a strict-majority value, at least `floor(|G|/2)+1` members of
`G` already have it. The minimum therefore has at least one member from every
required group that can serve it.

## 4. Config schema

```yaml
networks:
  - architecture: evm
    evm:
      chainId: 1
      servedTip:
        enabledFor: ["latest", "finalized"]
        requiredGroups:
          - "type:external"
          - "type:internal"
```

Field added to `EvmServedTipConfig`:

```go
// RequiredGroups is a list of upstream selectors. When non-empty, the served
// tip for the network-wide lane is the minimum of the strict-majority tips
// computed independently within each selector-matched group. This guarantees
// the advertised block is servable by at least one upstream from every
// required group, which is necessary when requests are later routed through
// consensus that needs participation from each group. Empty (default) keeps
// the existing global-majority behavior.
RequiredGroups []string `yaml:"requiredGroups,omitempty" json:"requiredGroups,omitempty"`
```

- Each selector is validated by `common.ValidatePattern`.
- Runtime: warn if a selector matches zero upstreams; ignore that group to avoid
  outage on misconfiguration (fail-open for empty groups).

## 5. Algorithm

In `erpc/networks.go`, when computing the network-wide served tip and
`requiredGroups` is non-empty:

```
ups   = policy-eligible upstreams (already filtered by syncing/caps in evmTipBallot)
tips  = global ballot over ups
pick  = PickServedTip(tips)      // for Freshest, Inputs, Sorted telemetry

minTip = +∞
minRef = {Corroborated: +∞, Max: +∞}

for each selector in requiredGroups:
    upsG  = upstreams in ups matching selector
    tipsG, refG = evmTipBallot(upsG, axis)
    pickG = PickServedTip(tipsG)

    minTip = min(minTip, pickG.Tip)
    minRef.Corroborated = min(minRef.Corroborated, refG.Corroborated)
    minRef.Max          = min(minRef.Max, refG.Max)

    emit per-group metric (group=selector, value=pickG.Tip, lag=pick.Freshest-pickG.Tip)

pick.Tip = minTip   // 0 if no group produced a tip
return pick, minRef
```

### Why this reference for the regression guard?

The regression guard normally compares the pick against the second-highest live
head across the whole pool. With required groups, the pick is intentionally
lower than the fastest group's heads. Using `min(group corroborated heads)` as
the guard reference means:

- A deliberate gap between fast and slow groups is **not** treated as a
  regression.
- A poisoned ballot that pushes the global tip below what every group currently
  corroborates is still caught.

## 6. Interactions with existing features

| Feature | Behavior with `requiredGroups` |
|---|---|
| **Trajectory referee** | Disabled for the network-wide lane. Raising the tip above the slowest group would break the group guarantee. The referee stays active for non-group-aware networks and for selector-scoped lanes. |
| **Regression guard** | Uses the group-aware reference described in §5. |
| **Guaranteed methods floor** | Extended to apply the same group-aware logic over each method's supporting upstreams, so method-specific `latest` resolutions also respect group guarantees. |
| **Selector-scoped lanes (`use-upstream`)** | Unaffected. When a request carries a selector, the scoped tip is computed among the matched subset only; `requiredGroups` is **not** applied to the scoped pick. This prevents a request targeting one group from being forced to zero because other required groups are absent from the scoped set. |
| **Metrics** | Existing `lane="all"` gauges show the group-aware tip. New per-group gauges (`erpc_network_served_tip_group_block_number`, `erpc_network_served_tip_group_lag_blocks`) expose each group's majority tip and its lag vs. the global freshest view. |

## 7. Observability

New metrics in `telemetry/metrics.go`:

| Metric | Labels | Meaning |
|---|---|---|
| `erpc_network_served_tip_group_block_number` | project, network, group, axis | Strict-majority tip inside one required group |
| `erpc_network_served_tip_group_lag_blocks` | project, network, group, axis | That group's tip lag behind the global corroborated freshest view |

Existing `erpc_network_served_tip_block_number{lane="all"}` becomes the
`min(group tips)`. Per-group lag makes the bottleneck group visible.

## 8. Test scenarios

1. **Disabled (default).** No `requiredGroups` → existing global-majority
   behavior unchanged.
2. **Two groups, fast vs slow.** External {200,200}, internal {100,100} → tip
   = 100.
3. **One lagger inside a group.** External {201,200}, internal {100,99} →
   external majority = 200, internal majority = 99, tip = 99.
4. **Single-upstream group.** External {200}, internal {100,99} → tip = 99.
5. **Empty group selector.** Log warning, ignore the group, continue with the
   remaining groups.
6. **Selector-scoped request.** `use-upstream=type:external` returns external
   majority = 200, ignoring `requiredGroups` for the scoped pick.
7. **Regression guard stays healthy.** With external=200, internal=100 and
   tolerance=1024, the guard reference = 100 and does not hold.
8. **Guaranteed method floor respects groups.** Trace-capable external {200},
   internal {100} → floor = 100.
9. **Referee does not override.** Warm trajectory, then stall internal while
   external stays on trajectory; with `requiredGroups` the tip stays at the
   internal majority.

## 9. Backward compatibility & migration

- Empty `requiredGroups` is the default; existing configs parse and behave
  identically.
- No new directive, no change to `use-upstream` semantics.
- The trajectory referee is disabled only when `requiredGroups` is explicitly
  configured for the network-wide lane.

## 10. Out of scope / deferred

- Per-group monotonic shared counters. The first version is stateless per
  request; monotonicity comes from the upstream poller counters and the existing
  network-wide anchor, just like the non-group-aware path.
- Fail-closed behavior for empty groups. The first version warns and ignores to
  avoid outages from typos; a future metric/alert can make this visible.
- Automatic group discovery. Groups must be explicitly declared; eRPC does not
  infer them from tag keys.
