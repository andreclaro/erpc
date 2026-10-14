package common

import (
	"strings"
	"testing"

	"github.com/erpc/erpc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { util.ConfigureTestLogger() }

func jsonRpcNetwork() *NetworkConfig {
	return &NetworkConfig{
		Architecture: ArchitectureJsonRpc,
		Jsonrpc:      &JsonrpcNetworkConfig{NetworkId: "starknet"},
	}
}

func TestJsonrpcNetworkConfig_Validate(t *testing.T) {
	valid := func() *JsonrpcNetworkConfig {
		return &JsonrpcNetworkConfig{NetworkId: "starknet"}
	}

	t.Run("minimal config is valid", func(t *testing.T) {
		require.NoError(t, valid().Validate())
	})

	t.Run("empty networkId rejected", func(t *testing.T) {
		err := (&JsonrpcNetworkConfig{}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "networkId is required")
	})

	for _, bad := range []string{"has space", "has:colon", "sla/sh", ""} {
		t.Run("networkId charset rejects "+bad, func(t *testing.T) {
			j := valid()
			j.NetworkId = bad
			err := j.Validate()
			require.Error(t, err, "networkId %q", bad)
		})
	}

	for _, good := range []string{"a", "A-1_b.c", "polkadot-asset-hub", "v0_8"} {
		t.Run("networkId charset accepts "+good, func(t *testing.T) {
			j := valid()
			j.NetworkId = good
			require.NoError(t, j.Validate())
		})
	}

	t.Run("statePoller without tip rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "statePoller.tip is required")
	})

	t.Run("tip probe without method rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Path: "$.result"}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tip.method is required")
	})

	t.Run("tip probe without path rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "status"}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tip.path is required")
	})

	t.Run("bad encoding rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "status", Path: "$.result", Encoding: "octal"}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encoding")
	})

	t.Run("all encodings accepted", func(t *testing.T) {
		for _, enc := range []string{"", "auto", "number", "string", "hex"} {
			j := valid()
			j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "status", Path: "$.result", Encoding: enc}}
			require.NoError(t, j.Validate(), "encoding %q", enc)
		}
	})

	t.Run("thenCall without passValueAsParam rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{
			Method:   "chain_getFinalizedHead",
			Path:     "$.result",
			ThenCall: &JsonRpcThenCallConfig{Method: "chain_getHeader", Path: "$.result.number"},
		}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "passValueAsParam")
	})

	t.Run("thenCall with passValueAsParam accepted", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{
			Tip: &JsonRpcStateProbeConfig{Method: "chain_getHeader", Path: "$.result.number", Encoding: "hex"},
			FinalizedTip: &JsonRpcStateProbeConfig{
				Method:   "chain_getFinalizedHead",
				Path:     "$.result",
				ThenCall: &JsonRpcThenCallConfig{Method: "chain_getHeader", PassValueAsParam: true, Path: "$.result.number", Encoding: "hex"},
			},
		}
		require.NoError(t, j.Validate())
	})

	t.Run("unhealthyWhen without path rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{
			Method: "status", Path: "$.result",
			UnhealthyWhen: &JsonRpcValueMatcherConfig{Equals: true},
		}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unhealthyWhen.path")
	})

	t.Run("unhealthyWhen without equals/in rejected", func(t *testing.T) {
		j := valid()
		j.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{
			Method: "status", Path: "$.result",
			UnhealthyWhen: &JsonRpcValueMatcherConfig{Path: "$.result.syncing"},
		}}
		err := j.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "equals or in")
	})

	t.Run("errorRules shapes", func(t *testing.T) {
		cases := []struct {
			name    string
			rule    *JsonRpcClassificationRuleConfig
			wantErr string
		}{
			{"valid match equals", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", Equals: "error"}, ClassifyAs: "serverError"}, ""},
			{"valid xrpl two-stage", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", Equals: "error"}, CodePath: "$.result.error", CodeMap: map[string]string{"txnNotFound": "missingData"}, Default: "serverError"}, ""},
			{"valid errorPath in", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ErrorPath: "$.error.cause.name", In: []interface{}{"UNKNOWN_BLOCK"}}, ClassifyAs: "missingData"}, ""},
			{"valid isNull", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "$.result.block", IsNull: true}, ClassifyAs: "missingData"}, ""},
			{"nil match", &JsonRpcClassificationRuleConfig{ClassifyAs: "missingData"}, "match is required"},
			{"both paths", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", ErrorPath: "b", Equals: "x"}, ClassifyAs: "missingData"}, "exactly one of"},
			{"no matcher", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a"}, ClassifyAs: "missingData"}, "equals, in, or isNull"},
			{"isNull combined", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", IsNull: true, Equals: "x"}, ClassifyAs: "missingData"}, "isNull cannot be combined"},
			{"unknown classifyAs", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", Equals: "x"}, ClassifyAs: "banana"}, "not a valid class"},
			{"codeMap without codePath", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", Equals: "x"}, CodeMap: map[string]string{"a": "missingData"}}, "codeMap requires codePath"},
			{"missing classifyAs", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", Equals: "x"}}, "classifyAs is required"},
			{"bad default", &JsonRpcClassificationRuleConfig{Match: &JsonRpcClassificationMatchConfig{ResultPath: "a", Equals: "x"}, CodePath: "b", CodeMap: map[string]string{"a": "missingData"}, Default: "banana"}, "not a valid class"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				j := valid()
				j.ErrorRules = []*JsonRpcClassificationRuleConfig{tc.rule}
				err := j.Validate()
				if tc.wantErr == "" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tc.wantErr)
				}
			})
		}
	})
}

