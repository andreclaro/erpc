package svm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/erpc/erpc/common"
)

type SubscriptionOperation uint8

const (
	SubscriptionOperationNone SubscriptionOperation = iota
	SubscriptionOperationSubscribe
	SubscriptionOperationUnsubscribe
)

type SubscriptionDescriptor struct {
	Family                 string
	MetricKind             string
	SubscribeMethod        string
	UnsubscribeMethod      string
	NotificationMethod     string
	CommitmentOptionsIndex int
	ClampProcessedDefault  bool
	TerminalSignature      bool
	Known                  bool
}

var subscriptionDescriptors = map[string]SubscriptionDescriptor{
	"accountSubscribe": {
		Family: "account", MetricKind: "account",
		SubscribeMethod: "accountSubscribe", UnsubscribeMethod: "accountUnsubscribe", NotificationMethod: "accountNotification",
		CommitmentOptionsIndex: 1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"accountUnsubscribe": {
		Family: "account", MetricKind: "account",
		SubscribeMethod: "accountSubscribe", UnsubscribeMethod: "accountUnsubscribe", NotificationMethod: "accountNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"programSubscribe": {
		Family: "program", MetricKind: "program",
		SubscribeMethod: "programSubscribe", UnsubscribeMethod: "programUnsubscribe", NotificationMethod: "programNotification",
		CommitmentOptionsIndex: 1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"programUnsubscribe": {
		Family: "program", MetricKind: "program",
		SubscribeMethod: "programSubscribe", UnsubscribeMethod: "programUnsubscribe", NotificationMethod: "programNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"logsSubscribe": {
		Family: "logs", MetricKind: "logs",
		SubscribeMethod: "logsSubscribe", UnsubscribeMethod: "logsUnsubscribe", NotificationMethod: "logsNotification",
		CommitmentOptionsIndex: 1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"logsUnsubscribe": {
		Family: "logs", MetricKind: "logs",
		SubscribeMethod: "logsSubscribe", UnsubscribeMethod: "logsUnsubscribe", NotificationMethod: "logsNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"signatureSubscribe": {
		Family: "signature", MetricKind: "signature",
		SubscribeMethod: "signatureSubscribe", UnsubscribeMethod: "signatureUnsubscribe", NotificationMethod: "signatureNotification",
		CommitmentOptionsIndex: 1, ClampProcessedDefault: false, TerminalSignature: true, Known: true,
	},
	"signatureUnsubscribe": {
		Family: "signature", MetricKind: "signature",
		SubscribeMethod: "signatureSubscribe", UnsubscribeMethod: "signatureUnsubscribe", NotificationMethod: "signatureNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"blockSubscribe": {
		Family: "block", MetricKind: "block",
		SubscribeMethod: "blockSubscribe", UnsubscribeMethod: "blockUnsubscribe", NotificationMethod: "blockNotification",
		CommitmentOptionsIndex: 1, ClampProcessedDefault: true, TerminalSignature: false, Known: true,
	},
	"blockUnsubscribe": {
		Family: "block", MetricKind: "block",
		SubscribeMethod: "blockSubscribe", UnsubscribeMethod: "blockUnsubscribe", NotificationMethod: "blockNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"slotSubscribe": {
		Family: "slot", MetricKind: "slot",
		SubscribeMethod: "slotSubscribe", UnsubscribeMethod: "slotUnsubscribe", NotificationMethod: "slotNotification",
		CommitmentOptionsIndex: -1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"slotUnsubscribe": {
		Family: "slot", MetricKind: "slot",
		SubscribeMethod: "slotSubscribe", UnsubscribeMethod: "slotUnsubscribe", NotificationMethod: "slotNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"slotsUpdatesSubscribe": {
		Family: "slotsUpdates", MetricKind: "slotsUpdates",
		SubscribeMethod: "slotsUpdatesSubscribe", UnsubscribeMethod: "slotsUpdatesUnsubscribe", NotificationMethod: "slotsUpdatesNotification",
		CommitmentOptionsIndex: -1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"slotsUpdatesUnsubscribe": {
		Family: "slotsUpdates", MetricKind: "slotsUpdates",
		SubscribeMethod: "slotsUpdatesSubscribe", UnsubscribeMethod: "slotsUpdatesUnsubscribe", NotificationMethod: "slotsUpdatesNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"rootSubscribe": {
		Family: "root", MetricKind: "root",
		SubscribeMethod: "rootSubscribe", UnsubscribeMethod: "rootUnsubscribe", NotificationMethod: "rootNotification",
		CommitmentOptionsIndex: -1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"rootUnsubscribe": {
		Family: "root", MetricKind: "root",
		SubscribeMethod: "rootSubscribe", UnsubscribeMethod: "rootUnsubscribe", NotificationMethod: "rootNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
	"voteSubscribe": {
		Family: "vote", MetricKind: "vote",
		SubscribeMethod: "voteSubscribe", UnsubscribeMethod: "voteUnsubscribe", NotificationMethod: "voteNotification",
		CommitmentOptionsIndex: -1, ClampProcessedDefault: false, TerminalSignature: false, Known: true,
	},
	"voteUnsubscribe": {
		Family: "vote", MetricKind: "vote",
		SubscribeMethod: "voteSubscribe", UnsubscribeMethod: "voteUnsubscribe", NotificationMethod: "voteNotification",
		CommitmentOptionsIndex: -1, Known: true,
	},
}

func DescribeSubscriptionMethod(method string) (SubscriptionOperation, SubscriptionDescriptor, bool) {
	if d, ok := subscriptionDescriptors[method]; ok {
		if strings.HasSuffix(method, "Subscribe") {
			return SubscriptionOperationSubscribe, d, true
		}
		return SubscriptionOperationUnsubscribe, d, true
	}
	if strings.HasSuffix(method, "Subscribe") {
		stem := strings.TrimSuffix(method, "Subscribe")
		return SubscriptionOperationSubscribe, SubscriptionDescriptor{
			Family:            stem,
			MetricKind:        "custom",
			SubscribeMethod:   method,
			UnsubscribeMethod: stem + "Unsubscribe",
			Known:             false,
		}, true
	}
	if strings.HasSuffix(method, "Unsubscribe") {
		stem := strings.TrimSuffix(method, "Unsubscribe")
		return SubscriptionOperationUnsubscribe, SubscriptionDescriptor{
			Family:            stem,
			MetricKind:        "custom",
			SubscribeMethod:   stem + "Subscribe",
			UnsubscribeMethod: method,
			Known:             false,
		}, true
	}
	return SubscriptionOperationNone, SubscriptionDescriptor{}, false
}

func ValidateSubscriptionRequestID(ctx context.Context, nq *common.NormalizedRequest) error {
	handled, _, err := projectPreForward_negativeId(ctx, nq)
	if handled {
		return err
	}
	return nil
}

func InvalidSubscriptionID() error {
	return common.NewErrJsonRpcExceptionInternal(
		0,
		common.JsonRpcErrorInvalidArgument,
		"Invalid subscription id.",
		nil,
		nil,
	)
}

func ParseLocalSubscriptionID(jr *common.JsonRpcRequest) (uint64, error) {
	var rawParams []json.RawMessage
	if lit := jr.ParamsLiteral(); len(lit) > 0 {
		if err := common.SonicCfg.Unmarshal(lit, &rawParams); err != nil {
			return 0, InvalidSubscriptionID()
		}
	} else {
		b, err := common.SonicCfg.Marshal(jr.Params)
		if err != nil {
			return 0, InvalidSubscriptionID()
		}
		if err := common.SonicCfg.Unmarshal(b, &rawParams); err != nil {
			return 0, InvalidSubscriptionID()
		}
	}
	if len(rawParams) != 1 {
		return 0, InvalidSubscriptionID()
	}
	text := strings.TrimSpace(string(rawParams[0]))
	id, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, InvalidSubscriptionID()
	}
	return id, nil
}

func InjectSubscriptionCommitment(n common.Network, nq *common.NormalizedRequest, d SubscriptionDescriptor) error {
	if d.CommitmentOptionsIndex < 0 {
		return nil
	}
	cfg := n.Config()
	if cfg == nil || cfg.Svm == nil || cfg.Svm.Commitment == "" {
		return nil
	}
	def := strings.ToLower(cfg.Svm.Commitment)
	if def != "finalized" && def != "confirmed" && def != "processed" {
		return nil
	}
	def = clampCommitmentForMethod(d.SubscribeMethod, def)

	ctx := context.Background()
	rpcReq, err := nq.JsonRpcRequest(ctx)
	if err != nil {
		return nil
	}

	rpcReq.RLock()
	params := make([]interface{}, len(rpcReq.Params))
	copy(params, rpcReq.Params)
	rpcReq.RUnlock()

	for _, p := range params {
		if m, ok := p.(map[string]interface{}); ok {
			if v, ok := m["commitment"].(string); ok && v != "" {
				return nil
			}
		}
	}

	rpcReq.Lock()
	defer rpcReq.Unlock()

	idx := d.CommitmentOptionsIndex
	switch {
	case idx < len(rpcReq.Params):
		if m, ok := rpcReq.Params[idx].(map[string]interface{}); ok {
			m["commitment"] = def
			rpcReq.InvalidateCacheHash()
		}
	case idx == len(rpcReq.Params):
		rpcReq.Params = append(rpcReq.Params, map[string]interface{}{"commitment": def})
		rpcReq.InvalidateCacheHash()
	}
	return nil
}

func IsTerminalSignatureNotification(_ SubscriptionDescriptor, params json.RawMessage) bool {
	var envelope struct {
		Result struct {
			Value interface{} `json:"value"`
		} `json:"result"`
	}
	if err := common.SonicCfg.Unmarshal(params, &envelope); err != nil {
		return false
	}
	switch v := envelope.Result.Value.(type) {
	case string:
		return false
	case map[string]interface{}:
		_, ok := v["err"]
		return ok
	}
	return false
}
