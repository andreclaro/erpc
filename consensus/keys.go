package consensus

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/erpc/erpc/common"
)

// ConsensusKey selects how successful responses are reduced to a consensus key.
type ConsensusKey string

const (
	// ConsensusKeyCanonical hashes the full normalized response (default).
	ConsensusKeyCanonical ConsensusKey = "canonical"
	// ConsensusKeyBlockhash compares only the block hash.
	ConsensusKeyBlockhash ConsensusKey = "blockhash"
	// ConsensusKeyBlockhashWithLink compares block hash plus chain-link fields
	// (parent hash, parent slot/number, block height).
	ConsensusKeyBlockhashWithLink ConsensusKey = "blockhashWithLink"
)

// ParseConsensusKey normalizes a config string into a typed key. Unknown values
// fall back to canonical so a typo cannot silently disable comparison.
func ParseConsensusKey(s string) ConsensusKey {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case strings.ToLower(string(ConsensusKeyBlockhash)):
		return ConsensusKeyBlockhash
	case strings.ToLower(string(ConsensusKeyBlockhashWithLink)):
		return ConsensusKeyBlockhashWithLink
	default:
		return ConsensusKeyCanonical
	}
}

// keyExtractor turns a successful JSON-RPC result into a short consensus key.
// If the result shape is unsupported or missing required fields, it returns an
// error and the caller falls back to canonical hashing.
type keyExtractor func(ctx context.Context, raw []byte) (string, error)

var (
	errMissingBlockhash = errors.New("result has no blockhash")
	errMissingLinkField = errors.New("result missing a chain-link field")
)

// consensusKeyExtractors maps (key, lowercased method) to an extractor. Methods
// without an entry for the selected key fall back to canonical hashing.
var consensusKeyExtractors = map[ConsensusKey]map[string]keyExtractor{
	ConsensusKeyBlockhash: {
		"getblock":             extractSvmBlockhash,
		"getconfirmedblock":    extractSvmBlockhash,
		"eth_getblockbynumber": extractEvmBlockhash,
		"eth_getblockbyhash":   extractEvmBlockhash,
	},
	ConsensusKeyBlockhashWithLink: {
		"getblock":             extractSvmBlockhashWithLink,
		"getconfirmedblock":    extractSvmBlockhashWithLink,
		"eth_getblockbynumber": extractEvmBlockhashWithLink,
		"eth_getblockbyhash":   extractEvmBlockhashWithLink,
	},
}

// methodKeyExtractor returns the extractor for the configured key and method,
// or nil if the method has no fast-path extractor for that key.
func methodKeyExtractor(key ConsensusKey, method string) keyExtractor {
	if key == ConsensusKeyCanonical || method == "" {
		return nil
	}
	if methods, ok := consensusKeyExtractors[key]; ok {
		if ex, ok := methods[strings.ToLower(method)]; ok {
			return ex
		}
	}
	return nil
}

// extractSvmBlockhash returns the Solana blockhash.
func extractSvmBlockhash(_ context.Context, raw []byte) (string, error) {
	var obj struct {
		Blockhash string `json:"blockhash"`
	}
	if err := common.SonicCfg.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.Blockhash == "" {
		return "", errMissingBlockhash
	}
	return "svm:" + obj.Blockhash, nil
}

// extractSvmBlockhashWithLink returns blockhash + previousBlockhash + parentSlot
// + blockHeight. Pointer fields distinguish absent values from zero.
func extractSvmBlockhashWithLink(_ context.Context, raw []byte) (string, error) {
	var obj struct {
		Blockhash         string `json:"blockhash"`
		PreviousBlockhash string `json:"previousBlockhash"`
		ParentSlot        *int64 `json:"parentSlot"`
		BlockHeight       *int64 `json:"blockHeight"`
	}
	if err := common.SonicCfg.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.Blockhash == "" {
		return "", errMissingBlockhash
	}
	if obj.PreviousBlockhash == "" || obj.ParentSlot == nil {
		return "", errMissingLinkField
	}
	return fmt.Sprintf("svm:%s:%s:%s:%s",
		obj.Blockhash,
		obj.PreviousBlockhash,
		formatIntPtr(obj.ParentSlot),
		formatIntPtr(obj.BlockHeight),
	), nil
}

// extractEvmBlockhash returns the EVM block hash.
func extractEvmBlockhash(_ context.Context, raw []byte) (string, error) {
	var obj struct {
		Hash string `json:"hash"`
	}
	if err := common.SonicCfg.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.Hash == "" {
		return "", errMissingBlockhash
	}
	return "evm:" + obj.Hash, nil
}

// extractEvmBlockhashWithLink returns hash + parentHash + number. The number is
// kept as the raw hex string to avoid normalization differences.
func extractEvmBlockhashWithLink(_ context.Context, raw []byte) (string, error) {
	var obj struct {
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
		Number     string `json:"number"`
	}
	if err := common.SonicCfg.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.Hash == "" {
		return "", errMissingBlockhash
	}
	if obj.ParentHash == "" || obj.Number == "" {
		return "", errMissingLinkField
	}
	return fmt.Sprintf("evm:%s:%s:%s", obj.Hash, obj.ParentHash, obj.Number), nil
}

func formatIntPtr(v *int64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *v)
}