func TestNetworkConfig_JsonRpcValidation(t *testing.T) {
	t.Run("jsonrpc architecture requires jsonrpc block", func(t *testing.T) {
		n := &NetworkConfig{Architecture: ArchitectureJsonRpc}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jsonrpc is required")
	})

	t.Run("evm block on jsonrpc network rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Evm = &EvmNetworkConfig{ChainId: 1}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evm block is not allowed")
	})

	t.Run("svm block on jsonrpc network rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Svm = &SvmNetworkConfig{Cluster: "mainnet-beta"}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "svm block is not allowed")
	})

	t.Run("jsonrpc block on evm network rejected", func(t *testing.T) {
		n := &NetworkConfig{Architecture: "evm", Evm: &EvmNetworkConfig{ChainId: 1}, Jsonrpc: &JsonrpcNetworkConfig{NetworkId: "x"}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only allowed on jsonrpc networks")
	})

	t.Run("conflicting finality flags rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {Finalized: true, Realtime: true},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("finalityFromParam with static flag rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		idx := 0
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {Finalized: true, FinalityFromParam: &FinalityFromParamConfig{Index: &idx, Map: map[string]string{"latest": "realtime"}, Default: "realtime"}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("finalityFromParam key and index both set rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		idx := 0
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {FinalityFromParam: &FinalityFromParamConfig{Key: "finality", Index: &idx, Map: map[string]string{"final": "finalized"}, Default: "realtime"}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one of key or index")
	})

	t.Run("finalityFromParam neither key nor index rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {FinalityFromParam: &FinalityFromParamConfig{Map: map[string]string{"final": "finalized"}, Default: "realtime"}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one of key or index")
	})

	t.Run("finalityFromParam empty map rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {FinalityFromParam: &FinalityFromParamConfig{Key: "finality", Map: map[string]string{}, Default: "realtime"}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "map is required")
	})

	t.Run("finalityFromParam bad map value rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {FinalityFromParam: &FinalityFromParamConfig{Key: "finality", Map: map[string]string{"final": "eventually"}, Default: "realtime"}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a valid finality")
	})

	t.Run("finalityFromParam missing default rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getThing": {FinalityFromParam: &FinalityFromParamConfig{Key: "finality", Map: map[string]string{"final": "finalized"}}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "default is required")
	})

	t.Run("valid NEAR-style declaration accepted", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"block": {FinalityFromParam: &FinalityFromParamConfig{Key: "finality", Map: map[string]string{"final": "finalized", "near-final": "unfinalized", "optimistic": "realtime"}, Default: "realtime"}},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})

	t.Run("cacheTipBucket without statePoller rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 1},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires jsonrpc.statePoller")
	})

	t.Run("cacheTipBucket with statePoller accepted", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Jsonrpc.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "getLatestLedger", Path: "$.result.sequence"}}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 1},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})

	t.Run("negative cacheTipBucket rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Jsonrpc.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "m", Path: "$.result"}}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"m": {Realtime: true, CacheTipBucket: -1},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), ">= 1")
	})

	t.Run("resultClassifications with codeMap rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getTransaction": {ResultClassifications: []*JsonRpcClassificationRuleConfig{{
				Match:    &JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", Equals: "NOT_FOUND"},
				CodePath: "$.result.error", CodeMap: map[string]string{"x": "missingData"},
			}}},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only support match and classifyAs")
	})

	t.Run("resultClassifications match+classifyAs accepted", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"getTransaction": {Finalized: true, ResultClassifications: []*JsonRpcClassificationRuleConfig{{
				Match:      &JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", In: []interface{}{"NOT_FOUND"}},
				ClassifyAs: "missingData",
			}}},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})
}

