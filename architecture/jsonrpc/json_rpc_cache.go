package jsonrpc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/data"
	"github.com/erpc/erpc/telemetry"
	"github.com/klauspost/compress/zstd"
	"github.com/rs/zerolog"
)

// GenericJsonRpcCache is the cache DAL for jsonrpc-architecture networks. It
// reuses the shared data.CachePolicy + data.Connector abstractions exactly like
// the EVM and SVM caches — the network id ("jsonrpc:<slug>") keeps the
// namespaces disjoint — with the generic key scheme from
// specs/generic-jsonrpc/feature.md §7 / feature-v2 §4.5:
//
//   - partition key: {networkId}[:{cacheKeySuffix}][:{tipBucket}] — the ref
//     segment is the tip/bucketSize quotient for methods declaring
//     cacheTipBucket (requires a resolvable tip via the network's
//     JsonRpcHighestTip), empty otherwise;
//   - range key: req.CacheHash() — method + sha256 of params, the same
//     identity multiplexing uses.
//
// There is deliberately no blockRef/block-timestamp machinery: freshness is the
// operator's TTL bound, and no chain-specific method table exists here.
type GenericJsonRpcCache struct {
	projectId string
	policies  []*data.CachePolicy
	logger    *zerolog.Logger

	// compressionThreshold is the minimum payload size (bytes) worth
	// compressing; 0 means compression is disabled for writes. The decoder is
	// always present so entries written while compression was enabled stay
	// readable after an operator turns it off.
	compressionThreshold int
	encoder              *zstd.Encoder
	decoder              *zstd.Decoder
}

// NewGenericJsonRpcCache constructs the cache from a shared common.CacheConfig.
// It mirrors svm.NewSvmJsonRpcCache so erpc/init.go can wire all architecture
// caches in one place.
func NewGenericJsonRpcCache(ctx context.Context, logger *zerolog.Logger, cfg *common.CacheConfig) (*GenericJsonRpcCache, error) {
	lg := logger.With().Str("component", "genericJsonRpcCache").Logger()

	connectors := make(map[string]data.Connector)
	for _, connCfg := range cfg.Connectors {
		c, err := data.NewConnector(ctx, &lg, connCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create connector %s: %w", connCfg.Id, err)
		}
		connectors[connCfg.Id] = c
	}

	var policies []*data.CachePolicy
	for _, policyCfg := range cfg.Policies {
		connector, ok := connectors[policyCfg.Connector]
		if !ok {
			return nil, fmt.Errorf("connector %s not found for policy", policyCfg.Connector)
		}
		policy, err := data.NewCachePolicy(policyCfg, connector)
		if err != nil {
			return nil, fmt.Errorf("failed to create policy: %w", err)
		}
		policies = append(policies, policy)
	}

	cache := &GenericJsonRpcCache{policies: policies, logger: &lg}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd decoder: %w", err)
	}
	cache.decoder = decoder

	if cfg.Compression != nil && cfg.Compression.Enabled != nil && *cfg.Compression.Enabled {
		level := zstd.SpeedFastest
		switch strings.ToLower(cfg.Compression.ZstdLevel) {
		case "", "fastest":
		case "default":
			level = zstd.SpeedDefault
		case "better":
			level = zstd.SpeedBetterCompression
		case "best":
			level = zstd.SpeedBestCompression
		default:
			lg.Warn().Str("level", cfg.Compression.ZstdLevel).Msg("unknown compression level, using 'fastest'")
		}
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(level))
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd encoder: %w", err)
		}
		cache.encoder = encoder
		cache.compressionThreshold = 512
		if cfg.Compression.Threshold > 0 {
			cache.compressionThreshold = cfg.Compression.Threshold
		}
		lg.Info().
			Int("threshold", cache.compressionThreshold).
			Str("level", level.String()).
			Msg("generic jsonrpc cache compression configured")
	}

	return cache, nil
}

