# Inside eRPC — Round 2 deep-dives (spec addendum)

Extends `SPEC.md` (round 1). Same conventions, same page anatomy, same widget
rules (SSR-safe static SVG + GSAP hydration in `useEffect`, killed/rebuilt
timeline on Replay, IntersectionObserver autoplay, reduced-motion → final
state, `.cv-dd-root dd-widget` shell with dd-head/dd-cap/dd-replay/dd-stage).
No `Callout` component exists — first-principles callouts are plain markdown
blockquotes. Imports are deep-relative (`../../components/explainer/...`).
Every default/metric/label below was verified against source during research
(2026-09-11, base 4d26c86a); page authors must re-verify before citing.

New nav order (parent updates `docs/pages/inside-erpc/_meta.js` in integration):
request-lifecycle, request-tracing, finality-and-caching, failsafe-in-depth,
block-availability, consensus, health-and-selection, evm-translation,
architecture-svm, errors, metrics.

---

## §6.8 request-tracing — following one call through every function

Title: **"Request tracing — following one call through every function"**
Widget: `<TimelineRace scenarios={tracingScenarios} />` — 4 tabs. This is the
"as much detail as possible" page: the function-level call chain, the OTel
span map, and 4 different request examples.

### The call chain (verified; cite with file:line in "In the source")

1. **Ingress**: `createRequestHandler` (erpc/http_server.go:816) → span
   `Http.ReceivedRequest` → aliasing → `parseUrlPath` (:877) → gzip body
   (:372) → batch split, one goroutine per element (:427-465) →
   `NewNormalizedRequest` → span `Request.Handle` → `Validate` →
   IgnoreMethods/AllowMethods gating → `auth.NewPayloadFromHttp` →
   `project.GetNetwork` → directive enrichment (`ApplyDirectiveDefaults`,
   `EnrichFromHttp`) → `project.Forward` (:725).
2. **Auth + project**: `ProjectsRegistry.GetProject` →
   `AuthenticateConsumer` (erpc/projects.go:123) → `AuthRegistry.Authenticate`
   (auth/registry.go:46) strategy chain → strategy-level rate permit
   (`authorizer.go:117`, origin=`auth`) → `Project.Forward` span
   (erpc/projects.go:132) → `AcquireRateLimitPermit` (erpc/projects.go:374,
   origin=`project`) → `doForward` → `HandleProjectPreForward` →
   `network.Forward`.
3. **Network.Forward** (erpc/networks.go:1743): bind ctx →
   `tryServeStaticResponse` (:1788) → multiplexer `handleMultiplexing` (:3050;
   leader/follower) → `cacheDal.Get` (:1823, span `Cache.Get`) →
   `policyEngine.GetOrderedInLane` (:1863) → SVM prefilters →
   `SetUpstreams` → `HandleNetworkPreForward` → `tryShortCircuitFutureBlock`
   (:2005) → network rate permit (:2022, origin=`network`) →
   `prepareRequest` (:2030, EVM `NormalizeHttpJsonRpc`) →
   `getFailsafeExecutor` (:1708, first match by method/finality/kind) →
   `failsafeExecutor.Run` (:2357) → async cache write (:2426, `AddRef`,
   10s ctx on appCtx) → `ExecState().Apply(forwardSpan)` (:2464) →
   `normalizeResponse` (:3326, id fidelity to `IDRawBytes`).
4. **Failsafe executor** (erpc/network_executor.go): `Run` (:164) =
   `timeout( consensus? ( retry( hedge( sweep|tryOneUpstream ))))` —
   timeout ctx `WithTimeoutCause(..., ErrDynamicTimeoutExceeded)` (:175);
   retry loop `runRetry` (:227) with `shouldRetryWithReason` (:396) reasons
   `execution_exception_retryable|block_unavailable|missing_data|retryable_error|empty_result|pending_tx`;
   `runHedge` (:589) adaptive delay, keep-rule at :677; consensus branch
   (:205) `consensus.Run` over `retry(hedge(tryOneUpstream))`.
