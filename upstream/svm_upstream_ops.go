package upstream

import (
	"context"
	"fmt"
	"strings"

	"github.com/erpc/erpc/common"
)

// svmVerifyGenesisHash guards against a mis-configured Solana upstream pointing
// at the wrong cluster (e.g. an upstream listed under mainnet-beta that actually
// serves devnet). It runs once at bootstrap.
//
// Known clusters (mainnet-beta, devnet, testnet) are matched against the local
// genesis-hash table without an RPC call. Unknown clusters are verified via
// getGenesisHash only when CheckGenesisHash:true is set — otherwise we skip
// silently to support private/local clusters.
func (u *Upstream) svmVerifyGenesisHash(ctx context.Context) error {
	cfg := u.config
	if cfg == nil || cfg.Svm == nil {
		return nil
	}
	cluster := cfg.Svm.Cluster
	if cluster == "" {
		return nil
	}

	expected, known := common.KnownGenesisHash(cluster)

	if !known && !cfg.Svm.CheckGenesisHash {
		u.logger.Debug().Str("cluster", cluster).
			Msg("skipping svm genesis hash validation: unknown cluster without checkGenesisHash")
		return nil
	}

	actual, err := u.svmFetchGenesisHash(ctx)
	if err != nil {
		// Treat fetch failures as non-fatal unless the operator explicitly opted in.
		if cfg.Svm.CheckGenesisHash {
			return common.NewErrUpstreamClientInitialization(
				fmt.Errorf("svm getGenesisHash failed: %w", err),
				u,
			)
		}
		u.logger.Warn().Err(err).Str("cluster", cluster).
			Msg("svm getGenesisHash failed; continuing (checkGenesisHash not set)")
		return nil
	}

	if known && expected != "" && !strings.EqualFold(actual, expected) {
		return common.NewErrUpstreamClientInitialization(
			fmt.Errorf("svm genesis hash mismatch for cluster %q: expected %s, got %s", cluster, expected, actual),
			u,
		)
	}
	u.logger.Debug().Str("cluster", cluster).Str("genesisHash", actual).
		Msg("svm genesis hash validated")
	return nil
}

func (u *Upstream) svmFetchGenesisHash(ctx context.Context) (string, error) {
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getGenesisHash","params":[]}`))
	resp, err := u.Forward(ctx, req, true)
	if resp != nil {
		defer resp.Release()
	}
	if err != nil {
		return "", err
	}
	jrr, err := resp.JsonRpcResponse()
	if err != nil {
		return "", err
	}
	if jrr.Error != nil {
		return "", jrr.Error
	}
	var hash string
	if err := common.SonicCfg.Unmarshal(jrr.GetResultBytes(), &hash); err != nil {
		return "", fmt.Errorf("decode genesis hash: %w", err)
	}
	return hash, nil
}

// SvmStatePoller exposes the per-upstream SVM slot tracker for hooks and tests.
func (u *Upstream) SvmStatePoller() common.SvmStatePoller {
	return u.svmStatePoller
}
