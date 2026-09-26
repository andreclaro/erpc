package integrity

import (
	"context"
)

// Authenticity checks: per-item cryptographic authenticity. These are the
// pillar-1 substitute for EVM's tx/receipt Merkle membership recomputation —
// Solana exposes no tx Merkle root in RPC, so the batch ed25519 signatures
// are the tamper-evidence.

func init() {
	register(signatureVerify)
	register(genesisHash)
}

var signatureVerify = &Check{
	ID:      "svm.auth.signatureVerify",
	Family:  FamilyAuthenticity,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock", "gettransaction"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		txs, err := blockOrEnvelopeTxs(d)
		if err != nil || len(txs) == 0 {
			// Includes the transactionDetails:"signatures" shape (bare strings,
			// no message material): skip, never pass silently.
			return Skipped
		}
		for i, tx := range txs {
			if err := verifyTxSignatures(tx); err != nil {
				return failf("tx[%d]: %s", i, err.Error())
			}
		}
		return nil
	},
}

// paramExpectedGenesisHash is set by the config compiler from the network's
// known cluster table — the check skips (never fabricates) when unknown.
const paramExpectedGenesisHash = "expected"

var genesisHash = &Check{
	ID:      "svm.auth.genesisHash",
	Family:  FamilyAuthenticity,
	Class:   Deterministic,
	Methods: []string{"getgenesishash"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		got, ok := d.GenesisHash()
		if !ok {
			return Skipped
		}
		expected := cfg.param(paramExpectedGenesisHash, "")
		if expected == "" {
			// Cluster unknown to us: nothing authoritative to compare against.
			return Skipped
		}
		if got != expected {
			return failf("genesis hash %s does not match the known hash %s for this cluster (wrong cluster / synthesized response)", got, expected)
		}
		return nil
	},
}