5. **Upstream.Forward** (upstream/upstream.go:543): `shouldSkip`
   (cordon/exclusion/breaker) → upstream budget permit (:590, origin=`upstream`,
   credit mode) → `tryForward` → `RecordUpstreamRequest` →
   `Client.SendRequest` (:778; span `HttpJsonRpcClient.sendSingleRequest`
   clients/http_json_rpc_client.go:667) → validate → classify (:819) →
   health record / quantile `ObserveDuration` (:911, reverts only) →
   upstream-scope failsafe executor `Run` (:964) → timeout metric.
6. **Response**: `WriteResponse` span → `setResponseHeaders` (:1172;
   `writeCounterHeaders` :1225) → traceparent injection (:765) →
   `determineResponseStatusCode` (200 default :1591).

### OTel span map (the page's signature table)

`Http.ReceivedRequest` → `Http.ReadBody` (detail) → `Http.ParseRequests`
(detail) → `Request.Handle` → `Project.Forward` → `RateLimiter.DoLimit`
(CLIENT, attrs budget/method/scope/result) → `Network.Forward` →
`PolicyEngine.GetOrdered` (detail) → `Cache.Get` / `Cache.GetForPolicy` →
`Network.forwardAttempt` → `Network.UpstreamLoop` (detail) →
`Consensus.Run` + `Consensus.CollectResponses` (detail) → `Upstream.Forward` →
`Upstream.tryForward.PreRequest` / `.SendRequest` (detail) →
`HttpJsonRpcClient.sendSingleRequest` (or `GrpcBdsClient.*`) → `Cache.Set` →
`Network.NormalizeResponse` (detail) → `HttpServer.WriteResponse` (detail).
Tiers: `StartSpan` always-on, `StartDetailSpan` needs `tracing.detailed`;
force via header `X-ERPC-Force-Trace: true` or `?force-trace=true`
(common/tracing_core.go:29). Auth path has NO span of its own. Response
carries W3C traceparent header (tracing_util.go:58). ExecState attrs applied
to spans: `execution.attempts/retries/hedges`, `upstreams.tried/outcomes/
reasons/durations_ms/won` (common/exec_state.go:325).

### Widget tabs (TimelineScenario data)

- **"Cache-hit read"** (~30ms axis): lanes Http → Auth+Project →
  Network(multiplex leader) → Cache(HIT, green) → Response. Punchline chip:
  "6 functions, 0 upstream calls". Late fade-in: async cache SET goroutine
  after response.
- **"Cold read · hedge"** (~200ms axis): cache MISS → ordering → hedge fires
  at adaptive delay → two upstream attempts race (winner green, discard
  amber `ErrUpstreamHedgeCancelled`) → response → async cache SET. Show
  `X-ERPC-Hedges: 1` header chip.
- **"Retry on missing data"** (~1.2s axis, catch-up): upstream A → `-32014`
  missing_data → reason=missing_data retry (wait histogram beat) → upstream B
  success → response. Chip: `dataUnavailableCapReached` guard note.
- **"Consensus call"** (~250ms axis): 3 slots (reason=consensus_slot) →
  2/3 agree → winner; annotate `Consensus.Run` span + analyzer goroutine
  surviving the response.

### First principles

- Tracing follows the code, not the deployment topology: span names ARE the
  function names.
- Timeouts are contexts with causes (`ErrDynamicTimeoutExceeded`), not
  checked flags — the deepest layer learns the budget the same way.
- The response path never blocks on bookkeeping (async cache write on
  appCtx; analyzer drains in background).

### AISection must include

Config schema (`tracing` block: `enabled`, `detailed`, `endpoint`,
`forceTraceMatchers` — verify in common/config.go), edge cases (auth
invisible in traces; batch = N `Request.Handle` spans; force-trace bypasses
sampler), observability (`erpc_unexpected_panic_total{scope}`), source entry
points, related pages (reference/tracing? verify existence; monitoring).

---

## §6.9 architecture-svm — Solana through the same pipeline

