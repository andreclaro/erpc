package integrity

// Request/response binding checks: a response must be the answer to the
// question that was actually asked. An upstream under pressure (or under
// attack) frequently cheats by answering a cheaper question — serving a
// cached sibling transaction, a longer block list, or rewards with invented
// payees. These checks pin the response to the request with deterministic,
// encoding-local rules.

import (
	"context"
	"encoding/json"
)

func init() {
	register(requestedSigMatch)
	register(blocksLimit)
	register(rewardsShape)
}

func firstSig(sigs []string) string {
	if len(sigs) == 0 {
		return ""
	}
	return sigs[0]
}

// requestedSignatures extracts the base58 signature the getTransaction-family
// request asked for. Only params[0] is the signature; later params are config
// objects and must not be treated as signatures.
func requestedSignatures(d *Decoded) []string {
	if len(d.reqParams) == 0 {
		return nil
	}
	s, ok := d.reqParams[0].(string)
	if !ok || s == "" {
		return nil
	}
	return []string{s}
}

// svm.struct.requestedSigMatch — getTransaction-family: the served
// transaction must actually be the one the client asked for. The request
// signature is the query key, so it must match the served transaction's
// first signature (the transaction's primary id). A mismatch means the
// upstream answered a different (cheaper, cached, or invented) transaction.
var requestedSigMatch = &Check{
	ID:      "svm.struct.requestedSigMatch",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"gettransaction", "getconfirmedtransaction"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		reqSigs := requestedSignatures(d)
		if len(reqSigs) == 0 {
			return Skipped
		}
		t, err := d.TxEnvelope()
		if err != nil || t == nil || len(t.Transaction) == 0 {
			// Null result / no transaction material — nothing to bind.
			return Skipped
		}
		// The object form carries inline signatures; the base58 wire form
		// does not (compact [signatures, message] array) — skip that variant.
		var obj struct {
			Signatures []string `json:"signatures"`
		}
		if err := json.Unmarshal(t.Transaction, &obj); err != nil || len(obj.Signatures) == 0 {
			return Skipped
		}
		if len(obj.Signatures) == 0 || obj.Signatures[0] != reqSigs[0] {
			return failf("served transaction signature %q is not the requested signature %q (answered a different transaction?)", firstSig(obj.Signatures), reqSigs[0])
		}
		return nil
	},
}

// svm.shape.blocksLimit — getBlocks/getBlocksWithLimit: the returned slot
// list may not exceed what was asked for. getBlocksWithLimit bounds by the
// limit param; getBlocks bounds by its explicit end slot (absent end means
// "to tip" — no bound, skip).
var blocksLimit = &Check{
	ID:      "svm.shape.blocksLimit",
	Family:  FamilyShape,
	Class:   Deterministic,
	Methods: []string{"getblocks", "getblockswithlimit"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		list, err := d.BlocksList()
		if err != nil || len(list) == 0 {
			return Skipped
		}
		var bound int64 = -1
		switch d.method {
		case "getblockswithlimit":
			if len(d.reqParams) >= 2 {
				bound, _ = anyInt64(d.reqParams[1])
			}
		case "getblocks":
			// params: start, [end, commitment]. No end -> unbounded to tip.
			if len(d.reqParams) >= 2 {
				start, sok := anyInt64(d.reqParams[0])
				end, eok := anyInt64(d.reqParams[1])
				if sok && eok && end >= start {
					bound = end - start + 1
				}
			}
		}
		if bound < 0 {
			return Skipped
		}
		if int64(len(list)) > bound {
			return failf("served %d block slots but the request bounds the answer to %d (longer list than asked?)", len(list), bound)
		}
		return nil
	},
}

// svm.struct.rewardsShape — getBlock rewards entries: payee must be a
// decodable base58 public key and lamports a signed integer (rent rewards
// are legitimately negative). rewardType vocabulary is cluster-evolving, so
// only structural fields are pinned.
var rewardsShape = &Check{
	ID:      "svm.struct.rewardsShape",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		b, err := d.Block()
		if err != nil || b == nil || len(b.Rewards) == 0 {
			return Skipped
		}
		for i, r := range b.Rewards {
			raw := r
			var rw struct {
				Pubkey   string          `json:"pubkey"`
				Lamports json.RawMessage `json:"lamports"`
			}
			if err := json.Unmarshal(raw, &rw); err != nil {
				return failf("reward [%d]: malformed entry", i)
			}
			pk, err := base58DecodeLen(rw.Pubkey, 32)
			if err != nil || len(pk) != 32 {
				return failf("reward [%d]: payee %q is not a base58 public key", i, rw.Pubkey)
			}
			if _, ok := rawJSONInt64(rw.Lamports); !ok {
				return failf("reward [%d]: lamports is not an integer", i)
			}
		}
		return nil
	},
}
