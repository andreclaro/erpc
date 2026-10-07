package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJsonRpcNetworkConfig_Validate(t *testing.T) {
	t.Run("valid slug passes", func(t *testing.T) {
		require.NoError(t, (&JsonRpcNetworkConfig{Slug: "starknet"}).Validate())
		require.NoError(t, (&JsonRpcNetworkConfig{Slug: "stellar-mainnet"}).Validate())
		require.NoError(t, (&JsonRpcNetworkConfig{Slug: "near_testnet_2"}).Validate())
	})

	t.Run("missing slug rejected", func(t *testing.T) {
		err := (&JsonRpcNetworkConfig{}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network.*.jsonRpc.slug")
	})

	// The slug is a single segment of the network id (jsonrpc:<slug>) and of
	// the URL path (/<project>/jsonrpc/<slug>); anything that would forge an
	// extra segment is a hard error.
	for _, slug := range []string{"stark net", "stark.net", "stark:net", "stark/net", ""} {
		t.Run("malformed slug rejected: "+slug, func(t *testing.T) {
			err := (&JsonRpcNetworkConfig{Slug: slug}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "network.*.jsonRpc.slug")
		})
	}
}

func TestNetworkConfig_Validate_JsonRpc(t *testing.T) {
	jn := func() *NetworkConfig {
		return &NetworkConfig{
			Architecture: ArchitectureJsonRpc,
			JsonRpc:      &JsonRpcNetworkConfig{Slug: "starknet"},
		}
	}

	t.Run("valid jsonrpc network passes", func(t *testing.T) {
		require.NoError(t, jn().Validate(&Config{}))
	})

	t.Run("jsonrpc architecture without jsonRpc block rejected", func(t *testing.T) {
		n := jn()
		n.JsonRpc = nil
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network.*.jsonRpc is required")
	})

	t.Run("evm block on jsonrpc network rejected", func(t *testing.T) {
		n := jn()
		n.Evm = &EvmNetworkConfig{}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evm/svm blocks are not allowed")
	})

	t.Run("svm block on jsonrpc network rejected", func(t *testing.T) {
		n := jn()
		n.Svm = &SvmNetworkConfig{Cluster: "mainnet-beta"}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evm/svm blocks are not allowed")
	})

	t.Run("jsonRpc block on evm network rejected", func(t *testing.T) {
		n := &NetworkConfig{
			Architecture: ArchitectureEvm,
			Evm:          &EvmNetworkConfig{ChainId: 1},
			JsonRpc:      &JsonRpcNetworkConfig{Slug: "starknet"},
		}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jsonRpc block is only allowed on jsonrpc networks")
	})

	t.Run("failsafe consensus on jsonrpc network rejected", func(t *testing.T) {
		n := jn()
		n.Failsafe = []*FailsafeConfig{{
			MatchMethod: "*",
			Consensus:   &ConsensusPolicyConfig{MaxParticipants: 2, AgreementThreshold: 1},
		}}
		err := n.Validate(&Config{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failsafe.consensus is not supported")
	})

	// Transport-grade policies stay fully available — the architecture strips
	// protocol machinery only.
	t.Run("failsafe retry on jsonrpc network passes", func(t *testing.T) {
		n := jn()
		n.Failsafe = []*FailsafeConfig{{
			MatchMethod: "*",
			Retry: &RetryPolicyConfig{
				MaxAttempts:     2,
				BackoffFactor:   1.5,
				BackoffMaxDelay: Duration(10 * time.Second),
			},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})

	t.Run("failsafe circuitBreaker on jsonrpc network passes", func(t *testing.T) {
		n := jn()
		n.Failsafe = []*FailsafeConfig{{
			MatchMethod: "*",
			CircuitBreaker: &CircuitBreakerPolicyConfig{
				FailureThresholdCount:    2,
				FailureThresholdCapacity: 5,
				HalfOpenAfter:            Duration(30 * time.Second),
				SuccessThresholdCount:    1,
				SuccessThresholdCapacity: 3,
			},
		}}
		require.NoError(t, n.Validate(&Config{}))
	})
}

func TestUpstreamConfig_Validate_JsonRpc(t *testing.T) {
	ju := func() *UpstreamConfig {
		return &UpstreamConfig{
			Id:       "u1",
			Type:     UpstreamTypeJsonRpc,
			Endpoint: "https://starknet.example.io",
			JsonRpc:  &JsonRpcUpstreamConfig{Slug: "starknet"},
		}
	}

	t.Run("valid jsonrpc upstream passes", func(t *testing.T) {
		require.NoError(t, ju().Validate(&Config{}, false))
	})

	t.Run("jsonrpc upstream without jsonRpc block rejected", func(t *testing.T) {
		u := ju()
		u.JsonRpc = nil
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jsonRpc.slug is required")
	})

	t.Run("jsonrpc upstream with empty slug rejected", func(t *testing.T) {
		u := ju()
		u.JsonRpc.Slug = ""
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jsonRpc.slug is required")
	})

	t.Run("evm block on jsonrpc upstream rejected", func(t *testing.T) {
		u := ju()
		u.Evm = &EvmUpstreamConfig{ChainId: 1}
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evm/svm blocks are not allowed")
	})

	t.Run("svm block on jsonrpc upstream rejected", func(t *testing.T) {
		u := ju()
		u.Svm = &SvmUpstreamConfig{Cluster: "mainnet-beta"}
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evm/svm blocks are not allowed")
	})

	t.Run("malformed slug on jsonrpc upstream rejected", func(t *testing.T) {
		u := ju()
		u.JsonRpc.Slug = "stark net"
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jsonRpc.slug")
	})

	t.Run("failsafe consensus on jsonrpc upstream rejected", func(t *testing.T) {
		u := ju()
		u.Failsafe = []*FailsafeConfig{{
			MatchMethod: "*",
			Consensus:   &ConsensusPolicyConfig{MaxParticipants: 2, AgreementThreshold: 1},
		}}
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failsafe.consensus is not supported")
	})

	// The "forgot to set type: jsonrpc" misconfiguration: a slug on an
	// evm-typed upstream would silently match no network.
	t.Run("jsonRpc.slug on evm-typed upstream rejected", func(t *testing.T) {
		u := &UpstreamConfig{
			Id:       "e1",
			Type:     UpstreamTypeEvm,
			Endpoint: "https://eth.example.io",
			JsonRpc:  &JsonRpcUpstreamConfig{Slug: "starknet"},
		}
		err := u.Validate(&Config{}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only allowed when type is 'jsonrpc'")
	})

	t.Run("jsonRpc block without slug on evm upstream passes", func(t *testing.T) {
		u := &UpstreamConfig{
			Id:       "e1",
			Type:     UpstreamTypeEvm,
			Endpoint: "https://eth.example.io",
			JsonRpc:  &JsonRpcUpstreamConfig{EnableGzip: &[]bool{true}[0]},
		}
		require.NoError(t, u.Validate(&Config{}, false))
	})
}

func TestValidateJsonRpcUpstreamNetworkPairing(t *testing.T) {
	ups := func(id, slug string) *UpstreamConfig {
		return &UpstreamConfig{Id: id, Type: UpstreamTypeJsonRpc, JsonRpc: &JsonRpcUpstreamConfig{Slug: slug}}
	}
	ntw := func(slug string) *NetworkConfig {
		return &NetworkConfig{Architecture: ArchitectureJsonRpc, JsonRpc: &JsonRpcNetworkConfig{Slug: slug}}
	}

	t.Run("matching slug passes", func(t *testing.T) {
		require.NoError(t, validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{ups("u1", "starknet")},
			[]*NetworkConfig{ntw("starknet")},
		))
	})

	// Lazy network creation: no jsonrpc network is declared at all, so the
	// upstream's slug may create one at bootstrap — stay quiet (svm precedent).
	t.Run("no declared jsonrpc networks is not an error", func(t *testing.T) {
		require.NoError(t, validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{ups("u1", "starknet")},
			[]*NetworkConfig{{Architecture: ArchitectureEvm, Evm: &EvmNetworkConfig{ChainId: 1}}},
		))
		require.NoError(t, validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{ups("u1", "starknet")},
			nil,
		))
	})

	// Once at least one jsonrpc network IS declared, an unmatched upstream
	// slug would bootstrap healthy and serve nothing — catch it at startup.
	t.Run("unmatched slug rejected when jsonrpc networks are declared", func(t *testing.T) {
		err := validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{ups("u1", "starknet")},
			[]*NetworkConfig{ntw("near")},
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no jsonrpc network declares that slug")
	})

	t.Run("passes when some network matches the slug", func(t *testing.T) {
		require.NoError(t, validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{ups("u1", "starknet")},
			[]*NetworkConfig{ntw("near"), ntw("starknet")},
		))
	})

	t.Run("non-jsonrpc upstreams ignored", func(t *testing.T) {
		require.NoError(t, validateJsonRpcUpstreamNetworkPairing(
			[]*UpstreamConfig{{Id: "e1", Type: UpstreamTypeEvm, Evm: &EvmUpstreamConfig{ChainId: 1}}},
			[]*NetworkConfig{ntw("starknet")},
		))
	})
}

// NetworkId is derived from the slug exactly once; a drift between these two
// would break upstream selection silently.
func TestNetworkConfig_NetworkId_JsonRpc(t *testing.T) {
	n := &NetworkConfig{Architecture: ArchitectureJsonRpc, JsonRpc: &JsonRpcNetworkConfig{Slug: "starknet"}}
	require.Equal(t, "jsonrpc:starknet", n.NetworkId())
	require.True(t, IsValidNetwork(n.NetworkId()))
}