// WithProjectId returns a shallow copy tagged with the project id so per-project
// telemetry and logs show the right owner.
func (c *GenericJsonRpcCache) WithProjectId(projectId string) *GenericJsonRpcCache {
	lg := c.logger.With().Str("projectId", projectId).Logger()
	return &GenericJsonRpcCache{
		projectId:            projectId,
		policies:             c.policies,
		logger:               &lg,
		compressionThreshold: c.compressionThreshold,
		encoder:              c.encoder,
		decoder:              c.decoder,
	}
}

func (c *GenericJsonRpcCache) SetPolicies(policies []*data.CachePolicy) {
	c.policies = policies
}

// IsObjectNull is the nil-safe check callers use before dispatching to the cache.
func (c *GenericJsonRpcCache) IsObjectNull() bool {
	return c == nil || len(c.policies) == 0
}

// Get tries each matching policy in order and returns the first non-empty hit.
// A miss is indicated by (nil, nil) — the network layer then falls through to
// upstream forwarding.
func (c *GenericJsonRpcCache) Get(ctx context.Context, req *common.NormalizedRequest) (*common.NormalizedResponse, error) {
	start := time.Now()
	rpcReq, err := req.JsonRpcRequest(ctx)
	if err != nil {
		return nil, err
	}

	finality := req.Finality(ctx)
	policies, err := c.findGetPolicies(req.NetworkId(), rpcReq.Method, rpcReq.Params, finality)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		telemetry.MetricCacheGetSkippedTotal.
			WithLabelValues(c.projectId, req.NetworkLabel(), rpcReq.Method).
			Inc()
		return nil, nil
	}

	sawMiss, sawError := false, false
	for _, policy := range policies {
		connector := policy.GetConnector()
		if req.ShouldSkipCacheRead(connector.Id()) {
			continue
		}
		ttlLabel := ttlString(policy.GetTTL())
		jrr, err := c.doGet(ctx, connector, req, rpcReq)
		if err != nil {
			// Semantic-miss errors: the connector is signalling "no key" /
			// "expired", not a real failure — identical handling to the EVM/SVM
			// caches, see the long comment in svm/json_rpc_cache.go.
			if common.HasErrorCode(err, common.ErrCodeRecordNotFound) ||
				common.HasErrorCode(err, common.ErrCodeRecordExpired) ||
				common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
				sawMiss = true
				continue
			}
			telemetry.MetricCacheGetErrorTotal.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
				common.ErrorSummary(err),
			).Inc()
			c.logger.Debug().Err(err).Str("connector", connector.Id()).
				Msg("generic jsonrpc cache get failed; trying next policy")
			sawError = true
			continue
		}
		if jrr != nil {
			telemetry.MetricCacheGetSuccessHitTotal.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
			).Inc()
			telemetry.MetricCacheGetSuccessHitDuration.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
			).Observe(time.Since(start).Seconds())
			return common.NewNormalizedResponse().
				WithRequest(req).
				WithFromCache(true).
				WithJsonRpcResponse(jrr), nil
		}
		sawMiss = true
	}
	missReason := "empty_result"
	switch {
	case sawMiss:
		missReason = "connector_miss"
	case sawError:
		missReason = "connector_error"
	}
	firstPolicy := policies[0]
	firstTTL := ttlString(firstPolicy.GetTTL())
	telemetry.MetricCacheGetSuccessMissTotal.WithLabelValues(
		c.projectId, req.NetworkLabel(), rpcReq.Method,
		firstPolicy.GetConnector().Id(), firstPolicy.String(), firstTTL,
		missReason,
	).Inc()
	telemetry.MetricCacheGetSuccessMissDuration.WithLabelValues(
		c.projectId, req.NetworkLabel(), rpcReq.Method,
		firstPolicy.GetConnector().Id(), firstPolicy.String(), firstTTL,
	).Observe(time.Since(start).Seconds())
	return nil, nil
}

// ttlString returns the string form of a policy TTL, or "none" when the
// pointer is nil — keeps the metric label cardinality bounded.
func ttlString(ttl *time.Duration) string {
	if ttl == nil {
		return "none"
	}
	return ttl.String()
}

