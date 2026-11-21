package consensus

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConsensusKey(t *testing.T) {
	cases := []struct {
		in   string
		want ConsensusKey
	}{
		{"", ConsensusKeyCanonical},
		{"canonical", ConsensusKeyCanonical},
		{"Canonical", ConsensusKeyCanonical},
		{"blockhash", ConsensusKeyBlockhash},
		{"BLOCKHASH", ConsensusKeyBlockhash},
		{"blockhashWithLink", ConsensusKeyBlockhashWithLink},
		{"blockhashwithlink", ConsensusKeyBlockhashWithLink},
		{"unknown", ConsensusKeyCanonical},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseConsensusKey(tc.in))
		})
	}
}

func TestExtractSvmBlockhash(t *testing.T) {
	ctx := context.Background()
	key, err := extractSvmBlockhash(ctx, []byte(`{"blockhash":"abc123","transactions":[]}`))
	require.NoError(t, err)
	assert.Equal(t, "svm:abc123", key)

	_, err = extractSvmBlockhash(ctx, []byte(`{"transactions":[]}`))
	assert.ErrorIs(t, err, errMissingBlockhash)
}

func TestExtractSvmBlockhashWithLink(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{
		"blockhash": "abc123",
		"previousBlockhash": "def456",
		"parentSlot": 99,
		"blockHeight": 95
	}`)
	key, err := extractSvmBlockhashWithLink(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, "svm:abc123:def456:99:95", key)

	_, err = extractSvmBlockhashWithLink(ctx, []byte(`{"blockhash":"abc123"}`))
	assert.ErrorIs(t, err, errMissingLinkField)
}

func TestExtractSvmBlockhashWithLink_NullBlockHeight(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{
		"blockhash": "abc123",
		"previousBlockhash": "def456",
		"parentSlot": 99
	}`)
	key, err := extractSvmBlockhashWithLink(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, "svm:abc123:def456:99:null", key)
}

func TestExtractEvmBlockhash(t *testing.T) {
	ctx := context.Background()
	key, err := extractEvmBlockhash(ctx, []byte(`{"hash":"0xabc","number":"0x1"}`))
	require.NoError(t, err)
	assert.Equal(t, "evm:0xabc", key)

	_, err = extractEvmBlockhash(ctx, []byte(`{"number":"0x1"}`))
	assert.ErrorIs(t, err, errMissingBlockhash)
}

func TestExtractEvmBlockhashWithLink(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{
		"hash": "0xabc",
		"parentHash": "0xdef",
		"number": "0x64"
	}`)
	key, err := extractEvmBlockhashWithLink(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, "evm:0xabc:0xdef:0x64", key)

	_, err = extractEvmBlockhashWithLink(ctx, []byte(`{"hash":"0xabc"}`))
	assert.ErrorIs(t, err, errMissingLinkField)
}

func TestMethodKeyExtractor(t *testing.T) {
	assert.NotNil(t, methodKeyExtractor(ConsensusKeyBlockhash, "getBlock"))
	assert.NotNil(t, methodKeyExtractor(ConsensusKeyBlockhashWithLink, "eth_getBlockByNumber"))
	assert.NotNil(t, methodKeyExtractor(ConsensusKeyBlockhashWithLink, "GETBLOCK"))
	assert.Nil(t, methodKeyExtractor(ConsensusKeyCanonical, "getBlock"))
	assert.Nil(t, methodKeyExtractor(ConsensusKeyBlockhash, "unknownMethod"))
}
