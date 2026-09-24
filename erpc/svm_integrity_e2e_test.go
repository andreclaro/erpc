package erpc

// End-to-end SVM integrity tests: real network/upstream/hook plumbing with
// only the HTTP transport mocked (gock). The wire-format fixture builder is
// intentionally local to this package — an independent implementation of the
// Solana wire format, so a serializer bug in the integrity package fails the
// ed25519 round-trip instead of mirroring itself.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/data"
	"github.com/erpc/erpc/health"
	"github.com/erpc/erpc/thirdparty"
	"github.com/erpc/erpc/upstream"
	"github.com/erpc/erpc/util"
	"github.com/h2non/gock"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const svmB58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func svmB58Encode(b []byte) string {
	var out []byte
	z := 0
	for z < len(b) && b[z] == 0 {
		z++
	}
	n := make([]byte, 0, (len(b)*138/100)+1)
	sz := (len(b)-z)*138/100 + 1
	tmp := make([]byte, sz)
	for _, c := range b[z:] {
		carry := int(c)
		for j := sz - 1; j >= 0; j-- {
			carry += 256 * int(tmp[j])
			tmp[j] = byte(carry % 58)
			carry /= 58
		}
	}
	j := 0
	for j < sz && tmp[j] == 0 {
		j++
	}
	for ; j < sz; j++ {
		n = append(n, svmB58Alphabet[tmp[j]])
	}
	out = append(out, strings.Repeat("1", z)...)
	out = append(out, n...)
	return string(out)
}

func svmShortvec(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

type svmFixtureKey struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	b58  string
}

func svmFixtureKeys(t *testing.T, n int) []svmFixtureKey {
	t.Helper()
	out := make([]svmFixtureKey, n)
	for i := range out {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		out[i] = svmFixtureKey{pub: pub, priv: priv, b58: svmB58Encode(pub)}
	}
	return out
}

func svmFixture32(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed
	}
	return b
}

// svmSignedTx builds one legacy-format signed transaction and returns its RPC
// JSON (encoding:"json"). signers are the first n keys.
func svmSignedTx(t *testing.T, keys []svmFixtureKey, signers int, recent []byte) map[string]any {
	t.Helper()
	rawKeys := make([][]byte, len(keys))
	for i, k := range keys {
		rawKeys[i] = k.pub
	}
	// Independently serialize the wire message (legacy).
	var msg []byte
	msg = append(msg, byte(signers), 0, byte(len(keys)-signers))
	msg = append(msg, svmShortvec(len(keys))...)
	for _, k := range rawKeys {
		msg = append(msg, k...)
	}
	msg = append(msg, recent...)
	data := []byte{1, 2, 3}
	msg = append(msg, svmShortvec(1)...)
	msg = append(msg, 1)                         // programIdIndex
	msg = append(msg, svmShortvec(1)...)         // account count
	msg = append(msg, 0)                         // account 0
	msg = append(msg, svmShortvec(len(data))...) // data len
	msg = append(msg, data...)

	sigs := make([]string, signers)
	for i := 0; i < signers; i++ {
		sigs[i] = svmB58Encode(ed25519.Sign(keys[i].priv, msg))
	}
	keyStrs := make([]string, len(keys))
	for i, k := range keys {
		keyStrs[i] = k.b58
	}
	return map[string]any{
		"signatures": sigs,
		"message": map[string]any{
			"accountKeys":     keyStrs,
			"header":          map[string]any{"numRequiredSignatures": signers, "numReadonlySignedAccounts": 0, "numReadonlyUnsignedAccounts": len(keys) - signers},
			"recentBlockhash": svmB58Encode(recent),
			"instructions": []map[string]any{{
				"programIdIndex": 1,
				"accounts":       []int{0},
				"data":           svmB58Encode(data),
			}},
		},
	}
}