Title: **"Architecture SVM — Solana through the same pipeline"**
Widget: bespoke **`SvmSlotLanes`** (`docs/components/explainer/SvmSlotLanes.tsx`).

### Core verified facts to teach

- Registry dispatch, not if/else: each architecture registers an
  `ArchitectureHandler` via `init()` (common/architecture.go:22;
  architecture/svm/handler.go:10); pipeline calls
  `Handle{Project,Network,Upstream}{Pre,Post}Forward` +
  `NewJsonRpcErrorExtractor` (common/architecture.go:11-18). "The pipeline
  files never need to change for a new chain."
- `SvmStatePoller` (architecture/svm/svm_state_poller.go:77): polls 4 RPCs
  per tick (`getHealth`, `getSlot{processed}`, `getSlot{finalized}`,
  `getMaxShredInsertSlot`; static payloads :69-73); tick 400ms
  (`DefaultPollInterval` :23); tolerated rollback 1024 slots (:19);
  lag > 100 slots = degraded (`MaxShredInsertSlotLagThreshold`
  common/architecture_svm.go:56); traffic-fed skips via harvested
  `context.slot` (hooks.go:700-739, max 4 skips).
- Majority vs max: `SvmHighestLatestSlot`/`SvmHighestFinalizedSlot` are
  MAJORITY tips via `evm.PickServedTip` (erpc/networks.go:1369-1390);
  `...SlotMax`/`...IndexedSlot` are pool MAXIMA for rejection gates
  (:1392-1424). Advertising asymmetry: promise majority, serve max
  (hooks.go:504-514).
- Commitment → finality is SPLIT (architecture/svm/finality.go):
  `GetFinality` (:158) for cacheability — `neverCacheMethods` → Realtime
  (:32-51), `alwaysFinalizedMethods` → Finalized (:67-70),
  `slotPinnedMethods` Finalized only at explicit `finalized` commitment
  (:106-115), **everything else → Realtime** (:174-177) because
  `commitment: finalized` on Solana is a moving head (~400ms); routing uses
  `IsFinalizedCommitment` (:208) with `effectiveCommitment` fallback to node
  default `finalized` (:339-378).
- Commitment injection (hooks.go): `resolveCommitment` (:265) stamps
  `svm.commitment` into option objects (:188); `processed` clamped to
  `confirmed` for `atLeastConfirmedMethods` (:130-138, Agave -32602);
  write methods get `preflightCommitment`/`commitment` (:400-416);
  `InvalidateCacheHash` after injection (common/json_rpc.go:1437).
- Special methods: `getGenesisHash` answered from a hardcoded table without
  an upstream call (hooks.go:143-167; table common/architecture_svm.go:81-87);
  `getBlock`/`getConfirmedBlock` availability gate (hooks.go:542-618, `-32014`
  `ErrEndpointMissingData`, margin = 2 + debounce/400ms :628-636,
  `svm.enforceBlockAvailability` default true erpc/networks.go:1426-1433);
  `getSlot` floor/cap post-forward (hooks.go:810-945); **`getBlockHeight`
  deliberately excluded** — height ≠ slot (handler.go:70-81);
  `sendTransaction` etc. non-retryable writes (util.go:20-28), airdrop is
  single-dispatch (blocks consensus fan-out, util.go:46-48).
- Cache keying diverges ON PURPOSE: `SvmJsonRpcCache` avoids `req.CacheHash()`
  (lowercasing hasher would collapse case-sensitive base58 pubkeys,
  architecture/svm/json_rpc_cache.go:27-29,456-463); `svmRequestKey`
  preserves case (:477-498); partition key `<networkId>:<slotRef>`, slotRef =
  `minContextSlot` or `"*"` (:554-577). No block-timestamp age guard for SVM.
- gRPC BDS: for SVM ONLY a cache connector (`driver: grpc`), serving exactly
  ONE method (`getBlock`); SVM upstreams are HTTP-only —
  `clients/registry.go:122-142` errors on other schemes for `UpstreamTypeSvm`.
  `networkId` REQUIRED in connector config (eth_chainId unimplemented on BDS
  readers, common/config.go:384-397). json/jsonParsed only; skipped slots
  fall through (TODO BDA-3110, grpc_bds_client.go:1529-1534).