// Set writes the response to every matching policy concurrently-addressed;
// failures are logged and swallowed per-policy — a single flaky connector must
// not fail the upstream request path. Responses carrying a JSON-RPC error are
// never cached: the caller may retry against another upstream, and classified
// failures (missingData shapes) must not enter the cache at all.
func (c *GenericJsonRpcCache) Set(ctx context.Context, req *common.NormalizedRequest, resp *common.NormalizedResponse) error {
	if resp == nil || resp.IsObjectNull() {
		return nil
	}
	rpcReq, err := req.JsonRpcRequest(ctx)
	if err != nil {
		return err
	}
	rpcResp, err := resp.JsonRpcResponse(ctx)
	if err != nil {
		return err
	}
	if rpcResp == nil || rpcResp.Error != nil {
		return nil
	}

	finality := req.Finality(ctx)
	isEmpty := resp.IsResultEmptyish()
	policies, err := c.findSetPolicies(req.NetworkId(), rpcReq.Method, rpcReq.Params, finality, isEmpty)
	if err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}

	groupKey, requestKey, err := c.generateKeys(ctx, req, rpcReq)
	if err != nil {
		return err
	}
	payload := rpcResp.GetResultBytes()
	if payload == nil {
		return nil
	}
	var valueToStore []byte

	for _, policy := range policies {
		connector := policy.GetConnector()
		ttl := policy.GetTTL()
		ttlLabel := ttlString(ttl)
		if !policy.MatchesSizeLimits(len(payload)) {
			telemetry.MetricCacheSetSkippedTotal.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
			).Inc()
			continue
		}
		if valueToStore == nil {
			valueToStore = c.compress(payload)
		}
		telemetry.MetricCacheSetOriginalBytes.WithLabelValues(
			c.projectId, req.NetworkLabel(), rpcReq.Method,
			connector.Id(), policy.String(), ttlLabel,
		).Add(float64(len(payload)))
		if len(valueToStore) != len(payload) {
			telemetry.MetricCacheSetCompressedBytes.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
			).Add(float64(len(valueToStore)))
		}
		setCtx := data.WithReverseIndex(ctx, req.NetworkId(), req.CacheKeySuffix())
		if err := connector.Set(setCtx, groupKey, requestKey, valueToStore, ttl); err != nil {
			telemetry.MetricCacheSetErrorTotal.WithLabelValues(
				c.projectId, req.NetworkLabel(), rpcReq.Method,
				connector.Id(), policy.String(), ttlLabel,
				common.ErrorSummary(err),
			).Inc()
			c.logger.Warn().Err(err).Str("connector", connector.Id()).
				Str("groupKey", groupKey).Str("requestKey", requestKey).
				Msg("generic jsonrpc cache set failed")
			continue
		}
	}
	return nil
}

func (c *GenericJsonRpcCache) findGetPolicies(networkId, method string, params []interface{}, finality common.DataFinalityState) ([]*data.CachePolicy, error) {
	var matched []*data.CachePolicy
	seen := make(map[data.Connector]bool)
	for _, p := range c.policies {
		ok, err := p.MatchesForGet(networkId, method, params, finality)
		if err != nil {
			return nil, err
		}
		if ok {
			conn := p.GetConnector()
			if !seen[conn] {
				matched = append(matched, p)
				seen[conn] = true
			}
		}
	}
	return matched, nil
}

func (c *GenericJsonRpcCache) findSetPolicies(networkId, method string, params []interface{}, finality common.DataFinalityState, isEmpty bool) ([]*data.CachePolicy, error) {
	var matched []*data.CachePolicy
	for _, p := range c.policies {
		ok, err := p.MatchesForSet(networkId, method, params, finality, isEmpty)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, p)
		}
	}
	return matched, nil
}

