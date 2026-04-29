package common

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type NetworkArchitecture string

const (
	ArchitectureEvm NetworkArchitecture = "evm"
	ArchitectureSvm NetworkArchitecture = "svm"
)

type Network interface {
	Id() string
	Label() string
	ProjectId() string
	Architecture() NetworkArchitecture
	Config() *NetworkConfig
	Logger() *zerolog.Logger
	GetMethodMetrics(method string) TrackedMetrics
	Forward(ctx context.Context, nq *NormalizedRequest) (*NormalizedResponse, error)
	GetFinality(ctx context.Context, req *NormalizedRequest, resp *NormalizedResponse) DataFinalityState

	// TODO: migrate these to an EvmNetwork sub-interface (mirroring SvmNetwork
	// below) so test mocks and non-EVM callers don't carry EVM-specific methods.
	EvmHighestLatestBlockNumber(ctx context.Context) int64
	EvmHighestFinalizedBlockNumber(ctx context.Context) int64
	EvmLeaderUpstream(ctx context.Context) Upstream
}

// SvmNetwork is the SVM-specific view of a Network. Callers that need slot
// accessors should type-assert: if svm, ok := n.(common.SvmNetwork); ok { ... }.
// Production Network implementations satisfy this automatically when the
// underlying network is SVM; EVM networks correctly fail the assertion.
type SvmNetwork interface {
	Network
	SvmHighestLatestSlot(ctx context.Context) int64
	SvmHighestFinalizedSlot(ctx context.Context) int64
}

func IsValidArchitecture(architecture string) bool {
	switch NetworkArchitecture(architecture) {
	case ArchitectureEvm, ArchitectureSvm:
		return true
	}
	return false
}

func IsValidNetwork(network string) bool {
	if strings.HasPrefix(network, "evm:") {
		chainId, err := strconv.ParseInt(strings.TrimPrefix(network, "evm:"), 10, 64)
		if err != nil {
			return false
		}
		return chainId > 0
	}
	if strings.HasPrefix(network, "svm:") {
		cluster := strings.TrimPrefix(network, "svm:")
		if cluster == "" {
			return false
		}
		// Accept known clusters outright; accept custom ones that look like an identifier
		// (e.g. "fogo-mainnet", "my-localnet") so users can run private clusters behind eRPC.
		// Bootstrap enforces the actual genesis hash when CheckGenesisHash is set.
		if IsValidSvmCluster(cluster) {
			return true
		}
		for _, r := range cluster {
			if !(r == '-' || r == '_' || r == '.' ||
				(r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9')) {
				return false
			}
		}
		return true
	}

	return false
}

type QuantileTracker interface {
	Add(value float64)
	GetQuantile(qtile float64) time.Duration
	Reset()
}

type TrackedMetrics interface {
	ErrorRate() float64
	GetResponseQuantiles() QuantileTracker
}