// svmBlockResult returns a getBlock result for slot 100. corrupt selects the
// poison variant:
//   - "selfParent": previousBlockhash == blockhash (structural)
//   - "badSig": tx signed over one recentBlockhash, JSON claims another
//     (authenticity — the check must re-derive the signed bytes and fail)
func svmBlockResult(t *testing.T, corrupt string) string {
	t.Helper()
	keys := svmFixtureKeys(t, 3)
	recent := svmFixture32(9)
	signedRecent := svmFixture32(9)
	if corrupt == "badSig" {
		signedRecent = svmFixture32(9)
		recent = svmFixture32(200) // JSON claims a different blockhash
	}
	tx := svmSignedTx(t, keys, 2, signedRecent)
	if corrupt == "badSig" {
		tx["message"].(map[string]any)["recentBlockhash"] = svmB58Encode(recent)
	}
	blockhash := svmFixture32(7)
	previous := svmFixture32(3)
	if corrupt == "selfParent" {
		previous = blockhash
	}
	block := map[string]any{
		"blockhash":         svmB58Encode(blockhash),
		"previousBlockhash": svmB58Encode(previous),
		"parentSlot":        95,
		"blockHeight":       90,
		"blockTime":         1700000000,
		"transactions":      []any{tx},
		"rewards":           []any{},
	}
	raw, err := json.Marshal(block)
	require.NoError(t, err)
	return string(raw)
}

func mockSvmGetBlock(host, resultBody string, times int) {
	gock.New("http://" + host).
		Post("").
		Times(times).
		Filter(func(r *http.Request) bool {
			return r.URL.Host == host && strings.Contains(util.SafeReadBody(r), `"method":"getBlock"`)
		}).
		Reply(200).
		BodyString(`{"jsonrpc":"2.0","id":1,"result":` + resultBody + `}`)
}

// setupSvmIntegrityNetwork wires a two-upstream SVM network with a retry
// failsafe (so a content-validation error triggers failover) and the given
// integrity config. With integrityCfg == nil it exercises the opt-out path.
func setupSvmIntegrityNetwork(t *testing.T, ctx context.Context, integrityCfg *common.IntegrityConfig, cluster string) *Network {
	t.Helper()
	if cluster == "" {
		cluster = "mainnet-beta"
	}
	rateLimitersRegistry, _ := upstream.NewRateLimitersRegistry(ctx, &common.RateLimiterConfig{}, &log.Logger)
	metricsTracker := health.NewTracker(&log.Logger, "test", time.Minute)

	vr := thirdparty.NewVendorsRegistry()
	pr, err := thirdparty.NewProvidersRegistry(&log.Logger, vr, []*common.ProviderConfig{}, nil)
	require.NoError(t, err)

	sharedStateCfg := &common.SharedStateConfig{
		Connector: &common.ConnectorConfig{
			Driver: common.DriverMemory,
			Memory: &common.MemoryConnectorConfig{MaxItems: 100_000, MaxTotalSize: "1GB"},
		},
		LockMaxWait:     common.Duration(200 * time.Millisecond),
		UpdateMaxWait:   common.Duration(200 * time.Millisecond),
		FallbackTimeout: common.Duration(3 * time.Second),
		LockTtl:         common.Duration(4 * time.Second),
	}
	sharedStateCfg.SetDefaults("test")
	ssr, err := data.NewSharedStateRegistry(ctx, &log.Logger, sharedStateCfg)
	require.NoError(t, err)

	upstreams := []*common.UpstreamConfig{
		{Id: "rpc1", Type: common.UpstreamTypeSvm, Endpoint: "http://svm-integ-rpc1.localhost", Svm: &common.SvmUpstreamConfig{Cluster: cluster}},
		{Id: "rpc2", Type: common.UpstreamTypeSvm, Endpoint: "http://svm-integ-rpc2.localhost", Svm: &common.SvmUpstreamConfig{Cluster: cluster}},
	}
	upstreamsRegistry := upstream.NewUpstreamsRegistry(
		ctx, &log.Logger, "test", upstreams, ssr, rateLimitersRegistry, vr, pr,
		nil, metricsTracker, nil,
	)

	networkConfig := &common.NetworkConfig{
		Architecture: common.ArchitectureSvm,
		Svm: &common.SvmNetworkConfig{
			Cluster:    cluster,
			Commitment: "confirmed",
		},
		Failsafe: []*common.FailsafeConfig{{
			Retry: &common.RetryPolicyConfig{MaxAttempts: 3},
		}},
		Integrity: integrityCfg,
	}
	network, err := NewNetwork(
		ctx, &log.Logger, "test", networkConfig,
		rateLimitersRegistry, upstreamsRegistry, metricsTracker, nil,
	)
	require.NoError(t, err)

	upstreamsRegistry.Bootstrap(ctx)
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, upstreamsRegistry.PrepareUpstreamsForNetwork(ctx, util.SvmNetworkId("", cluster)))
	time.Sleep(150 * time.Millisecond)
	network.PinUpstreamOrderForTest("rpc1", "rpc2")
	return network
}