func TestNetworkConfig_JsonRpcConsensusGating(t *testing.T) {
	consensusFs := func(match string) *FailsafeConfig {
		return &FailsafeConfig{
			MatchMethod: match,
			Consensus:   &ConsensusPolicyConfig{MaxParticipants: 2},
		}
	}

	t.Run("consensus with no declarations rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{consensusFs("starknet_getBlock*")}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match any method declaration")
	})

	t.Run("consensus with wildcard rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{consensusFs("*")}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_getBlockWithTxs": {Finalized: true},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must scope matchMethod")
	})

	t.Run("consensus matching undeclared method rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{consensusFs("starknet_getBlock*")}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_getBlockWithTxs": {Finalized: true},
			"starknet_getBlockTxCount": {},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no finality declaration")
	})

	t.Run("consensus on finalized declaration accepted", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{consensusFs("starknet_getTransactionReceipt")}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_getTransactionReceipt": {Finalized: true},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})

	t.Run("consensus on realtime without poller rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{consensusFs("starknet_blockNumber")}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_blockNumber": {Realtime: true},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "statePoller is not configured")
	})

	t.Run("consensus on realtime with poller accepted", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Jsonrpc.StatePoller = &JsonRpcStatePollerConfig{Tip: &JsonRpcStateProbeConfig{Method: "starknet_blockNumber", Path: "$.result"}}
		n.Failsafe = []*FailsafeConfig{consensusFs("starknet_blockNumber")}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_blockNumber": {Realtime: true},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})

	t.Run("preferBlockHeadLeader rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{{
			MatchMethod: "starknet_getTransactionReceipt",
			Consensus:   &ConsensusPolicyConfig{MaxParticipants: 2, DisputeBehavior: ConsensusDisputeBehaviorPreferBlockHeadLeader},
		}}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_getTransactionReceipt": {Finalized: true},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "leader election is evm-only")
	})

	t.Run("onlyBlockHeadLeader rejected", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Failsafe = []*FailsafeConfig{{
			MatchMethod: "starknet_getTransactionReceipt",
			Consensus:   &ConsensusPolicyConfig{MaxParticipants: 2, LowParticipantsBehavior: ConsensusLowParticipantsBehaviorOnlyBlockHeadLeader},
		}}
		n.Methods = &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
			"starknet_getTransactionReceipt": {Finalized: true},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "leader election is evm-only")
	})
}

func TestJsonRpcUpstreamNetworkPairing(t *testing.T) {
	project := func(upstreams []*UpstreamConfig, networks []*NetworkConfig) *ProjectConfig {
		return &ProjectConfig{Id: "main", Upstreams: upstreams, Networks: networks}
	}
	jrUpstream := func(id, slug string) *UpstreamConfig {
		return &UpstreamConfig{Id: id, Type: UpstreamTypeJsonRpc, Jsonrpc: &JsonrpcUpstreamConfig{NetworkId: slug}, Endpoint: "https://x.example/rpc"}
	}

	t.Run("typed upstream with matching slug accepted", func(t *testing.T) {
		p := project([]*UpstreamConfig{jrUpstream("a", "starknet")}, []*NetworkConfig{jsonRpcNetwork()})
		require.NoError(t, p.Validate(&Config{}))
	})

	t.Run("typed upstream with unknown slug rejected", func(t *testing.T) {
		p := project([]*UpstreamConfig{jrUpstream("a", "solana")}, []*NetworkConfig{jsonRpcNetwork()})
		err := p.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "matches no declared jsonrpc network")
	})

	t.Run("typed upstream without slug rejected", func(t *testing.T) {
		p := project([]*UpstreamConfig{{Id: "a", Type: UpstreamTypeJsonRpc, Endpoint: "https://x.example/rpc"}}, []*NetworkConfig{jsonRpcNetwork()})
		err := p.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "matches no declared jsonrpc network")
	})

	t.Run("inherited jsonrpc block on evm upstream is inert", func(t *testing.T) {
		p := project([]*UpstreamConfig{{Id: "a", Type: UpstreamTypeEvm, Jsonrpc: &JsonrpcUpstreamConfig{NetworkId: "starknet"}, Endpoint: "https://x.example/rpc"}}, []*NetworkConfig{jsonRpcNetwork()})
		require.NoError(t, p.Validate(&Config{}))
	})

	t.Run("no jsonrpc networks means no pairing checks", func(t *testing.T) {
		p := project([]*UpstreamConfig{jrUpstream("a", "anything")}, nil)
		require.NoError(t, p.Validate(&Config{}))
	})

	t.Run("duplicate networkId rejected", func(t *testing.T) {
		p := project([]*UpstreamConfig{jrUpstream("a", "starknet")}, []*NetworkConfig{jsonRpcNetwork(), jsonRpcNetwork()})
		err := p.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be unique")
	})
}