- Genesis validation fail-closed at bootstrap against the known-cluster
  genesis-hash table; unknown clusters only with `svm.checkGenesisHash: true`
  (upstream/upstream.go:354-368).
- Defaults: `chain` "solana" (common/architecture_svm.go:92-97),
  `statePollerDebounce` 400ms (common/defaults.go:2535-2540),
  `maxFinalizedSlotLag` 100 (common/defaults.go:2541-2550; explicit 0
  disables the consensus slot-lag filter), `commitment` NO default
  (common/defaults.go:2526-2530). No SVM-specific failsafe struct.
- Selection: same health tracker (slots fed as block numbers,
  svm_state_poller.go:172-182) + two architecture-only prefilters when a
  consensus policy is active and commitment is finalized:
  `FilterByFinalizedSlotLag` (slot_lag.go:32-54, second-highest clamp
  :107-133) and `FilterByMinContextSlot` (slot_lag.go:142-157); both fail
  open. No WebSocket support anywhere (clients/registry.go:100-101).

### Widget story (SvmSlotLanes, 3 beats)

- **Beat 1 — the poller**: three upstream lanes (ups-a/b/c) with slot
  counters advancing on 400ms ticks at different rates; a poller chip hops
  lane to lane firing the 4 RPC dots; getHealth every 5th tick; majority tip
  line vs pool-max line drawn across the lanes (two distinct lines — this is
  the SVM signature).
- **Beat 2 — a finalized getBlock**: request arrives with
  `commitment: "finalized"` → injection stamp animation → slot-lag filter
  crosses ups-c (root lag > 100) → availability gate: requested slot above
  min(indexedTip)+margin on ups-b → `-32014` → rerouted to ups-a →
  `context.slot` harvested from response → poller skip counter ticks.
- **Beat 3 — cache key**: key chips for a base58-pinned request showing
  case preserved (`sv1...` vs `SV1...` two distinct keys side by side —
  the EVM hasher would have collapsed them) and partition key
  `svm:mainnet:93728481` (minContextSlot) vs `svm:mainnet:*`.

### First principles

- Slots are not blocks: heights, roots, shreds and processed tips are four
  different axes — the code names them separately everywhere.
- "Finalized" means different things to a cache and a router; splitting
  `GetFinality` (cacheability) from `IsFinalizedCommitment` (routing) is the
  load-bearing design decision.
- Extension point: new chain = new handler package + registry entry; the
  pipeline is architecture-agnostic.

---

## §6.10 errors — one taxonomy, every provider

Title: **"Errors — one taxonomy, every provider"**
Widget: bespoke **`ErrorJourney`** (`docs/components/explainer/ErrorJourney.tsx`).
Cross-link the existing `docs/pages/reference/errors.mdx` (full taxonomy
table) — do not duplicate it; this page teaches the *machinery*.

### Core verified facts

- Foundation: `BaseError{Code, Message, Cause, Details}` (common/errors.go:185-192);
  `StandardError` seam (:194-203); `Details` carries behavioral flags:
  `retryableTowardNetwork` (:210-218), `permanentMissingData` (:225-233).
  No architecture-specific error types — both normalize into one taxonomy.
- Structural retryability (NO `RetryableErrorHint`, NO per-method code
  table — state this explicitly, readers will expect it):
  `IsRetryableTowardNetwork` (common/errors.go:2550-2607; default true;
  false only for causeless exhausted / all-children-non-retryable bundles /
  explicit flag on the single-cause chain; deliberately never descends into
  joined bundles :2544-2549); `IsRetryableTowardsUpstream` (:2636-2701, hard
  no-retry list :2653-2690); `IsPermanentlyMissingData` (:2616-2634);
  `IsCapacityIssue` (:2703-2712); `ClassifySeverity` critical/warning/info
  (:2736-2771).
