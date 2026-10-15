package integrity

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// Wire-format reconstruction and ed25519 verification for Solana transactions.
//
// The spec's pillar-1 substitute for EVM's tx/receipt Merkle membership checks:
// every transaction's signatures are verified against the exact wire message
// bytes (the signed payload), recomputed from the RPC JSON encoding. The
// compact-u16 (shortvec) lengths and the v0 version prefix (0x80) follow the
// solana wire spec; the signed payload for v0 messages INCLUDES the 0x80
// prefix byte.
//
// Note on key types: Solana transaction-level signers are ed25519 keys; the
// secp256k1 program only verifies Ethereum-style signatures *inside*
// instruction data — the tx-level signatures themselves are always ed25519.
// So a pure-stdlib ed25519.Verify is correct for every transaction.

// putU16 appends a compact-u16 (shortvec) encoding of v.
func putU16(dst []byte, v int) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(dst, b)
		}
		dst = append(dst, b|0x80)
	}
}

// messageWireBytes re-serializes the parsed message into the exact byte
// sequence the transaction signatures were produced over.
func messageWireBytes(m *parsedMessage) ([]byte, error) {
	if m == nil {
		return nil, errors.New("nil message")
	}
	buf := make([]byte, 0, 512)
	if m.Version == 1 {
		// VERSION_PREFIX (0x80) | version 0 — part of the signed payload.
		buf = append(buf, 0x80)
	} else if m.Version != 0 {
		return nil, fmt.Errorf("unsupported message version %d", m.Version)
	}
	buf = append(buf,
		byte(m.NumRequiredSignatures),
		byte(m.NumReadonlySigned),
		byte(m.NumReadonlyUnsigned),
	)
	buf = putU16(buf, len(m.AccountKeys))
	for _, k := range m.AccountKeys {
		key, err := base58DecodeLen(k, ed25519.PublicKeySize)
		if err != nil {
			return nil, fmt.Errorf("account key not a 32-byte base58 value: %w", err)
		}
		buf = append(buf, key...)
	}
	blockhash, err := base58DecodeLen(m.RecentBlockhash, 32)
	if err != nil {
		return nil, fmt.Errorf("recentBlockhash not a 32-byte base58 value: %w", err)
	}
	buf = append(buf, blockhash...)
	buf = putU16(buf, len(m.Instructions))
	for _, ix := range m.Instructions {
		if ix.ProgramIDIndex < 0 || ix.ProgramIDIndex > 255 {
			return nil, fmt.Errorf("programIdIndex %d out of u8 range", ix.ProgramIDIndex)
		}
		buf = append(buf, byte(ix.ProgramIDIndex))
		buf = putU16(buf, len(ix.Accounts))
		buf = append(buf, ix.Accounts...)
		buf = putU16(buf, len(ix.Data))
		buf = append(buf, ix.Data...)
	}
	if m.Version == 1 {
		buf = putU16(buf, len(m.Lookups))
		for _, lk := range m.Lookups {
			key, err := base58DecodeLen(lk.AccountKey, ed25519.PublicKeySize)
			if err != nil {
				return nil, fmt.Errorf("lookup account key not 32 bytes: %w", err)
			}
			buf = append(buf, key...)
			buf = putU16(buf, len(lk.WritableIndexes))
			buf = append(buf, lk.WritableIndexes...)
			buf = putU16(buf, len(lk.ReadonlyIndexes))
			buf = append(buf, lk.ReadonlyIndexes...)
		}
	}
	return buf, nil
}

// verifyTxSignatures verifies every required ed25519 signature of a parsed
// transaction against the reconstructed wire message bytes.
func verifyTxSignatures(tx *parsedTx) error {
	if tx == nil || !tx.HasMessage {
		return errNotVerifiable
	}
	m := &tx.Message
	if m.NumRequiredSignatures <= 0 {
		return errors.New("message declares zero required signatures")
	}
	if len(m.AccountKeys) < m.NumRequiredSignatures {
		return fmt.Errorf("required signatures %d exceed account keys %d", m.NumRequiredSignatures, len(m.AccountKeys))
	}
	if len(tx.Signatures) != m.NumRequiredSignatures {
		return fmt.Errorf("signature count %d != numRequiredSignatures %d", len(tx.Signatures), m.NumRequiredSignatures)
	}
	msg, err := messageWireBytes(m)
	if err != nil {
		return err
	}
	for i, sigStr := range tx.Signatures {
		sig, err := base58DecodeLen(sigStr, ed25519.SignatureSize)
		if err != nil {
			return fmt.Errorf("signature %d not a 64-byte base58 value: %w", i, err)
		}
		key, err := base58DecodeLen(m.AccountKeys[i], ed25519.PublicKeySize)
		if err != nil {
			return fmt.Errorf("signer key %d not a 32-byte base58 value: %w", i, err)
		}
		if !ed25519.Verify(ed25519.PublicKey(key), msg, sig) {
			return fmt.Errorf("ed25519 signature %d does not verify against signer %s", i, m.AccountKeys[i])
		}
	}
	return nil
}