func svmGetBlockRequest() *common.NormalizedRequest {
	return common.NewNormalizedRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[100, {"encoding":"json","transactionDetails":"full"}]}`))
}

func svmIntrinsicIntegrity() *common.IntegrityConfig {
	return &common.IntegrityConfig{
		IntegritySettings: common.IntegritySettings{
			Level: "intrinsic",
		},
	}
}

// TestSvmIntegrity_CorruptSignatureFailsOver is the headline scenario: rpc1
// serves a block whose transaction signature does NOT verify against its
// claimed message (the exact "mixed-up / tampering node" case). The integrity
// engine must convert that to a content-validation error, the retry failsafe
// must fail over to rpc2, and the caller receives the honest block.
func TestSvmIntegrity_CorruptSignatureFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)
	util.SetupMocksForSvmStatePoller("svm-integ-rpc2.localhost", 1000, 990)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockSvmGetBlock("svm-integ-rpc1.localhost", svmBlockResult(t, "badSig"), 1)
	mockSvmGetBlock("svm-integ-rpc2.localhost", svmBlockResult(t, ""), 1)

	net := setupSvmIntegrityNetwork(t, ctx, svmIntrinsicIntegrity(), "")
	req := svmGetBlockRequest()
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "must fail over to the healthy upstream")
	require.NotNil(t, resp)

	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	prev, err := jrr.PeekStringByPath(ctx, "previousBlockhash")
	require.NoError(t, err)
	assert.Equal(t, svmB58Encode(svmFixture32(3)), prev, "the served block must be the honest upstream's")
	assert.True(t, req.IntegrityCaught(), "a reject-then-recover request must be flagged IntegrityCaught")
}

// TestSvmIntegrity_SelfParentBlockFailsOver exercises the structural check:
// a block naming itself as parent is geometrically impossible — reject and
// fail over.
func TestSvmIntegrity_SelfParentBlockFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)
	util.SetupMocksForSvmStatePoller("svm-integ-rpc2.localhost", 1000, 990)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockSvmGetBlock("svm-integ-rpc1.localhost", svmBlockResult(t, "selfParent"), 1)
	mockSvmGetBlock("svm-integ-rpc2.localhost", svmBlockResult(t, ""), 1)

	net := setupSvmIntegrityNetwork(t, ctx, svmIntrinsicIntegrity(), "")
	req := svmGetBlockRequest()
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "self-parent block must trigger failover")
	require.NotNil(t, resp)
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	prev, err := jrr.PeekStringByPath(ctx, "previousBlockhash")
	require.NoError(t, err)
	assert.Equal(t, svmB58Encode(svmFixture32(3)), prev)
}

// TestSvmIntegrity_CleanBlockPasses proves the engine does not over-reject
// real data: a correctly signed, well-formed block passes through untouched.
func TestSvmIntegrity_CleanBlockPasses(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockSvmGetBlock("svm-integ-rpc1.localhost", svmBlockResult(t, ""), 1)

	net := setupSvmIntegrityNetwork(t, ctx, svmIntrinsicIntegrity(), "")
	req := svmGetBlockRequest()
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, req.IntegrityCaught(), "an honest block must not be flagged")
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	prev, err := jrr.PeekStringByPath(ctx, "previousBlockhash")
	require.NoError(t, err)
	assert.Equal(t, svmB58Encode(svmFixture32(3)), prev)
}

// TestSvmIntegrity_OffByDefault guards the opt-in contract: with no integrity
// config, a corrupt block is proxied untouched (the module must not change
// behavior for networks that did not ask for it).
func TestSvmIntegrity_OffByDefault(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfParent := svmBlockResult(t, "selfParent")
	mockSvmGetBlock("svm-integ-rpc1.localhost", selfParent, 1)

	net := setupSvmIntegrityNetwork(t, ctx, nil, "")
	req := svmGetBlockRequest()
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "without an integrity config nothing runs — the response is proxied as-is")
	require.NotNil(t, resp)
	assert.False(t, req.IntegrityCaught())
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	prev, err := jrr.PeekStringByPath(ctx, "previousBlockhash")
	require.NoError(t, err)
	assert.Equal(t, svmB58Encode(svmFixture32(7)), prev, "self-parent corruption is served when integrity is off")
}

// svmPollerMocksWithoutGenesis registers the state-poller mocks
// (getSlot processed/finalized, getHealth, getMaxShredInsertSlot) WITHOUT the
// getGenesisHash mock — SetupMocksForSvmStatePoller's genesis mock is
// registered first and Persist(), so it would shadow a test's own genesis
// mocks (gock matches in registration order).
func svmPollerMocksWithoutGenesis(host string, latestSlot, finalizedSlot int64) {
	match := func(needle string) func(*http.Request) bool {
		return func(r *http.Request) bool {
			return r.URL.Host == host && strings.Contains(util.SafeReadBody(r), needle)
		}
	}
	gock.New("http://" + host).Post("").Persist().Filter(match("getHealth")).
		Reply(200).BodyString(`{"jsonrpc":"2.0","id":1,"result":"ok"}`)
	gock.New("http://" + host).Post("").Persist().Filter(match(`"method":"getSlot"`)).
		Reply(200).BodyString(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%d}`, latestSlot))
	gock.New("http://" + host).Post("").Persist().Filter(match("getMaxShredInsertSlot")).
		Reply(200).BodyString(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%d}`, latestSlot))
}

// TestSvmIntegrity_GenesisMismatchFailsOver pins cluster identity: an
// upstream serving a genesis hash that is not the expected one is rejected
// and failed around. Uses an UNKNOWN cluster so the getGenesisHash
// short-circuit (which serves known-cluster genesis from the table without
// contacting upstreams) does not fire, and binds the expected hash via a
// per-check override — exactly the escape hatch operators have for private
// clusters.
func TestSvmIntegrity_GenesisMismatchFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	svmPollerMocksWithoutGenesis("svm-integ-rpc1.localhost", 1000, 990)
	svmPollerMocksWithoutGenesis("svm-integ-rpc2.localhost", 1000, 990)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const expectedGenesis = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"
	genesisMethod := func(r *http.Request) bool {
		return strings.Contains(util.SafeReadBody(r), `"method":"getGenesisHash"`)
	}
	// rpc1 claims a genesis that is NOT the expected one. Persist() because the
	// bootstrap/poller may also probe genesis before the user request lands.
	gock.New("http://svm-integ-rpc1.localhost").
		Post("").Persist().Filter(genesisMethod).
		Reply(200).BodyString(`{"jsonrpc":"2.0","id":1,"result":"` + svmB58Encode(svmFixture32(0xee)) + `"}`)
	// rpc2 answers with the expected one.
	gock.New("http://svm-integ-rpc2.localhost").
		Post("").Persist().Filter(genesisMethod).
		Reply(200).BodyString(`{"jsonrpc":"2.0","id":1,"result":"` + expectedGenesis + `"}`)

	cfg := svmIntrinsicIntegrity()
	cfg.Checks = map[string]*common.IntegrityCheckConfig{
		"svm.auth.genesisHash": {Params: map[string]string{"expected": expectedGenesis}},
	}
	// "customnet" is not in the known-genesis table, so the request reaches
	// upstreams and the override above supplies ground truth.
	net := setupSvmIntegrityNetwork(t, ctx, cfg, "customnet")
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getGenesisHash","params":[]}`))
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "must fail over to the upstream with the expected genesis")
	require.NotNil(t, resp)
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), expectedGenesis,
		"the served genesis must be the expected one")
	assert.True(t, req.IntegrityCaught())
}

