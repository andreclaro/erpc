package integrity

// base58 encoding/decoding for the Solana alphabet (Bitcoin/IPFS flavor, no 0OIl).
// Solana hashes, pubkeys, signatures and base58-encoded tx fields all use it.

import (
	"errors"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Indexes = func() [128]int8 {
	idx := [128]int8{}
	for i := range idx {
		idx[i] = -1
	}
	for i := 0; i < len(base58Alphabet); i++ {
		idx[base58Alphabet[i]] = int8(i)
	}
	return idx
}()

var big58 = big.NewInt(58)

// base58Decode decodes a base58 string into bytes. An empty string decodes to
// an empty slice (callers that require an exact length still fail their length
// check, so empty is never silently accepted as a valid hash).
func base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty base58 string")
	}
	// Count leading '1's — they encode leading zero bytes.
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	n := new(big.Int)
	for i := zeros; i < len(s); i++ {
		c := s[i]
		if c >= 128 || base58Indexes[c] < 0 {
			return nil, errors.New("invalid base58 character")
		}
		n.Mul(n, big58)
		n.Add(n, big.NewInt(int64(base58Indexes[c])))
	}
	body := n.Bytes()
	out := make([]byte, zeros+len(body))
	copy(out[zeros:], body)
	return out, nil
}

// base58DecodeLen decodes and additionally asserts the decoded length — the
// common case for blockhashes/pubkeys (32) and signatures (64). A wrong length
// is reported as a decode error so checks treat it as malformed, not valid.
func base58DecodeLen(s string, want int) ([]byte, error) {
	b, err := base58Decode(s)
	if err != nil {
		return nil, err
	}
	if len(b) != want {
		return nil, errors.New("unexpected decoded length")
	}
	return b, nil
}

// base58Encode encodes bytes into the Solana base58 alphabet. Used by tests to
// craft fixtures, and harmless to keep in the package for diagnostics.
func base58Encode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b[zeros:])
	var rev []byte
	for n.Sign() > 0 {
		mod := new(big.Int)
		n.DivMod(n, big58, mod)
		rev = append(rev, base58Alphabet[mod.Int64()])
	}
	out := make([]byte, zeros+len(rev))
	for i := 0; i < len(rev); i++ {
		out[zeros+len(rev)-1-i] = rev[i]
	}
	// Leading zeros map to '1'.
	for i := 0; i < zeros; i++ {
		out[i] = '1'
	}
	return string(out)
}
