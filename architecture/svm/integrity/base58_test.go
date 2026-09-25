package integrity

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBase58_KnownVectors(t *testing.T) {
	// Standard Bitcoin/IPFS-alphabet vectors (Solana shares the alphabet).
	vectors := []struct {
		raw string
		enc string
	}{
		{"Hello World!", "2NEpo7TZRRrLZSi2U"},
		{"hello world", "StV1DL6CwTryKyV"},
		{"a", "2g"},
		{"bbb", "a3gV"},
	}
	for _, v := range vectors {
		got := base58Encode([]byte(v.raw))
		assert.Equal(t, v.enc, got, "encode %q", v.raw)
		dec, err := base58Decode(v.enc)
		require.NoError(t, err, "decode %q", v.enc)
		assert.Equal(t, v.raw, string(dec), "round-trip %q", v.enc)
	}
}

func TestBase58_LeadingZeroBytes(t *testing.T) {
	// Leading 0x00 bytes map to leading '1's — the property that makes
	// all-zeros hashes/sigs encode as runs of '1'.
	zeros32 := make([]byte, 32)
	enc := base58Encode(zeros32)
	assert.Equal(t, "11111111111111111111111111111111", enc)

	dec, err := base58Decode(enc)
	require.NoError(t, err)
	assert.Equal(t, zeros32, dec)

	// Mixed: two leading zeros then payload.
	mixed := append([]byte{0, 0}, []byte{0x61, 0x62}...) // "ab"
	enc = base58Encode(mixed)
	assert.Equal(t, byte('1'), enc[0])
	assert.Equal(t, byte('1'), enc[1])
	dec, err = base58Decode(enc)
	require.NoError(t, err)
	assert.Equal(t, mixed, dec)
}

func TestBase58_RoundTripRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, n := range []int{1, 31, 32, 64, 100, 256} {
		b := make([]byte, n)
		rng.Read(b)
		dec, err := base58Decode(base58Encode(b))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(b, dec), "round-trip %d bytes", n)
	}
}

func TestBase58_DecodeErrors(t *testing.T) {
	// Forbidden alphabet characters (0, O, I, l) must fail, not skip.
	for _, s := range []string{"0", "O", "I", "l", "abc0def", "123O"} {
		_, err := base58Decode(s)
		assert.Error(t, err, "input %q", s)
	}
	// Empty is an error at the decode layer; length checks live in decodeLen.
	_, err := base58Decode("")
	assert.Error(t, err)
}

func TestBase58_DecodeLen(t *testing.T) {
	h32 := make([]byte, 32)
	b, err := base58DecodeLen(base58Encode(h32), 32)
	require.NoError(t, err)
	assert.Equal(t, h32, b)

	// Wrong length is a decode error so checks treat it as malformed.
	_, err = base58DecodeLen(base58Encode(h32), 64)
	assert.Error(t, err)

	// Empty string must not be silently accepted as a valid hash.
	_, err = base58DecodeLen("", 32)
	assert.Error(t, err)
}
