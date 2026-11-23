package consensus

import (
	"context"
	"testing"

	_ "github.com/erpc/erpc/architecture/svm"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func init() {
	util.ConfigureTestLogger()
}

// mutateBlockField unmarshals a getBlock result, replaces one top-level field,
// and re-marshals it.
func mutateBlockField(raw []byte, field string, value any) []byte {
	var block map[string]any
	if err := common.SonicCfg.Unmarshal(raw, &block); err != nil {
		panic(err)
	}
	block[field] = value
	out, err := common.SonicCfg.Marshal(block)
	if err != nil {
		panic(err)
	}
	return out
}

// TestConsensus_SVM_GetBlock_Key_Agreement verifies that blockhash and
// blockhashWithLink keys group identical getBlock responses together, even when
// non-linkage fields like blockTime differ.
func TestConsensus_SVM_GetBlock_Key_Agreement(t *testing.T) {
	base := benchSvmGetBlock(10)

	for _, key := range []common.ConsensusKey{common.ConsensusKeyBlockhash, common.ConsensusKeyBlockhashWithLink} {
		t.Run(string(key), func(t *testing.T) {
			logger := zerolog.New(zerolog.NewTestWriter(t))
			pol := NewConsensusPolicyBuilder().
				WithKey(key).
				WithMaxParticipants(2).
				WithAgreementThreshold(2).
				WithLogger(&logger).
				Build()

			req := benchSvmGetBlockRequest()
			ctx := context.WithValue(context.Background(), common.RequestContextKey, req)
			winner, err := pol.Run(ctx, req, func(_ context.Context, _ *common.NormalizedRequest) (*common.NormalizedResponse, error) {
				return benchSvmGetBlockResponse(base), nil
			})
			require.NoError(t, err)
			require.NotNil(t, winner)
		})
	}
}

// TestConsensus_SVM_GetBlock_Key_DetectsDivergence verifies that the
// blockhashWithLink key disputes when linkage fields differ, while blockhash
// (which ignores linkage) still agrees.
func TestConsensus_SVM_GetBlock_Key_DetectsDivergence(t *testing.T) {
	baseA := benchSvmGetBlock(10)
	baseB := mutateBlockField(baseA, "previousBlockhash", "1uH7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7X")

	t.Run("blockhash agrees", func(t *testing.T) {
		logger := zerolog.New(zerolog.NewTestWriter(t))
		pol := NewConsensusPolicyBuilder().
			WithKey(common.ConsensusKeyBlockhash).
			WithMaxParticipants(2).
			WithAgreementThreshold(2).
			WithLogger(&logger).
			Build()

		req := benchSvmGetBlockRequest()
		ctx := context.WithValue(context.Background(), common.RequestContextKey, req)
		calls := 0
		winner, err := pol.Run(ctx, req, func(_ context.Context, _ *common.NormalizedRequest) (*common.NormalizedResponse, error) {
			calls++
			if calls == 1 {
				return benchSvmGetBlockResponse(baseA), nil
			}
			return benchSvmGetBlockResponse(baseB), nil
		})
		require.NoError(t, err)
		require.NotNil(t, winner)
	})

	t.Run("blockhashWithLink disputes", func(t *testing.T) {
		logger := zerolog.New(zerolog.NewTestWriter(t))
		pol := NewConsensusPolicyBuilder().
			WithKey(common.ConsensusKeyBlockhashWithLink).
			WithMaxParticipants(2).
			WithAgreementThreshold(2).
			WithDisputeBehavior(common.ConsensusDisputeBehaviorReturnError).
			WithLogger(&logger).
			Build()

		req := benchSvmGetBlockRequest()
		ctx := context.WithValue(context.Background(), common.RequestContextKey, req)
		calls := 0
		_, err := pol.Run(ctx, req, func(_ context.Context, _ *common.NormalizedRequest) (*common.NormalizedResponse, error) {
			calls++
			if calls == 1 {
				return benchSvmGetBlockResponse(baseA), nil
			}
			return benchSvmGetBlockResponse(baseB), nil
		})
		require.Error(t, err)
		require.True(t, common.HasErrorCode(err, common.ErrCodeConsensusDispute))
	})
}
