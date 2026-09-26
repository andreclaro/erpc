package integrity

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutU16_ShortvecBoundaries(t *testing.T) {
	cases := []struct {
		v    int
		want []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7f}},       // single-byte max
		{128, []byte{0x80, 0x01}}, // two-byte min
		{300, []byte{0xac, 0x02}},
		{16383, []byte{0xff, 0x7f}},       // two-byte max
		{16384, []byte{0x80, 0x80, 0x01}}, // three-byte min
	}
	for _, c := range cases {
		assert.Equal(t, c.want, putU16(nil, c.v), "shortvec(%d)", c.v)
	}
}

func TestMessageWireBytes_V0PrefixIsSigned(t *testing.T) {
	key := base58Encode(make([]byte, 32))
	hash := base58Encode(make([]byte, 32))

	legacy := parsedMessage{Version: 0, NumRequiredSignatures: 1, AccountKeys: []string{key}, RecentBlockhash: hash}
	buf, err := messageWireBytes(&legacy)
	require.NoError(t, err)
	assert.Equal(t, byte(1), buf[0], "legacy wire starts with the header byte — no version prefix")

	v0 := parsedMessage{Version: 1, NumRequiredSignatures: 1, AccountKeys: []string{key}, RecentBlockhash: hash}
	buf, err = messageWireBytes(&v0)
	require.NoError(t, err)
	assert.Equal(t, byte(0x80), buf[0], "v0 signed payload MUST include the 0x80 prefix")
}

func TestMessageWireBytes_RejectsUnknownVersion(t *testing.T) {
	m := parsedMessage{Version: 2}
	_, err := messageWireBytes(&m)
	assert.Error(t, err)

	_, err = messageWireBytes(nil)
	assert.Error(t, err)
}

func TestMessageWireBytes_ReconstructsLayout(t *testing.T) {
	// Round-trip: parse a crafted wire message, re-serialize, and compare
	// against the bytes putU16 + fixed layout produce.
	key := base58Encode(bytes.Repeat([]byte{7}, 32))
	hash := base58Encode(bytes.Repeat([]byte{9}, 32))
	m := parsedMessage{
		Version:               1,
		NumRequiredSignatures: 2,
		NumReadonlySigned:     1,
		NumReadonlyUnsigned:   3,
		AccountKeys:           []string{key, key},
		RecentBlockhash:       hash,
		Instructions:          []parsedInstr{{ProgramIDIndex: 4, Accounts: []byte{0, 1}, Data: []byte{0xde, 0xad}}},
		Lookups:               []parsedLookup{{AccountKey: key, WritableIndexes: []byte{5}, ReadonlyIndexes: []byte{6}}},
	}
	got, err := messageWireBytes(&m)
	require.NoError(t, err)

	var want bytes.Buffer
	want.WriteByte(0x80)
	want.Write([]byte{2, 1, 3})
	want.Write(putU16(nil, 2))
	want.Write(bytes.Repeat([]byte{7}, 32))
	want.Write(bytes.Repeat([]byte{7}, 32))
	want.Write(bytes.Repeat([]byte{9}, 32))
	want.Write(putU16(nil, 1))
	want.WriteByte(4)
	want.Write(putU16(nil, 2))
	want.Write([]byte{0, 1})
	want.Write(putU16(nil, 2))
	want.Write([]byte{0xde, 0xad})
	want.Write(putU16(nil, 1))
	want.Write(bytes.Repeat([]byte{7}, 32))
	want.Write(putU16(nil, 1))
	want.WriteByte(5)
	want.Write(putU16(nil, 1))
	want.WriteByte(6)

	assert.Equal(t, want.Bytes(), got)
}

func TestVerifyTxSignatures_Guards(t *testing.T) {
	assert.True(t, errors.Is(verifyTxSignatures(nil), errNotVerifiable))

	noMsg := &parsedTx{HasMessage: false}
	assert.True(t, errors.Is(verifyTxSignatures(noMsg), errNotVerifiable))

	zeroSigs := &parsedTx{HasMessage: true, Message: parsedMessage{NumRequiredSignatures: 0}}
	assert.Error(t, verifyTxSignatures(zeroSigs))

	countMismatch := &parsedTx{
		HasMessage: true,
		Signatures: []string{"a", "b"},
		Message:    parsedMessage{NumRequiredSignatures: 1, AccountKeys: []string{"k"}},
	}
	assert.Error(t, verifyTxSignatures(countMismatch), "signature count must equal numRequiredSignatures")
}
