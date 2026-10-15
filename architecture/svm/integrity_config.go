package svm

import (
	"strings"

	"github.com/erpc/erpc/architecture/svm/integrity"
	"github.com/erpc/erpc/common"
)

// Integrity plumbing for the SVM architecture — mirrors the EVM side
// (architecture/evm/integrity_config.go) over the shared, architecture-agnostic
// common.IntegrityConfig. SVM-specific bits: the cluster's known genesis hash
// feeds svm.auth.genesisHash; there is no SVM chain-profile table yet (EVM's
// ChainProfiles has no SVM counterpart — the hook point stays for later).

// compileIntegritySettings compiles the network's IntegritySettings into the
// engine's CheckSet + ReorgPolicy. One translation point so Validate never
// touches config shapes.
func compileIntegritySettings(s *common.IntegritySettings, chain, cluster string) (integrity.CheckSet, integrity.ReorgPolicy) {
	cs := integrity.CheckSet{}
	if s == nil {
		return cs, integrity.DefaultReorgPolicy()
	}
	cs = integrity.CheckSetForLevel(integrity.Level(strings.ToLower(s.Level)))
	for id, oc := range s.Checks {
		if oc != nil {
			applyCheckOverride(cs, id, oc)
		}
	}
	// Bind the cluster's known genesis hash so svm.auth.genesisHash can
	// compare against ground truth instead of self-reporting.
	if genesis, ok := common.KnownGenesisHash(chain, cluster); ok && genesis != "" {
		cfg := cs[genesisHashCheckID]
		if cfg.Enabled {
			if cfg.Params == nil {
				cfg.Params = map[string]string{}
			}
			cfg.Params[genesisHashExpectedParam] = genesis
			cs[genesisHashCheckID] = cfg
		}
	}
	policy := integrity.DefaultReorgPolicy()
	if s.InvalidBehavior != nil {
		if b, ok := parseBehavior(s.InvalidBehavior.Finalized); ok {
			policy.Finalized = b
		}
		if b, ok := parseBehavior(s.InvalidBehavior.Unfinalized); ok {
			policy.Unfinalized = b
		}
	}
	return cs, policy
}

// applyCheckOverride mutates cs for one per-check override: enable/disable,
// parameters, and an optional per-check failure mode.
func applyCheckOverride(cs integrity.CheckSet, id string, oc *common.IntegrityCheckConfig) {
	switch {
	case oc.Enabled != nil && !*oc.Enabled:
		cfg := cs[id]
		cfg.Enabled = false
		cs[id] = cfg
	case oc.Enabled != nil && *oc.Enabled:
		cfg := cs[id]
		cfg.Enabled = true
		cs[id] = cfg
	}
	if len(oc.Params) > 0 {
		cfg := cs[id]
		cfg.Params = oc.Params
		cs[id] = cfg
	}
	if b, ok := parseBehavior(oc.OnFailure); ok {
		cfg := cs[id]
		cfg.FailOverride = &b
		cs[id] = cfg
	}
}

const (
	genesisHashCheckID       = "svm.auth.genesisHash"
	genesisHashExpectedParam = "expected"
)

// resolveIntegrity computes the effective CheckSet and ReorgPolicy for a request.
// The network's integrity config is the single source: its level/profiles plus
// the per-request header selector. With no config, nothing runs (opt-in).
func resolveIntegrity(n common.Network, dirs *common.RequestDirectives) (integrity.CheckSet, integrity.ReorgPolicy, bool) {
	if n == nil || n.Config() == nil || n.Config().Integrity == nil {
		return nil, integrity.ReorgPolicy{}, false
	}
	selector := ""
	if dirs != nil {
		selector = dirs.IntegritySelector
	}
	var chain, cluster string
	if svm := n.Config().Svm; svm != nil {
		chain = svm.Chain
		cluster = svm.Cluster
	}
	settings := resolveRequestSettings(n.Config().Integrity, selector)
	cs, policy := compileIntegritySettings(settings, chain, cluster)
	observeOnly := settings != nil && settings.ObserveOnly != nil && *settings.ObserveOnly
	return cs, policy, observeOnly
}

// resolveRequestSettings computes the effective settings for one request: the
// configured base, with the per-request header selector overlaid when
// headerMode permits. In profiles mode a request may only select a named
// profile; in full mode it may also set a level word; off ignores the selector.
func resolveRequestSettings(cfg *common.IntegrityConfig, selector string) *common.IntegritySettings {
	if cfg == nil {
		return nil
	}
	base := cfg.IntegritySettings.Copy()
	if base == nil {
		base = &common.IntegritySettings{}
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return base
	}
	switch strings.ToLower(strings.TrimSpace(cfg.HeaderMode)) {
	case common.IntegrityHeaderModeProfiles:
		overlaySettings(base, cfg.Profiles[selector])
	case common.IntegrityHeaderModeFull:
		if isIntegrityLevel(selector) {
			base.Level = strings.ToLower(selector)
		} else {
			overlaySettings(base, cfg.Profiles[selector])
		}
	}
	return base
}

// overlaySettings merges over's set (non-zero) fields onto base.
func overlaySettings(base, over *common.IntegritySettings) {
	if base == nil || over == nil {
		return
	}
	if over.Level != "" {
		base.Level = over.Level
	}
	if over.Budget != nil {
		base.Budget = over.Budget.Copy()
	}
	if over.InvalidBehavior != nil {
		base.InvalidBehavior = over.InvalidBehavior.Copy()
	}
	if over.AutoCorrectWhenPossible != nil {
		base.AutoCorrectWhenPossible = over.AutoCorrectWhenPossible
	}
	for id, c := range over.Checks {
		if base.Checks == nil {
			base.Checks = make(map[string]*common.IntegrityCheckConfig, len(over.Checks))
		}
		base.Checks[id] = c.Copy()
	}
}

func isIntegrityLevel(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "intrinsic", "corroborated", "authoritative":
		return true
	}
	return false
}

// parseBehavior maps the config/header vocabulary (recordOnly | hardReject |
// off — exactly these, validated loudly at load) to an engine Behavior.
// ok=false when the string is empty/unrecognized so callers keep their default.
func parseBehavior(s string) (integrity.Behavior, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hardreject":
		return integrity.BehaviorError, true
	case "recordonly":
		return integrity.BehaviorRecord, true
	case "off":
		return integrity.BehaviorIgnore, true
	default:
		return integrity.BehaviorError, false
	}
}
