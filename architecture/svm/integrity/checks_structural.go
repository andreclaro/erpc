package integrity

import (
	"context"
	"time"
)

// Structural checks: cross-reference invariants over a block's own fields and
// its transactions. All deterministic — provable from committed data alone.

func init() {
	register(blockShape)
	register(txShape)
	register(sigUniqueness)
}

// solanaMainnetGenesisTime is the mainnet-beta genesis (2020-03-16T00:00:00Z).
// Earlier blockTime values are impossible; the generous upper bound absorbs
// clock skew on serving nodes.
const solanaGenesisTime = int64(1584230400)

var blockShape = &Check{
	ID:      "svm.struct.blockShape",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		b, err := d.Block()
		if err != nil || b == nil || b.Blockhash == "" {
			return Skipped
		}
		bh, err := base58DecodeLen(b.Blockhash, 32)
		if err != nil {
			return failf("blockhash %q is not a 32-byte base58 value", b.Blockhash)
		}
		ph, err := base58DecodeLen(b.PreviousBlockhash, 32)
		if err != nil {
			return failf("previousBlockhash %q is not a 32-byte base58 value", b.PreviousBlockhash)
		}
		// Genesis (slot 0) self-references an all-zeros previous hash; every
		// other block must chain to a distinct parent hash.
		isZeroParent := true
		for _, by := range ph {
			if by != 0 {
				isZeroParent = false
				break
			}
		}
		if !isZeroParent && b.Blockhash == b.PreviousBlockhash {
			return failf("block %s references itself as parent", b.Blockhash)
		}
		if b.ParentSlot != nil && *b.ParentSlot < 0 {
			return failf("parentSlot %d is negative", *b.ParentSlot)
		}
		// parentSlot must be strictly below the requested slot (skipped-slot
		// chains make gaps legal, so slot == parentSlot + 1 is NOT required).
		if slot, ok := d.RequestedSlot(); ok && slot > 0 {
			if b.ParentSlot != nil && *b.ParentSlot >= slot {
				return failf("parentSlot %d >= requested slot %d", *b.ParentSlot, slot)
			}
		}
		if b.BlockHeight != nil && *b.BlockHeight < 0 {
			return failf("blockHeight %d is negative", *b.BlockHeight)
		}
		if b.BlockTime != nil {
			now := time.Now().Unix()
			if *b.BlockTime < solanaGenesisTime {
				return failf("blockTime %d predates Solana genesis", *b.BlockTime)
			}
			if *b.BlockTime > now+120 {
				return failf("blockTime %d is %ds in the future (clock skew bound exceeded)", *b.BlockTime, *b.BlockTime-now)
			}
		}
		// A block carrying a blockhash always carries a tx list — possibly
		// empty (skipped-slot placeholders are legal). Agave omits the field
		// entirely only when the request set transactionDetails:"signatures"
		// (top-level signatures array instead) or "none"; anywhere else, a
		// null transactions array means the payload was truncated/synthesized.
		if b.Transactions == nil {
			td, _ := d.requestTransactionDetails()
			switch td {
			case "signatures", "none":
				// legal omission — "signatures" mode must still surface the
				// signature array it substitutes.
				if td == "signatures" && b.Signatures == nil {
					return failf("block %s requested with transactionDetails:\"signatures\" carries no signatures array", b.Blockhash)
				}
			default:
				return failf("block %s has a null transactions array (missing transactionDetails or truncated payload)", b.Blockhash)
			}
		}
		_ = bh
		return nil
	},
}

var txShape = &Check{
	ID:      "svm.struct.txShape",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock", "gettransaction"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		txs, err := blockOrEnvelopeTxs(d)
		if err != nil || len(txs) == 0 {
			return Skipped
		}
		for i, tx := range txs {
			v := checkTxShape(tx)
			if v != nil {
				return failf("tx[%d]: %s", i, v.Reason)
			}
		}
		return nil
	},
}