func (c *GenericJsonRpcCache) doGet(ctx context.Context, connector data.Connector, req *common.NormalizedRequest, rpcReq *common.JsonRpcRequest) (*common.JsonRpcResponse, error) {
	rpcReq.RLockWithTrace(ctx)
	defer rpcReq.RUnlock()

	groupKey, requestKey, err := c.generateKeys(ctx, req, rpcReq)
	if err != nil {
		return nil, err
	}

	resultBytes, err := connector.Get(ctx, data.ConnectorMainIndex, groupKey, requestKey, req)
	if err != nil {
		return nil, err
	}
	if len(resultBytes) == 0 {
		return nil, nil
	}
	resultBytes, err = c.decompress(resultBytes)
	if err != nil {
		return nil, err
	}

	jrr, err := common.NewJsonRpcResponseFromBytes(nil, resultBytes, nil)
	if err != nil {
		return nil, err
	}
	_ = jrr.SetID(rpcReq.ID)
	return jrr, nil
}

// generateKeys derives the (partition, range) key pair. The ref segment is the
// tip bucket for methods declaring cacheTipBucket — when the method does not
// declare one, or no tip is resolvable, the ref stays empty (CachePartitionKey
// yields {networkId}:{suffix-key}: with an empty final segment, matching how
// the EVM cache passes an empty blockRef for non-block-aware requests).
func (c *GenericJsonRpcCache) generateKeys(ctx context.Context, req *common.NormalizedRequest, rpcReq *common.JsonRpcRequest) (string, string, error) {
	requestKey, err := req.CacheHash(ctx)
	if err != nil {
		return "", "", err
	}
	ref := ""
	if methodCfg := methodConfigForRequest(req, rpcReq); methodCfg != nil && methodCfg.CacheTipBucket > 0 {
		if tip, ok := networkTip(ctx, req); ok && tip > 0 {
			ref = strconv.FormatInt(tip/int64(methodCfg.CacheTipBucket), 10)
		}
	}
	return common.CachePartitionKey(req.NetworkId(), req.CacheKeySuffix(), ref), requestKey, nil
}

// methodConfigForRequest resolves the operator's method declaration through
// the request's network config. Nil-tolerant at every step so cache paths work
// for requests constructed without a network (internal probes, tests).
func methodConfigForRequest(req *common.NormalizedRequest, rpcReq *common.JsonRpcRequest) *common.CacheMethodConfig {
	if req == nil || rpcReq == nil {
		return nil
	}
	n := req.Network()
	if n == nil {
		return nil
	}
	nc := n.Config()
	if nc == nil {
		return nil
	}
	return nc.Methods.FindMethodConfig(rpcReq.Method)
}

// networkTip resolves the network's highest tracked tip. The method is added
// to erpc.Network in the same change set; the type assertion degrades to
// "no bucket" (plain key) on any other Network implementation.
func networkTip(ctx context.Context, req *common.NormalizedRequest) (int64, bool) {
	if req == nil {
		return 0, false
	}
	n := req.Network()
	if n == nil {
		return 0, false
	}
	tipper, ok := n.(interface{ JsonRpcHighestTip(context.Context) int64 })
	if !ok {
		return 0, false
	}
	return tipper.JsonRpcHighestTip(ctx), true
}

// compress returns the zstd-compressed payload when compression is enabled and
// the payload is both over the threshold and actually smaller compressed;
// otherwise it returns payload unchanged.
func (c *GenericJsonRpcCache) compress(payload []byte) []byte {
	if c.encoder == nil || len(payload) < c.compressionThreshold {
		return payload
	}
	compressed := c.encoder.EncodeAll(payload, nil)
	if len(compressed) >= len(payload) {
		return payload
	}
	return compressed
}

// decompress inflates a stored value when it carries the zstd magic number.
// The check is on the bytes, not on the current config, so entries written
// while compression was enabled stay readable after it is turned off.
func (c *GenericJsonRpcCache) decompress(stored []byte) ([]byte, error) {
	if len(stored) < 4 || stored[0] != 0x28 || stored[1] != 0xB5 || stored[2] != 0x2F || stored[3] != 0xFD {
		return stored, nil
	}
	if c.decoder == nil {
		return nil, fmt.Errorf("cached value is zstd-compressed but no decoder is configured")
	}
	decompressed, err := c.decoder.DecodeAll(stored, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress cached value: %w", err)
	}
	return decompressed, nil
}

// Compile-time assertion that GenericJsonRpcCache implements common.CacheDAL.
var _ common.CacheDAL = (*GenericJsonRpcCache)(nil)
