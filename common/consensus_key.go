package common

import (
	"context"
	"fmt"
	"strings"
)

// ConsensusKey selects how a consensus round turns successful responses into
// comparable hashes. The default "canonical" hashes the full normalized
// payload. Architecture packages can register extractors for other keys that
// compare only the fields that identify the returned object.
type ConsensusKey string

const (
	// ConsensusKeyCanonical is the default: hash the full normalized response.
	ConsensusKeyCanonical ConsensusKey = "canonical"

	// ConsensusKeyBlockhash hashes only the blockhash field. Registered by the
	// SVM architecture package for getBlock.
	ConsensusKeyBlockhash ConsensusKey = "blockhash"

	// ConsensusKeyBlockhashWithLink hashes the blockhash plus the linkage
	// fields that tie the block to its parent (previousBlockhash, parentSlot,
	// blockHeight). Registered by the SVM architecture package for getBlock.
	ConsensusKeyBlockhashWithLink ConsensusKey = "blockhashWithLink"
)

// ConsensusKeyExtractor turns a successful JSON-RPC response into a hash for a
// specific consensus key. The method argument is the normalized request method.
// If the extractor returns ok=false the caller falls back to the canonical hash.
type ConsensusKeyExtractor func(ctx context.Context, jr *JsonRpcResponse, method string) (hash string, ok bool, err error)

var consensusKeyExtractors = map[ConsensusKey]map[string]ConsensusKeyExtractor{}

// RegisterConsensusKeyExtractor registers an extractor for a consensus key and
// method. Registrations are typically made from architecture package init()
// functions. The last registration wins if the same key/method is registered
// twice.
func RegisterConsensusKeyExtractor(key ConsensusKey, method string, fn ConsensusKeyExtractor) {
	method = strings.ToLower(method)
	if consensusKeyExtractors[key] == nil {
		consensusKeyExtractors[key] = map[string]ConsensusKeyExtractor{}
	}
	consensusKeyExtractors[key][method] = fn
}

// GetConsensusKeyExtractor returns the registered extractor for key/method, if
// any. Method matching is case-insensitive.
func GetConsensusKeyExtractor(key ConsensusKey, method string) (ConsensusKeyExtractor, bool) {
	if key == "" || key == ConsensusKeyCanonical {
		return nil, false
	}
	m, ok := consensusKeyExtractors[key]
	if !ok {
		return nil, false
	}
	method = strings.ToLower(method)
	fn, ok := m[method]
	return fn, ok
}

// NormalizeConsensusKey returns the canonical form of a user-supplied key
// string. Empty values are normalized to ConsensusKeyCanonical.
func NormalizeConsensusKey(key string) (ConsensusKey, error) {
	if strings.TrimSpace(key) == "" {
		return ConsensusKeyCanonical, nil
	}
	switch ConsensusKey(strings.ToLower(key)) {
	case ConsensusKeyCanonical:
		return ConsensusKeyCanonical, nil
	case ConsensusKeyBlockhash:
		return ConsensusKeyBlockhash, nil
	case ConsensusKeyBlockhashWithLink:
		return ConsensusKeyBlockhashWithLink, nil
	default:
		return "", fmt.Errorf("unsupported consensus key: %q", key)
	}
}