- Decision points: upstream retry `shouldRetry`
  (upstream/upstream_executor.go:233-265; non-retryable writes never retry
  :249-253; missing-data honors `RetryEmpty` directive :254-263); network
  retry `shouldRetryWithReason` (erpc/network_executor.go:396-536; reasons
  become metric labels :314); hedge keep-rule (upstream :314-325; network
  `kept = !IsRetryableTowardNetwork` :677); breaker outcome classifier
  `upstreamBreakerOutcome` (upstream/upstream_executor.go:398-428; hedge,
  probes, composite never touch the breaker :378-392).
- Normalization pipeline: client calls injected `JsonRpcErrorExtractor` on
  JSON-RPC error or HTTP >299 (clients/http_json_rpc_client.go:924);
  `CompositeJsonRpcErrorExtractor` tries each architecture's extractor in
  sorted order, first non-nil wins (upstream/composite_error_extractor.go:13-42
  — the file is about architecture multiplexing, NOT merging upstreams);
  EVM cascade architecture/evm/error_normalizer.go:20-767 (ordered matchers,
  fallback `ErrEndpointServerSideException` raw-code passthrough :701-711);
  gRPC twin `ExtractGrpcError` (:769+) and common/grpc_errors.go:9;
  SVM normalizer architecture/svm/error_normalizer.go (client-side marks
  `WithRetryableTowardNetwork(false)` :206-209; permanent-missing wire codes
  -32007/-32009 referenced erpc/network_executor.go:441-446).
- Vendor hooks: `Vendor.GetVendorSpecificErrorIfAny` fires BEFORE generic
  rules (error_normalizer.go:29, :987-1009); ~23 vendors implement it;
  exemplar Alchemy (thirdparty/alchemy.go:481-549): access-key →
  `ErrEndpointUnauthorized`/-32016; code 3 disambiguated by
  `IsMissingDataError` into missing-data (-32014, network-retryable) vs
  revert (execution exception) — tests alchemy_test.go:241-257 assert both
  directions.
- Client rendering: `TranslateToJsonRpcException` (common/json_rpc.go:1704-1878):
  collapse exhausted bundle to dominant cause via `orderCauses`
  (common/errors.go:916-978 — rank 1 retryable-toward-network first, rank 2
  upstream id, tie-break text; deterministic, never map order); class→wire
  code map: rate limits -32005 (:1769-1783), auth -32016 (:1784-1795),
  method ignored -32601 (:1796-1810), UNANIMOUS block-unavailable -32014
  (`isBlockUnavailableVerdict` :1684-1700), unmarshal -32700, invalid
  request -32602, getLogs guardrails -32012, else -32603 with deepest
  message (:1864-1877); already-normalized `ErrJsonRpcExceptionInternal`
  returned as-is (:1762-1764). HTTP status: JSON-RPC default 200
  (erpc/http_server.go:1840-1843); 400/401/404/429 exceptions (:1846-1860);
  409 dispute / 412 low-participants via `ErrorStatusCode()`.
- Named wire codes (common/errors.go:2322-2345): -99999 unknown, -32000
  call exception, -32003 tx rejected, -32600 invalid request, -32601 method
  not found, -32602 invalid params, -32603 internal, -32700 parse, 3 revert
  (de-facto), -32005 capacity (collides with Solana NodeUnhealthy —
  workaround erpc/http_server.go:1775-1789), -32012 too large, -32014
  missing data, -32015 node timeout, -32016 unauthorized.