// svmChainBlockResult builds a getBlock result with caller-controlled link
// fields: blockhash/parent hashes derived from byte seeds (independent local
// base58, same as the rest of this fixture package).
func svmChainBlockResult(hashSeed, prevSeed byte, parentSlot, blockHeight int64) string {
	block := map[string]any{
		"blockhash":         svmB58Encode(svmFixture32(hashSeed)),
		"previousBlockhash": svmB58Encode(svmFixture32(prevSeed)),
		"parentSlot":        parentSlot,
		"blockHeight":       blockHeight,
		"blockTime":         1700000000,
		"transactions":      []any{},
		"rewards":           []any{},
	}
	raw, err := json.Marshal(block)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func svmGetBlockAtRequest(slot int) *common.NormalizedRequest {
	return common.NewNormalizedRequest([]byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[%d, {"encoding":"json","transactionDetails":"full"}]}`, slot)))
}

// mockSvmGetBlockAt registers a getBlock mock that only matches requests for
// the given slot, replied with resultBody, for times requests.
func mockSvmGetBlockAt(host string, slot int, resultBody string, times int) {
	gock.New("http://" + host).
		Post("").
		Times(times).
		Filter(func(r *http.Request) bool {
			return r.URL.Host == host &&
				strings.Contains(util.SafeReadBody(r), `"method":"getBlock"`) &&
				strings.Contains(util.SafeReadBody(r), fmt.Sprintf(`"params":[%d`, slot))
		}).
		Reply(200).
		BodyString(`{"jsonrpc":"2.0","id":1,"result":` + resultBody + `}`)
}

// TestSvmIntegrity_ParentLinkFailsOver exercises the verified-block chain
// index end to end: the first getBlock anchors a finalized parent, the second
// getBlock returns a child whose previousBlockhash does not match the
// anchored parent — a finalized-slot contradiction (svm.commit.parentLink)
// that must reject and fail over to the honest upstream's consistent child.
func TestSvmIntegrity_ParentLinkFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)
	util.SetupMocksForSvmStatePoller("svm-integ-rpc2.localhost", 1000, 990)

	// Canonical chain: slot 900 (hash seed 7, height 800) ← slot 901
	// (hash seed 8, height 801). rpc1 poisons 901's parent link (seed 200).
	mockSvmGetBlockAt("svm-integ-rpc1.localhost", 900, svmChainBlockResult(7, 3, 899, 800), 1)
	mockSvmGetBlockAt("svm-integ-rpc1.localhost", 901, svmChainBlockResult(8, 200, 900, 801), 1)
	// rpc2 only gets asked for 901 — request 1 passes on rpc1 and anchors the
	// shared network index, so no failover occurs on the parent.
	mockSvmGetBlockAt("svm-integ-rpc2.localhost", 901, svmChainBlockResult(8, 7, 900, 801), 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	net := setupSvmIntegrityNetwork(t, ctx, svmCorroboratedIntegrity(), "mainnet-beta")

	// Request 1: anchor slot 900 on whichever upstream serves it.
	resp, err := svmProjectForward(ctx, net, svmGetBlockAtRequest(900))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Request 2: rpc1 serves a child whose parent link is broken.
	resp, err = svmProjectForward(ctx, net, svmGetBlockAtRequest(901))
	require.NoError(t, err, "must fail over to the upstream with the consistent chain")
	require.NotNil(t, resp)
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), svmB58Encode(svmFixture32(8)),
		"the served block must be the honest upstream's child")
	assert.Contains(t, string(jrr.GetResultBytes()), `"blockHeight":801`)
	// Failover proof: the honest child's previousBlockhash (seed 7) exists
	// only in rpc2's mock — rpc1's poisoned variant used seed 200.
	assert.Contains(t, string(jrr.GetResultBytes()), svmB58Encode(svmFixture32(7)),
		"the served child must be the honest upstream's")
}

func svmCorroboratedIntegrity() *common.IntegrityConfig {
	return &common.IntegrityConfig{
		IntegritySettings: common.IntegritySettings{
			Level: "corroborated",
		},
	}
}

// TestSvmIntegrity_FinalizedBoundFailsOver exercises svm.final.finalizedBound
// end to end: rpc1 answers a finalized-commitment getAccountInfo with data
// from a slot ABOVE its own finalized tip (internally contradictory — the
// node labels unfinalized data as finalized). With the per-check hardReject
// override the engine rejects and the retry fails over to rpc2's consistent
// answer.
func TestSvmIntegrity_FinalizedBoundFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)
	util.SetupMocksForSvmStatePoller("svm-integ-rpc2.localhost", 1000, 990)

	acctResult := func(slot, lamports int) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":` + svmItoa(slot) + `},"value":{"lamports":` + svmItoa(lamports) + `,"data":["","base64"],"owner":"11111111111111111111111111111111","executable":false}}}`
	}
	mockGetAccountInfo := func(host string, body string) {
		gock.New("http://" + host).
			Post("").Times(1).
			Filter(func(r *http.Request) bool {
				return r.URL.Host == host && strings.Contains(util.SafeReadBody(r), `"method":"getAccountInfo"`)
			}).
			Reply(200).BodyString(body)
	}
	// rpc1 claims finalized data from slot 995 while its own finalized tip is 990.
	mockGetAccountInfo("svm-integ-rpc1.localhost", acctResult(995, 999))
	// rpc2 answers from below the tip.
	mockGetAccountInfo("svm-integ-rpc2.localhost", acctResult(980, 111))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := svmCorroboratedIntegrity()
	cfg.Checks = map[string]*common.IntegrityCheckConfig{
		"svm.final.finalizedBound": {OnFailure: "hardReject"},
	}
	net := setupSvmIntegrityNetwork(t, ctx, cfg, "mainnet-beta")

	req := common.NewNormalizedRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["Addr1111111111111111111111111111111111111", {"commitment":"finalized"}]}`))
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "must fail over to the upstream with consistent finality")
	require.NotNil(t, resp)
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), `"lamports":111`,
		"the served account must be the honest upstream's")
	assert.Contains(t, string(jrr.GetResultBytes()), `"slot":980`)
	assert.True(t, req.IntegrityCaught())
}

func svmItoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// TestSvmIntegrity_TokenProgramFailsOver exercises svm.auth.tokenProgram end
// to end: rpc1 answers getTokenAccountsByOwner with an entry owned by a bogus
// "token" program — synthesized token data. With the per-check hardReject
// override the engine rejects and the retry fails over to rpc2's honest list.
func TestSvmIntegrity_TokenProgramFailsOver(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()
	util.SetupMocksForSvmStatePoller("svm-integ-rpc1.localhost", 1000, 990)
	util.SetupMocksForSvmStatePoller("svm-integ-rpc2.localhost", 1000, 990)

	// Honest 165-byte base SPL token account (state=initialized).
	honest := make([]byte, 165)
	honest[108] = 1
	// Dishonest: same shape but under a fake program id.
	dishonest := make([]byte, 165)
	dishonest[108] = 1

	entry := func(owner string, data []byte) string {
		return `{"pubkey":"Addr1111111111111111111111111111111111111","account":{"owner":"` + owner + `","lamports":100,"executable":false,"rentEpoch":2,"data":["` + base64.StdEncoding.EncodeToString(data) + `","base64"]}}`
	}
	byOwner := func(entries string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":990},"value":[` + entries + `]}}`
	}
	mockByOwner := func(host, body string) {
		gock.New("http://" + host).
			Post("").Times(1).
			Filter(func(r *http.Request) bool {
				return r.URL.Host == host && strings.Contains(util.SafeReadBody(r), `"method":"getTokenAccountsByOwner"`)
			}).
			Reply(200).BodyString(body)
	}
	mockByOwner("svm-integ-rpc1.localhost",
		byOwner(entry("FakeTokenProgram111111111111111111111111111", dishonest)))
	mockByOwner("svm-integ-rpc2.localhost",
		byOwner(entry("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", honest)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := svmCorroboratedIntegrity()
	cfg.Checks = map[string]*common.IntegrityCheckConfig{
		"svm.auth.tokenProgram": {OnFailure: "hardReject"},
	}
	net := setupSvmIntegrityNetwork(t, ctx, cfg, "mainnet-beta")

	req := common.NewNormalizedRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"getTokenAccountsByOwner","params":["Owner1111111111111111111111111111111111111", {"encoding":"base64"}]}`))
	resp, err := svmProjectForward(ctx, net, req)
	require.NoError(t, err, "must fail over to the upstream with token-program-owned accounts")
	require.NotNil(t, resp)
	jrr, err := resp.JsonRpcResponse(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	assert.NotContains(t, string(jrr.GetResultBytes()), "FakeTokenProgram")
	assert.True(t, req.IntegrityCaught())
}