// blockOrEnvelopeTxs returns the transactions to shape-check for the method:
// the block's tx list, or the single getTransaction envelope. Unverifiable
// encodings make the whole check skip (chain-safety).
func blockOrEnvelopeTxs(d *Decoded) ([]*parsedTx, error) {
	switch d.method {
	case "getblock", "getconfirmedblock":
		return d.BlockTxs()
	case "gettransaction":
		tx, err := d.TxForVerify()
		if err != nil {
			return nil, err
		}
		return []*parsedTx{tx}, nil
	}
	return nil, errNotVerifiable
}

func checkTxShape(tx *parsedTx) *Violation {
	m := &tx.Message
	if len(tx.Signatures) == 0 {
		return failf("no signatures")
	}
	if m.NumRequiredSignatures <= 0 {
		return failf("numRequiredSignatures is %d", m.NumRequiredSignatures)
	}
	if len(m.AccountKeys) == 0 {
		return failf("no account keys")
	}
	if m.NumRequiredSignatures > len(m.AccountKeys) {
		return failf("numRequiredSignatures %d exceeds account keys %d", m.NumRequiredSignatures, len(m.AccountKeys))
	}
	if m.NumReadonlySigned > m.NumRequiredSignatures {
		return failf("numReadonlySignedAccounts %d exceeds numRequiredSignatures %d", m.NumReadonlySigned, m.NumRequiredSignatures)
	}
	if m.NumReadonlyUnsigned > len(m.AccountKeys)-m.NumRequiredSignatures {
		return failf("numReadonlyUnsignedAccounts %d exceeds unsigned accounts %d", m.NumReadonlyUnsigned, len(m.AccountKeys)-m.NumRequiredSignatures)
	}
	if len(tx.Signatures) != m.NumRequiredSignatures {
		return failf("signature count %d != numRequiredSignatures %d", len(tx.Signatures), m.NumRequiredSignatures)
	}
	if _, err := base58DecodeLen(m.RecentBlockhash, 32); err != nil {
		return failf("recentBlockhash %q is not a 32-byte base58 value", m.RecentBlockhash)
	}
	for j, key := range m.AccountKeys {
		if _, err := base58DecodeLen(key, 32); err != nil {
			return failf("accountKeys[%d] %q is not a 32-byte base58 value", j, key)
		}
	}
	for j, ix := range m.Instructions {
		if ix.ProgramIDIndex >= len(m.AccountKeys) {
			return failf("instructions[%d] programIdIndex %d out of range (%d keys)", j, ix.ProgramIDIndex, len(m.AccountKeys))
		}
		for _, a := range ix.Accounts {
			if int(a) >= len(m.AccountKeys) {
				return failf("instructions[%d] account index %d out of range (%d keys)", j, a, len(m.AccountKeys))
			}
		}
	}
	if m.Version == 1 {
		for j, lk := range m.Lookups {
			if _, err := base58DecodeLen(lk.AccountKey, 32); err != nil {
				return failf("addressTableLookups[%d] accountKey %q is not 32 bytes", j, lk.AccountKey)
			}
		}
	}
	return nil
}

var sigUniqueness = &Check{
	ID:      "svm.struct.sigUniqueness",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		b, err := d.Block()
		if err != nil || b == nil {
			return Skipped
		}
		if b.Transactions != nil {
			// Full tx objects (transactionDetails full/accounts — the default).
			txs, err := d.BlockTxs()
			if err != nil || len(txs) == 0 {
				return Skipped
			}
			sigs := make([]string, 0, len(txs))
			for _, tx := range txs {
				sigs = append(sigs, tx.Signatures...)
			}
			return sigUniquenessOf(sigs)
		}
		// transactionDetails:"signatures" — Agave omits transactions and
		// substitutes the top-level signatures array. transactionDetails:"none"
		// emits neither — nothing to compare.
		return sigUniquenessOf(b.Signatures)
	},
}

func sigUniquenessOf(sigs []string) *Violation {
	if len(sigs) == 0 {
		return Skipped // "pass" must mean a comparison happened
	}
	seen := make(map[string]struct{}, len(sigs))
	for _, s := range sigs {
		if s == "" {
			return failf("empty signature in block")
		}
		if _, dup := seen[s]; dup {
			return failf("duplicate signature %s in block (replay/synthesis indicator)", s)
		}
		seen[s] = struct{}{}
	}
	return nil
}