func TestJsonRpcNetworkId(t *testing.T) {
	t.Run("network id composition", func(t *testing.T) {
		n := jsonRpcNetwork()
		assert.Equal(t, "jsonrpc:starknet", n.NetworkId())
		assert.Equal(t, "jsonrpc:starknet", util.JsonRpcNetworkId("starknet"))
	})

	t.Run("IsValidNetwork", func(t *testing.T) {
		for id, want := range map[string]bool{
			"jsonrpc:starknet":           true,
			"jsonrpc:polkadot-asset-hub": true,
			"jsonrpc:v0_8.node":          true,
			"jsonrpc:":                   false,
			"jsonrpc:a b":                false,
			"jsonrpc:a:b":                false,
			"evm:1":                      true,
		} {
			assert.Equal(t, want, IsValidNetwork(id), "IsValidNetwork(%q)", id)
			assert.Equal(t, want, util.IsValidNetworkId(id), "IsValidNetworkId(%q)", id)
		}
	})

	t.Run("IsValidArchitecture", func(t *testing.T) {
		assert.True(t, IsValidArchitecture("jsonrpc"))
		assert.True(t, IsValidArchitecture("evm"))
		assert.False(t, IsValidArchitecture("rest"))
	})
}

func TestJsonRpcNetworkDefaults(t *testing.T) {
	t.Run("architecture derived from jsonrpc block", func(t *testing.T) {
		n := &NetworkConfig{Jsonrpc: &JsonrpcNetworkConfig{NetworkId: "svc"}}
		require.NoError(t, n.SetDefaults(nil, nil))
		assert.Equal(t, ArchitectureJsonRpc, n.Architecture)
		assert.Equal(t, "jsonrpc:svc", n.NetworkId())
	})

	t.Run("empty jsonrpc block materialized for explicit architecture", func(t *testing.T) {
		n := &NetworkConfig{Architecture: ArchitectureJsonRpc, Jsonrpc: &JsonrpcNetworkConfig{NetworkId: "svc"}}
		require.NoError(t, n.SetDefaults(nil, nil))
		assert.NotNil(t, n.Jsonrpc)
	})

	t.Run("evm networkDefaults not injected into jsonrpc network", func(t *testing.T) {
		n := &NetworkConfig{Jsonrpc: &JsonrpcNetworkConfig{NetworkId: "svc"}}
		defaults := &NetworkDefaults{Evm: &EvmNetworkConfig{ChainId: 5, FallbackFinalityDepth: 10}}
		require.NoError(t, n.SetDefaults(nil, defaults))
		assert.Nil(t, n.Evm)
		assert.Equal(t, ArchitectureJsonRpc, n.Architecture)
	})

	t.Run("jsonrpc network does not inherit evm default method tables", func(t *testing.T) {
		n := &NetworkConfig{Jsonrpc: &JsonrpcNetworkConfig{NetworkId: "svc"}}
		require.NoError(t, n.SetDefaults(nil, nil))
		require.NotNil(t, n.Methods)
		assert.Nil(t, n.Methods.FindMethodConfig("eth_getBlockByNumber"), "eth_* defaults must not leak onto jsonrpc networks")
		assert.Nil(t, n.Methods.FindMethodConfig("getBlock"), "solana defaults must not leak onto jsonrpc networks")
	})

	t.Run("operator definitions still indexed on jsonrpc networks", func(t *testing.T) {
		n := &NetworkConfig{
			Architecture: ArchitectureJsonRpc,
			Jsonrpc:      &JsonrpcNetworkConfig{NetworkId: "svc"},
			Methods: &MethodsConfig{Definitions: map[string]*CacheMethodConfig{
				"getThing": {Finalized: true},
			}},
		}
		require.NoError(t, n.SetDefaults(nil, nil))
		mc := n.Methods.FindMethodConfig("getThing")
		require.NotNil(t, mc)
		assert.True(t, mc.Finalized)
	})

	t.Run("database jsonRpcCache block validates", func(t *testing.T) {
		db := &DatabaseConfig{JsonRpcCache: &CacheConfig{
			Connectors: []*ConnectorConfig{{Id: "memory", Driver: DriverMemory, Memory: &MemoryConnectorConfig{MaxItems: 100}}},
			Policies:   []*CachePolicyConfig{{Connector: "memory", Network: "jsonrpc:svc", Method: "*", Finality: DataFinalityStateUnknown}},
		}}
		require.NoError(t, db.Validate())
	})

	t.Run("network id charset rejects newline junk", func(t *testing.T) {
		n := jsonRpcNetwork()
		n.Jsonrpc.NetworkId = "bad\nid"
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "networkId"))
	})
}
