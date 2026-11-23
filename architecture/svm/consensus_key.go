package svm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/erpc/erpc/common"
)

func init() {
	common.RegisterConsensusKeyExtractor(common.ConsensusKeyBlockhash, "getBlock", svmGetBlockHashExtractor([]string{"blockhash"}))
	common.RegisterConsensusKeyExtractor(common.ConsensusKeyBlockhashWithLink, "getBlock", svmGetBlockHashExtractor([]string{"blockhash", "previousBlockhash", "parentSlot", "blockHeight"}))
}

// svmGetBlockHashExtractor returns a ConsensusKeyExtractor that hashes only the
// named linkage fields of a getBlock response. The fields are extracted as raw
// JSON values and concatenated in sorted order before hashing, so the hash is
// stable across field ordering in the upstream response.
func svmGetBlockHashExtractor(fields []string) common.ConsensusKeyExtractor {
	sort.Strings(fields)
	return func(ctx context.Context, jr *common.JsonRpcResponse, method string) (string, bool, error) {
		if jr == nil {
			return "", false, nil
		}
		if strings.ToLower(method) != "getblock" {
			return "", false, nil
		}
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			raw, err := jr.PeekBytesByPath(ctx, field)
			if err != nil {
				return "", false, fmt.Errorf("getBlock consensus key missing field %q: %w", field, err)
			}
			parts = append(parts, field+"="+string(raw))
		}
		h := sha256.New()
		_, _ = h.Write([]byte(strings.Join(parts, "\n")))
		return hex.EncodeToString(h.Sum(nil)), true, nil
	}
}
