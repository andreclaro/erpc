package erpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/erpc/erpc/clients"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/indexer/adapters/wsclient"
	"github.com/erpc/erpc/telemetry"
	"github.com/gorilla/websocket"
)

type httpHeader = http.Header

// svmDelivery gates SVM notifications behind the subscribe acknowledgment.
type svmDelivery struct {
	wsc   *WsConnection
	sub   wsclient.Subscription
	notif *wsNotifier

	mu            sync.Mutex
	open          bool
	closed        bool
	pending       []wsOut
	pendingWeight int64
}

func newSvmDelivery(wsc *WsConnection, sub wsclient.Subscription) *svmDelivery {
	labels := []string{wsc.project.Config.Id, wsc.networkLabel, sub.MetricKind}
	return &svmDelivery{
		wsc:   wsc,
		sub:   sub,
		notif: &wsNotifier{wsc: wsc, sub: sub, sent: telemetry.BindCounter(telemetry.MetricWebsocketNotificationsSentTotal, labels...), bytes: telemetry.BindCounter(telemetry.MetricWebsocketNotificationBytesTotal, labels...)},
	}
}

// Offer rewrites one upstream notification with the downstream subscription id
// and offers it to the writer. It never blocks. A terminal notification
// returns a buffered channel that receives the write outcome.
func (d *svmDelivery) Offer(n clients.WsSubscriptionNotification, terminal bool) (<-chan error, error) {
	if n.SubscriptionID == "" {
		return nil, errors.New("missing upstream subscription id")
	}
	frame, err := d.rewrite(n)
	if err != nil {
		return nil, err
	}

	costs := d.wsc.svmNotificationCostBlock(d.sub.NotificationMethod, d.sub.Family)
	msg, err := d.wsc.encodeMessage(costs, func(w io.Writer) error {
		_, err := w.Write(frame)
		return err
	})
	if err != nil {
		return nil, err
	}

	out := wsOut{
		msg:              msg,
		note:             d.notif,
		size:             len(frame),
		preserveOnRemove: terminal,
	}
	var done chan error
	if terminal {
		done = make(chan error, 1)
		out.done = done
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		releaseMessage(msg)
		return nil, errWsConnClosed
	}
	if d.open {
		d.mu.Unlock()
		err := d.wsc.tryQueue(out)
		return done, err
	}
	weight := min(max(int64(msg.Len()), wsOutBytes/wsOutCap), wsOutBytes)
	if int64(len(d.pending)) >= wsOutCap || d.pendingWeight+weight > wsOutBytes {
		d.mu.Unlock()
		releaseMessage(msg)
		d.wsc.stopAsync(websocket.CloseTryAgainLater, "subscription backpressure; reconnect and resubscribe")
		return nil, errWsQueueFull
	}
	d.pending = append(d.pending, out)
	d.pendingWeight += weight
	d.mu.Unlock()
	return done, nil
}

// Open releases pending frames in order after the acknowledgment write.
func (d *svmDelivery) Open() error {
	d.mu.Lock()
	d.open = true
	pending := d.pending
	d.pending = nil
	d.pendingWeight = 0
	d.mu.Unlock()

	var firstErr error
	for _, out := range pending {
		if err := d.wsc.tryQueue(out); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close stops the delivery from accepting new frames and drops pending ones.
func (d *svmDelivery) Close() {
	d.mu.Lock()
	d.closed = true
	pending := d.pending
	d.pending = nil
	d.pendingWeight = 0
	d.mu.Unlock()
	for _, out := range pending {
		releaseMessage(out.msg)
	}
}

// rewrite replaces only params.subscription with the downstream id.
func (d *svmDelivery) rewrite(n clients.WsSubscriptionNotification) ([]byte, error) {
	var outer map[string]json.RawMessage
	if err := common.SonicCfg.Unmarshal(n.Raw, &outer); err != nil {
		return nil, err
	}
	paramsRaw, ok := outer["params"]
	if !ok {
		return nil, errors.New("notification has no params")
	}
	var params map[string]json.RawMessage
	if err := common.SonicCfg.Unmarshal(paramsRaw, &params); err != nil {
		return nil, err
	}
	params["subscription"] = json.RawMessage(d.sub.ClientSubIDJSON)
	encodedParams, err := common.SonicCfg.Marshal(params)
	if err != nil {
		return nil, err
	}
	outer["params"] = encodedParams
	return common.SonicCfg.Marshal(outer)
}

// svmNotificationCostBlock builds the cost header block for an SVM
// notification. It is computed per message for unknown notification methods;
// known methods are cached in Task 9.
func (wsc *WsConnection) svmNotificationCostBlock(method, family string) []byte {
	h := make(httpHeader)
	h.Set("X-ERPC-Calls", "1")
	h.Set("X-ERPC-Billable", "1")
	h.Set("X-ERPC-Methods", method)
	h.Set("X-ERPC-Subscription", family)
	h.Set("X-ERPC-Network-Id", wsc.networkId)
	if wsc.networkLabel != "" {
		h.Set("X-ERPC-Network-Alias", wsc.networkLabel)
	}
	var buf bytes.Buffer
	_ = h.Write(&buf)
	return buf.Bytes()
}