- Multi-upstream merge: `NewErrUpstreamsExhausted` (common/errors.go:980-1013)
  joins causes + details (durationMs/projectId/networkId/method/attempts/
  retries/hedges); `SummarizeCauses` 18 buckets (:1060-1206, e.g. "2 upstream
  missing data, 1 upstream timeout"); dominance = most-frequent code,
  first-of-bucket (common/json_rpc.go:1721-1757); human summary appended to
  message (:1007-1010).
- Metrics: `erpc_upstream_attempt_outcome_total` 13 outcomes
  (common/exec_state.go:18-30: success, empty, transport_error,
  server_error, client_error, rate_limited, missing_data, exec_revert,
  block_unavailable, breaker_open, cancelled, timeout, skipped) with
  classification precedence `classifyUpstreamOutcome`
  (upstream/upstream.go:37-79, unmatched defaults to server_error :73);
  `erpc_upstream_selection_total{reason}` primary/retry/hedge/consensus_slot/
  sweep (common/exec_state.go:39-45, upstream/upstream.go:145-158);
  `erpc_network_retry_attempt_total{reason}` (6 values);
  `erpc_upstream_request_errors_total{error,severity}`.

### Widget story (ErrorJourney — 3 lanes, one decision tree)

Three wire errors enter left as raw JSON-RPC error objects; each travels:
raw → vendor hook chip → architecture normalizer → taxonomy error card
(name + retryableTowardNetwork flag chip flipping true/false) → decision
gate (retry? hedge? breaker? sweep on?) → client-rendered error object
(right side). Lanes:

1. **Revert (code 3)**: Alchemy hook → `ErrEndpointExecutionException` →
   retryableTowardNetwork=false → no retry; rendered `{code:3, data:0x08c379a0…}`.
2. **getLogs too large (-32012)**: `ErrEndpointRequestTooLarge`
   (`TooLargeComplaint` evm_block_range :2290-2293) → not retried as-is;
   routed to proactive split / bisect (cross-link evm-translation page).
3. **Missing data (-32014)**: `ErrEndpointMissingData`,
   retryableTowardNetwork=true, permanentMissingData=false → catch-up retry
   loop → second attempt succeeds.
A footer strip shows the exhausted-bundle render: 3 upstream causes →
`orderCauses` (retryable first) → dominant code → summary string.

### First principles

- Errors are data: the `Details` flags are the API the policies query.
- Retryability is structural, not configured — the error's class and flags
  decide; directives (RetryEmpty/RetryPending) only widen the retry set.
- The client sees a verdict, not a log: bundles collapse to one dominant,
  deterministic, unanimity-gated answer; HTTP 200 is deliberate for JSON-RPC.

---

## §6.11 metrics — reading erpc_* like a pro

Title: **"Metrics — reading erpc_* like a pro"**
Widget: bespoke **`MetricAnatomy`** (`docs/components/explainer/MetricAnatomy.tsx`).
Cross-link `docs/pages/reference/metrics.mdx` (full list) and
`monitoring/catch-up-metrics.md` — this page teaches how the families fit
the request path and what NOT to misread.

### Widget story (MetricAnatomy)

A horizontal mini-pipeline (Http → Project/Auth → Network(cache) → Executor →
Upstream → Cache-write → Response) plays a cold read with hedge; under each
stage, the exact metric(s) emitted light up in monospace chips as the
playhead passes, accumulating in a right-hand ledger with running counters.
Final ledger state (all verified): `erpc_network_request_received_total +1`,
`erpc_cache_get_success_miss_total{reason=connector_miss} +1`,
`erpc_upstream_attempt_outcome_total ×2 (success + cancelled)`,
`erpc_upstream_selection_total{reason=hedge} +1`,
`erpc_network_hedge_winner_total +1`, `erpc_upstream_request_total +1`,
`erpc_cache_set_success_total +1`, `erpc_network_successful_request_total +1`,
`erpc_network_request_duration_seconds` observed. A Replay replays; a
"cache HIT" toggle restarts the pass showing the hit ledger (upstream
metrics never appear). Reduced motion → ledger fully populated.

### AISection content requirements

Family tables (name / type / key labels + value enums / main emission site),
verified during research:

- **Network**: request_received, successful_request, failed_request,
  request_duration_seconds, multiplexed_request, static_response_served,
  hedged_request, hedge_discards, hedge_winner, retry_attempt_total{reason
  6 values}, timeout_fired_total{scope network|upstream}, timeout_duration,
  hedge_delay_seconds, data_unavailable_wait_seconds (catch-up baseline is
  NORMAL — cite monitoring/catch-up-metrics.md), no_upstreams_available,
  evm getLogs/trace_filter split family, served_tip gauges/counters
  (transitions not evaluations).
- **Upstream**: request_total (per attempt, has vendor/composite/attempt
  labels), request_errors_total{error,severity}, attempt_outcome_total
  (13 outcomes, is_hedge/is_retry), selection_total{reason 5},
  credit_units_total, request_duration_seconds, response_size_bytes,
  wrong_empty_response, misbehavior_total, breaker_state_change_total
  {transition 4}, cordon gauges/counters, staleness/state-probe family
  (stale_*, state_probe_total{probe,outcome}, block_head_large_rollback).
- **Cache**: get_success_hit/miss (miss reasons connector_miss/
  connector_error/ttl_rejected/empty_result), get_error, get_skipped,
  get_age_guard_reject, set_success/error/skipped, set original/compressed
  bytes, cache_executor_attempt_total{outcome 7} + breaker transitions,
  connector block-number gauges, ristretto cost gauge.
- **Consensus**: consensus_total{outcome 7}, duration, agreement_count,
  responses_collected (histogram of counts, NOT duration), short_circuit,
  wait_capped{trigger}, misbehavior_detected, upstream_punished,
  upstream_errors, cancellations{phase=caller_abandoned}, panics.
- **Selection**: erpc_selection_* family (position, score, eval_duration,
  eval_errors{kind}, rejection{step}, exclusion{reason},
  primary_switch{from,to}, readmit, sticky_hold, probe_requests /
  probe_errors{reason} / probe_skipped{reason} / probe_dropped,
  eligible_upstreams) — note scoring is HIGHER-is-better despite the stale
  "Lower = better" help string on erpc_selection_score
  (telemetry/metrics.go:260 vs internal/policy/stdlib.js sort).
- **Rate limit / auth / misc**: erpc_rate_limits_total{origin 4, scope},
  budget_max_count gauge, failopen_total, remote_* family, budget_decision
  DEPRECATED; erpc_auth_failed_total{strategy,reason}; CORS family; shadow
  identical/mismatch/error; erpc_unexpected_panic_total{scope};
  erpc_grpc_bds_hard_timeout_total / conn_replacements.

### Commonly-misread list (AISection edge cases + visible-body teaser)

1. `erpc_upstream_block_head_large_rollback` is a sticky GAUGE — `> 0`
   latches forever; `rate()` is meaningless (telemetry/metrics.go:568-577).
2. served_tip_regression/trajectory count TRANSITIONS, not evaluations —
   any sustained non-zero rate is alert-worthy (:157-198).
3. `erpc_network_served_tip_lag_blocks` is ABSENT (not 0) when served-tip
   is in default MAX mode (:133-142).
4. `erpc_rate_limiter_budget_decision_total` is DEPRECATED → use
   `erpc_rate_limits_total` (:791-795).
5. `finality` label is dual-meaning: head axis (latest|finalized) on the
   rollback gauge, DataFinalityState elsewhere (:562-567).
6. `cache_executor_attempt_total{outcome="success"}` = "answered within
   budget", NOT cache hit (:742-757).
7. `request_total` (pre-dispatch, often finality=unknown) vs misbehavior
   (post-resolution) — per-finality ratios can exceed 100% (:600-609).
8. Hedge attempts are excluded from upstream request/error counters —
   low error rates can hide heavy hedge churn (`hedge_discards_total` is the
   tell, :657-661).
9. Catch-up retries (`missing_data|block_unavailable|empty_result`) have a
   normal non-zero baseline on live chains — judge pressure trend, not raw
   counts (monitoring/catch-up-metrics.md).
10. `erpc_consensus_responses_collected` is a histogram of response COUNTS.

### First principles

- Metrics are emitted at decision points, not wrappers: each family maps to
  one place where eRPC commits to an outcome.
- Counters are denominators: rates against `attempt_outcome_total` and
  `request_total` answer "how healthy" per (network, upstream, method).
- Label cardinality is a design choice: error fingerprints are sanitized
  256-char summaries (common/errors.go:117-125), never raw strings.
