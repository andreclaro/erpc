package common

import "testing"

func TestIsValidArchitecture_JsonRpc(t *testing.T) {
	cases := []struct {
		arch string
		want bool
	}{
		{"evm", true},
		{"svm", true},
		{"jsonrpc", true},
		{"JSONRPC", false}, // case-sensitive, matches existing behavior
		{"JsonRpc", false},
		{"", false},
		{"starknet", false},
	}
	for _, tc := range cases {
		if got := IsValidArchitecture(tc.arch); got != tc.want {
			t.Errorf("IsValidArchitecture(%q) = %v, want %v", tc.arch, got, tc.want)
		}
	}
}

func TestIsValidNetwork_JsonRpc(t *testing.T) {
	cases := []struct {
		network string
		want    bool
	}{
		// Valid jsonrpc network IDs.
		{"jsonrpc:starknet", true},
		{"jsonrpc:stellar-mainnet", true},
		{"jsonrpc:near_testnet", true},
		{"jsonrpc:myChain123", true},

		// Rejections.
		{"jsonrpc:", false},          // empty slug
		{"jsonrpc:stark net", false}, // space
		{"jsonrpc:stark/net", false}, // slash
		{"jsonrpc:stark.net", false}, // dot — must survive as a single URL path segment
		{"jsonrpc:stark:net", false}, // extra colon segment

		// Other architectures still work.
		{"evm:1", true},
		{"svm:mainnet-beta", true},
		{"", false},
		{"unknown:foo", false},
	}
	for _, tc := range cases {
		if got := IsValidNetwork(tc.network); got != tc.want {
			t.Errorf("IsValidNetwork(%q) = %v, want %v", tc.network, got, tc.want)
		}
	}
}
