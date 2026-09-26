package integrity

import (
	"context"
)

// Shape checks: cheap syntactic sanity — magnitudes, the commitment
// vocabulary, and slot encoding. All deterministic.

func init() {
	register(magnitude)
	register(commitmentParam)
	register(slotEncoding)
}

// contextSlotMethods mirrors the SVM handler's envelope-method set: results
// carry result.context.slot. Kept as a local copy (wire-contract stable).
var contextSlotMethods = map[string]struct{}{
	"getaccountinfo":                    {},
	"getbalance":                        {},
	"getblock":                          {},
	"getblockcommitment":                {},
	"getblockheight":                    {},
	"getblockproduction":                {},
	"getblocks":                         {},
	"getblockswithlimit":                {},
	"getepochinfo":                      {},
	"getfeeformessage":                  {},
	"getfees":                           {},
	"gethealth":                         {},
	"gethighestsnapshotslot":            {},
	"getidentity":                       {},
	"getinflationgovernor":              {},
	"getinflationrate":                  {},
	"getlargestaccounts":                {},
	"getleaderchedule":                  {},
	"getmaxretransmitslot":              {},
	"getmaxshredinsertslot":             {},
	"getminimumbalanceforrentexemption": {},
	"getmultipleaccounts":               {},
	"getprogramaccounts":                {},
	"getrecentperformancesamples":       {},
	"getrecentprioritizationfees":       {},
	"getsignaturesforaddress":           {},
	"getsignaturestatuses":              {},
	"getslot":                           {},
	"getslotleader":                     {},
	"getstakeminimumdelegation":         {},
	"gettokenaccountbalance":            {},
	"gettokenaccountsbydelegate":        {},
	"gettokenaccountsbyowner":           {},
	"gettokenlargestaccounts":           {},
	"gettokensupply":                    {},
	"gettransaction":                    {},
	"gettransactioncount":               {},
	"getvoteaccounts":                   {},
	"isblockhashvalid":                  {},
	"requestairdrop":                    {},
	"sendtransaction":                   {},
	"simulatetransaction":               {},
}

// knownCommitmentLevels is the vocabulary a node must accept. The canonical
// three plus Agave's legacy aliases (all still accepted by RPC).
var knownCommitmentLevels = map[string]struct{}{
	"processed":    {},
	"confirmed":    {},
	"finalized":    {},
	"recent":       {}, // legacy alias of processed
	"single":       {}, // legacy alias of confirmed
	"singleGossip": {}, // legacy alias of confirmed
	"root":         {}, // legacy alias of finalized
	"max":          {}, // legacy alias of finalized
}

const (
	maxBlockTransactions = 500_000
	maxSignaturesPerPage = 1_000
	maxCommitmentDepth   = 32
)

var magnitude = &Check{
	ID:      "svm.shape.magnitude",
	Family:  FamilyShape,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock", "getblockcommitment", "getsignaturesforaddress"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		switch d.method {
		case "getblock", "getconfirmedblock":
			b, err := d.Block()
			if err != nil || b == nil {
				return Skipped
			}
			if len(b.Transactions) > maxBlockTransactions {
				return failf("block claims %d transactions (protocol bound %d)", len(b.Transactions), maxBlockTransactions)
			}
		case "getblockcommitment":
			c, err := d.BlockCommitment()
			if err != nil || c == nil {
				return Skipped
			}
			if len(c.Commitment) != maxCommitmentDepth {
				return failf("commitment array has %d entries (protocol bound %d)", len(c.Commitment), maxCommitmentDepth)
			}
			if c.TotalStake != nil && *c.TotalStake < 0 {
				return failf("totalStake %d is negative", *c.TotalStake)
			}
		case "getsignaturesforaddress":
			list, err := d.SigList()
			if err != nil {
				return Skipped
			}
			if len(list) > maxSignaturesPerPage {
				return failf("signature list has %d entries (protocol bound %d)", len(list), maxSignaturesPerPage)
			}
			for i, e := range list {
				if e.Signature == "" {
					return failf("signatures[%d] has an empty signature", i)
				}
			}
		}
		return nil
	},
}

var commitmentParam = &Check{
	ID:            "svm.shape.commitmentParam",
	Family:        FamilyShape,
	Class:         Deterministic,
	Methods:       envelopeMethodList(),
	AllowEmptyish: true, // judges the request params, not the result
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		for _, c := range d.RequestCommitments() {
			if _, ok := knownCommitmentLevels[c]; !ok {
				return failf("request commitment level %q is not a known Solana commitment level", c)
			}
		}
		// Also validate the getSignaturesForAddress paging bound when supplied.
		if d.method == "getsignaturesforaddress" && len(d.reqParams) > 1 {
			if m, ok := d.reqParams[1].(map[string]any); ok {
				if lim, ok := m["limit"]; ok {
					if n, ok := anyInt64(lim); ok && n > maxSignaturesPerPage {
						return failf("request limit %d exceeds protocol bound %d", n, maxSignaturesPerPage)
					}
				}
			}
		}
		return nil
	},
}

var slotEncoding = &Check{
	ID:      "svm.shape.slotEncoding",
	Family:  FamilyShape,
	Class:   Deterministic,
	Methods: []string{"getblocks", "getblockswithlimit", "getslot", "getblockheight", "gettransactioncount", "getepochinfo"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		switch d.method {
		case "getblocks", "getblockswithlimit":
			list, err := d.BlocksList()
			if err != nil {
				return Skipped
			}
			for i := 1; i < len(list); i++ {
				if list[i] <= list[i-1] {
					return failf("getBlocks result not strictly increasing at index %d (%d <= %d)", i, list[i], list[i-1])
				}
			}
		case "getslot", "getblockheight", "gettransactioncount":
			if _, ok := d.SlotNumber(); !ok {
				return failf("result is not a numeric slot value: %.80s", string(d.raw))
			}
		case "getepochinfo":
			e, err := d.EpochInfo()
			if err != nil || e == nil {
				return Skipped
			}
			for name, v := range map[string]*int64{
				"absoluteSlot": e.AbsoluteSlot, "blockHeight": e.BlockHeight,
				"slotIndex": e.SlotIndex, "epoch": e.Epoch,
			} {
				if v != nil && *v < 0 {
					return failf("getEpochInfo %s is negative", name)
				}
			}
			if e.AbsoluteSlot != nil && e.BlockHeight != nil && *e.AbsoluteSlot < *e.BlockHeight {
				return failf("absoluteSlot %d < blockHeight %d", *e.AbsoluteSlot, *e.BlockHeight)
			}
		}
		return nil
	},
}

func envelopeMethodList() []string {
	out := make([]string, 0, len(contextSlotMethods))
	for m := range contextSlotMethods {
		out = append(out, m)
	}
	return out
}

func anyInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
