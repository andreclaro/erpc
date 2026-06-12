package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsensusPolicyConfig_MinAgreementValidation(t *testing.T) {
	base := func(rp ...*ConsensusRequiredParticipant) *ConsensusPolicyConfig {
		return &ConsensusPolicyConfig{
			MaxParticipants:      3,
			AgreementThreshold:   2,
			RequiredParticipants: rp,
		}
	}

	t.Run("valid mixed-consensus config", func(t *testing.T) {
		c := base(
			&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
			&ConsensusRequiredParticipant{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
		)
		require.NoError(t, c.Validate())
	})

	t.Run("minAgreement exceeding minParticipants is rejected", func(t *testing.T) {
		c := base(&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 1, MinAgreement: 2})
		err := c.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "minAgreement")
		require.Contains(t, err.Error(), "minParticipants")
	})

	t.Run("minAgreement exceeding agreementThreshold is rejected", func(t *testing.T) {
		c := base(&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 3, MinAgreement: 3})
		err := c.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "agreementThreshold")
	})

	t.Run("negative minAgreement is rejected", func(t *testing.T) {
		c := base(&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 1, MinAgreement: -1})
		require.Error(t, c.Validate())
	})

	t.Run("minAgreement omitted (pool-only) stays valid", func(t *testing.T) {
		c := base(&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 1})
		require.NoError(t, c.Validate())
	})

	t.Run("sum of minAgreement exceeds agreementThreshold is rejected", func(t *testing.T) {
		c := base(
			&ConsensusRequiredParticipant{Tag: "type:internal", MinParticipants: 2, MinAgreement: 2},
			&ConsensusRequiredParticipant{Tag: "type:external", MinParticipants: 2, MinAgreement: 2},
		)
		err := c.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "sum of minAgreement")
	})
}
