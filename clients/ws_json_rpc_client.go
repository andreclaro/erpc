package clients

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/telemetry"
	"github.com/erpc/erpc/util"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	wsWriteWait       = 10 * time.Second
	wsReconnectMin    = 1 * time.Second
	wsReconnectMax    = 30 * time.Second
	wsReconnectFactor = 2.0
)

// Liveness windows: a connection with no pong or data frame for wsPongWait
// is torn down and re-dialed, since a half-open connection would otherwise
// block reads forever while pings keep "succeeding" into a buffer. Two
// pings fit in the window. A request that times out shortens the window
// to wsProbeWait and pings: a live peer answers well within it, and a
// half-open one fails its pending requests over seconds after the
// timeout, not at the end of wsPongWait. Vars so tests can shorten them.
var (
	wsPingInterval = 30 * time.Second
	wsPongWait     = 75 * time.Second
	wsProbeWait    = 5 * time.Second
)

// WsJsonRpcClient implements ClientInterface for WebSocket-based JSON-RPC upstream connections.
type WsJsonRpcClient struct {
	Url     *url.URL
	headers http.Header

	projectId string
	upstream  common.Upstream
	appCtx    context.Context
	logger    *zerolog.Logger

	pingInterval time.Duration
	pongWait     time.Duration
	probeWait    time.Duration

	// Connection state. Only readLoop replaces or tears down conn (besides
	// the initial dial and shutdown); the dial itself runs without connMu so
	// requests never wait on a handshake.
	connMu      sync.Mutex
	conn        *websocket.Conn
	connectedAt time.Time
	// epoch identifies the current connection (0 while disconnected);
	// written under connMu, epochs counts connections ever installed.
	epoch  atomic.Uint64
	epochs uint64
	// probing is the connection whose liveness probe is armed; a frame or
	// pong on it disarms it. Later timeouts on it never re-arm the probe,
	// so a stream of them cannot push the probe's deadline back.
	probing atomic.Pointer[websocket.Conn]

	// Write synchronization (gorilla/websocket requires synchronized writes)
	writeMu sync.Mutex

	// Pending request tracking: wire id -> request. Whoever deletes an
	// entry owns it, so duplicate responses are dropped.
	pendingMu sync.Mutex
	pending   map[string]*wsPending

	// Subscription notification callbacks: upstreamSubID -> route, for
	// the current connection only (ids are connection-scoped).
	subHandlersMu sync.RWMutex
	subHandlers   map[string]*wsSubscriptionRoute

	// Disconnect/reconnect callbacks, keyed by caller-supplied id so a
	// caller registering again replaces its callback.
	onDisconnectMu  sync.RWMutex
	onDisconnectCbs map[string]func()

	onReconnectMu  sync.RWMutex
	onReconnectCbs map[string]func()

	errorExtractor common.JsonRpcErrorExtractor

	// metricLabels are the labels the connectivity gauge was last set under.
	metricMu     sync.Mutex
	metricLabels []string

	// wireIDCounter generates the ids sent on the wire, so concurrent
	// requests with the same caller id don't collide in pending. The
	// caller's id is restored on the response.
	wireIDCounter atomic.Uint64
}

type wsSubscriptionRoute struct {
	handlers           map[string]func(WsSubscriptionNotification)
	onEvent            func(WsSubscriptionNotification)
	unsubscribeMethod  string
	notificationMethod string
	codec              WsSubscriptionIDCodec
	terminalCheck      func(json.RawMessage) bool
	terminal           atomic.Bool
}

type WsSubscriptionIDCodec uint8

const (
	WsSubscriptionIDString WsSubscriptionIDCodec = iota
	WsSubscriptionIDUint64
)

// WsSubscriptionNotification carries one notification frame from the upstream.
type WsSubscriptionNotification struct {
	SubscriptionID string
	Method         string
	Params         json.RawMessage
	Raw            json.RawMessage
}

// WsSubscription ties a subscribe or unsubscribe request to the connection it
// is scoped to. The client only sends these methods when the request context
// carries one (see WithWsSubscription): a subscription created by a routed
// request would have no owner to receive or cancel it.
type WsSubscription struct {
	// Handler receives the subscription's notifications. It is registered
	// before the next frame is read, so nothing sent right after the
	// subscribe response is missed.
	Handler func(WsSubscriptionNotification)
	// OnEvent runs once per resolved upstream frame before owner fanout.
	OnEvent func(WsSubscriptionNotification)
	// OwnerID identifies one local route within an upstream subscription ID's
	// handler set. It must be unique among concurrent owners of the same ID.
	OwnerID string
	// ID and Epoch are set when a subscribe succeeds. Unsubscribe must carry
	// the Epoch of the subscription it cancels; it is not sent once that
	// connection is gone, since ids are reused across connections.
	ID    string
	Epoch uint64
	// IDCodec selects how the upstream ID is encoded in subscribe results,
	// unsubscribe params, and notification params.
	IDCodec WsSubscriptionIDCodec
	// UnsubscribeMethod is the actual unsubscribe method to send upstream.
	UnsubscribeMethod string
	// NotificationMethod is the expected notification method name.
	NotificationMethod string
	// TerminalCheck returns true when a notification ends the subscription
	// naturally (e.g. a completed signature subscription).
	TerminalCheck func(json.RawMessage) bool
}

type wsSubscriptionCtxKey struct{}

// WithWsSubscription returns ctx carrying sub for an eth_subscribe or
// eth_unsubscribe sent through SendRequest.
func WithWsSubscription(ctx context.Context, sub *WsSubscription) context.Context {
	return context.WithValue(ctx, wsSubscriptionCtxKey{}, sub)
}

type wsPending struct {
	ch chan *wsPendingResult
	// sub is set for eth_subscribe. If the caller gives up, the entry is
	// kept (abandoned, guarded by pendingMu) so a late success is
	// unsubscribed instead of leaked.
	sub       *WsSubscription
	abandoned bool
}

type wsPendingResult struct {
	message []byte
	err     error
}

// wsMessage holds the fields that tell a response (id) from a notification
// (method).
type wsMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type wsNotificationParams struct {
	Subscription string          `json:"subscription"`
	Result       json.RawMessage `json:"result"`
}

func NewWsJsonRpcClient(
	appCtx context.Context,
	logger *zerolog.Logger,
	projectId string,
	upstream common.Upstream,
	parsedUrl *url.URL,
	jsonRpcCfg *common.JsonRpcUpstreamConfig,
	extractor common.JsonRpcErrorExtractor,
) (ClientInterface, error) {
	headers := http.Header{}
	if jsonRpcCfg != nil {
		for k, v := range jsonRpcCfg.Headers {
			headers.Set(k, v)
		}
	}

	client := &WsJsonRpcClient{
		Url:             parsedUrl,
		headers:         headers,
		pingInterval:    wsPingInterval,
		pongWait:        wsPongWait,
		probeWait:       wsProbeWait,
		projectId:       projectId,
		upstream:        upstream,
		appCtx:          appCtx,
		logger:          logger,
		pending:         make(map[string]*wsPending),
		subHandlers:     make(map[string]*wsSubscriptionRoute),
		onDisconnectCbs: make(map[string]func()),
		onReconnectCbs:  make(map[string]func()),
		errorExtractor:  extractor,
	}

	if err := client.connect(); err != nil {
		// readLoop keeps re-dialing in the background.
		logger.Warn().Err(err).Str("url", util.RedactEndpoint(parsedUrl.String())).Msg("initial websocket connection failed, will retry in background")
	}

	go client.readLoop()
	go client.pingLoop()
	go func() {
		<-appCtx.Done()
		client.shutdown()
	}()

	return client, nil
}

func (c *WsJsonRpcClient) GetType() ClientType {
	return ClientTypeWsJsonRpc
}

func (c *WsJsonRpcClient) IsConnected() bool {
	return c.epoch.Load() != 0
}

// Epoch identifies the current connection, or is 0 while disconnected. It
// changes on every reconnect.
func (c *WsJsonRpcClient) Epoch() uint64 {
	return c.epoch.Load()
}

// Done is closed once the client has shut down for good.
func (c *WsJsonRpcClient) Done() <-chan struct{} { return c.appCtx.Done() }

// Reconnect closes connection epoch, if still current; readLoop then tears
// it down and re-dials, firing the disconnect and reconnect callbacks.
func (c *WsJsonRpcClient) Reconnect(epoch uint64) {
	c.connMu.Lock()
	conn := c.conn
	current := conn != nil && c.epoch.Load() == epoch
	c.connMu.Unlock()
	if current {
		_ = conn.Close()
	}
}

func (c *WsJsonRpcClient) SendRequest(ctx context.Context, req *common.NormalizedRequest) (*common.NormalizedResponse, error) {
	ctx, span := common.StartDetailSpan(ctx, "WsJsonRpcClient.SendRequest",
		trace.WithAttributes(
			attribute.String("upstream.id", c.upstream.Id()),
		),
	)
	defer span.End()

	startedAt := time.Now()

	jrReq, err := req.JsonRpcRequest()
	if err != nil {
		return nil, common.NewErrUpstreamRequest(
			err,
			c.upstream,
			req.NetworkId(),
			"",
			0, 0, 0, 0,
		)
	}

	wireID := c.wireIDCounter.Add(1)
	idKey := strconv.FormatUint(wireID, 10)

	// Serialize the JSON-RPC request with the rewritten wire id
	jrReq.RLock()
	originalID := jrReq.ID
	method := jrReq.Method
	requestBody, err := common.SonicCfg.Marshal(map[string]interface{}{
		"jsonrpc": jrReq.JSONRPC,
		"id":      wireID,
		"method":  method,
		"params":  common.WireParams(ctx, req, jrReq.Params),
	})
	jrReq.RUnlock()
	if err != nil {
		common.SetTraceSpanError(span, err)
		return nil, common.NewErrUpstreamRequest(
			err,
			c.upstream,
			req.NetworkId(),
			method,
			0, 0, 0, 0,
		)
	}

	pending := &wsPending{ch: make(chan *wsPendingResult, 1)}
	var unsubscribe *WsSubscription
	sub, _ := ctx.Value(wsSubscriptionCtxKey{}).(*WsSubscription)
	if sub != nil {
		if sub.Handler != nil {
			pending.sub = sub
		} else {
			unsubscribe = sub
		}
	} else if method == "eth_subscribe" || method == "eth_unsubscribe" ||
		(c.upstream.Config() != nil && c.upstream.Config().Type == common.UpstreamTypeSvm &&
			(strings.HasSuffix(method, "Subscribe") || strings.HasSuffix(method, "Unsubscribe"))) {
		return nil, common.NewErrUpstreamRequestSkipped(
			fmt.Errorf("%s requires an upstream subscription owner", method),
			c.upstream.Id(),
		)
	}

	// Register under connMu so the entry belongs to exactly this conn:
	// teardown swaps conn and drains pending under the same lock.
	c.connMu.Lock()
	conn := c.conn
	stale := unsubscribe != nil && unsubscribe.Epoch != c.epoch.Load()
	if conn != nil && !stale {
		c.pendingMu.Lock()
		c.pending[idKey] = pending
		c.pendingMu.Unlock()
	}
	c.connMu.Unlock()

	if stale {
		// The subscription died with its connection; its id may since have
		// been reused on the new one.
		return nil, common.NewErrUpstreamRequestSkipped(errors.New("subscription's connection is already closed"), c.upstream.Id())
	}
	if conn == nil {
		// Re-dial in progress: fail fast so the request fails over.
		err := common.NewErrEndpointTransportFailure(c.Url, errors.New("websocket connection not established"))
		common.SetTraceSpanError(span, err)
		return nil, err
	}

	if err := c.writeToConn(conn, websocket.TextMessage, requestBody); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
		common.SetTraceSpanError(span, err)
		return nil, common.NewErrEndpointTransportFailure(c.Url, err)
	}

	c.logger.Debug().
		Str("host", c.Url.Host).
		RawJSON("request", requestBody).
		Msg("sent json rpc websocket request")

	// Wait for response. A caller that gives up after readLoop or teardown
	// already claimed the entry takes the result anyway: it is on its way,
	// and dropping it would strand a subscribe it created.
	var result *wsPendingResult
	select {
	case result = <-pending.ch:
	case <-ctx.Done():
		if c.abandonPending(idKey) {
			err := ctx.Err()
			if errors.Is(err, context.DeadlineExceeded) {
				err = common.NewErrEndpointRequestTimeout(time.Since(startedAt), err)
				// The write may block on a full send buffer; the caller
				// has waited long enough.
				go c.probe(conn)
			} else if errors.Is(err, context.Canceled) {
				err = common.NewErrEndpointRequestCanceled(err)
			}
			common.SetTraceSpanError(span, err)
			return nil, err
		}
		result = <-pending.ch
	case <-c.appCtx.Done():
		if c.abandonPending(idKey) {
			return nil, common.NewErrEndpointRequestCanceled(c.appCtx.Err())
		}
		result = <-pending.ch
	}
	if result.err != nil {
		common.SetTraceSpanError(span, result.err)
		return nil, result.err
	}
	nr := common.NewNormalizedResponse().WithRequest(req).WithBody(io.NopCloser(bytes.NewReader(result.message)))
	// Restore the caller's original JSON-RPC id on the response.
	jrr, perr := nr.JsonRpcResponse(ctx)
	if perr == nil && jrr != nil {
		_ = jrr.SetID(originalID)
	}
	if err := classifyJsonRpcError(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, nr, jrr, perr, c.errorExtractor, c.upstream); err != nil {
		common.SetTraceSpanError(span, err)
		return nr, err
	}
	return nr, nil
}

// abandonPending drops a request whose caller gave up. A subscribe stays
// registered so readLoop can cancel it if the upstream still creates it.
func (c *WsJsonRpcClient) abandonPending(idKey string) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	p, ok := c.pending[idKey]
	if ok {
		if p.sub != nil {
			p.abandoned = true
		} else {
			delete(c.pending, idKey)
		}
	}
	return ok
}

// UnregisterSubscriptionHandler removes the handler of an EVM subscription
// made on connection epoch. A no-op once that connection is gone.
func (c *WsJsonRpcClient) UnregisterSubscriptionHandler(upstreamSubID string, epoch uint64) {
	c.UnregisterSubscriptionHandlerOwner("", upstreamSubID, epoch)
}

// UnregisterSubscriptionHandlerOwner removes one owner from the upstream ID's
// handler set. It returns the number of remaining owners.
func (c *WsJsonRpcClient) UnregisterSubscriptionHandlerOwner(ownerID, upstreamSubID string, epoch uint64) int {
	c.subHandlersMu.Lock()
	defer c.subHandlersMu.Unlock()
	if c.epoch.Load() != epoch {
		return 0
	}
	route, ok := c.subHandlers[upstreamSubID]
	if !ok {
		return 0
	}
	delete(route.handlers, ownerID)
	remaining := len(route.handlers)
	if remaining == 0 {
		delete(c.subHandlers, upstreamSubID)
	}
	return remaining
}

// SubscriptionHandlerCount returns the number of active owners for the given
// upstream ID on the current epoch.
func (c *WsJsonRpcClient) SubscriptionHandlerCount(upstreamSubID string, epoch uint64) int {
	c.subHandlersMu.RLock()
	defer c.subHandlersMu.RUnlock()
	if c.epoch.Load() != epoch {
		return 0
	}
	if route, ok := c.subHandlers[upstreamSubID]; ok {
		return len(route.handlers)
	}
	return 0
}

// SetOnDisconnect sets the callback under id that runs when the connection
// drops, shutdown included.
func (c *WsJsonRpcClient) SetOnDisconnect(id string, callback func()) {
	c.onDisconnectMu.Lock()
	c.onDisconnectCbs[id] = callback
	c.onDisconnectMu.Unlock()
}

// RemoveOnDisconnect removes the callback under id.
func (c *WsJsonRpcClient) RemoveOnDisconnect(id string) {
	c.onDisconnectMu.Lock()
	delete(c.onDisconnectCbs, id)
	c.onDisconnectMu.Unlock()
}

// SetOnReconnect sets the callback under id that runs after a reconnect.
func (c *WsJsonRpcClient) SetOnReconnect(id string, callback func()) {
	c.onReconnectMu.Lock()
	c.onReconnectCbs[id] = callback
	c.onReconnectMu.Unlock()
}

func (c *WsJsonRpcClient) connect() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	if c.Url.Scheme == "wss" {
		dialer.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	conn, _, err := dialer.DialContext(c.appCtx, c.Url.String(), c.headers)
	if err != nil {
		return err
	}

	// Every pong (here) and data frame (in readLoop) extends the liveness
	// deadline and disarms a probe.
	_ = conn.SetReadDeadline(time.Now().Add(c.pongWait))
	conn.SetPongHandler(func(string) error {
		c.probing.CompareAndSwap(conn, nil)
		return conn.SetReadDeadline(time.Now().Add(c.pongWait))
	})

	c.connMu.Lock()
	if c.appCtx.Err() != nil {
		// shutdown already ran; don't install a connection behind it.
		c.connMu.Unlock()
		_ = conn.Close()
		return c.appCtx.Err()
	}
	c.conn = conn
	c.connectedAt = time.Now()
	c.epochs++
	c.epoch.Store(c.epochs)
	c.connMu.Unlock()
	c.setConnectedMetric()

	c.logger.Info().Str("url", util.RedactEndpoint(c.Url.String())).Msg("websocket connection established")
	return nil
}

// teardownConn closes conn, marks the client disconnected, fails every
// request pending on it and drops its subscription handlers.
func (c *WsJsonRpcClient) teardownConn(conn *websocket.Conn, cause error) {
	c.connMu.Lock()
	c.conn = nil
	c.epoch.Store(0)
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]*wsPending)
	c.pendingMu.Unlock()
	c.connMu.Unlock()

	if conn != nil {
		c.probing.CompareAndSwap(conn, nil)
		_ = conn.Close()
	}
	c.subHandlersMu.Lock()
	clear(c.subHandlers)
	c.subHandlersMu.Unlock()
	c.setConnectedMetric()
	for _, p := range pending {
		p.ch <- &wsPendingResult{err: cause}
	}
}

// setConnectedMetric publishes the connectivity gauge. Labels are resolved
// on every publish (pingLoop republishes each tick) because the upstream's
// network label is assigned after the client is built; the series under
// the previous labels is deleted when they change.
func (c *WsJsonRpcClient) setConnectedMetric() {
	labels := []string{c.projectId, c.upstream.VendorName(), c.upstream.NetworkLabel(), c.upstream.Id()}

	c.metricMu.Lock()
	defer c.metricMu.Unlock()
	if c.metricLabels != nil && !slices.Equal(c.metricLabels, labels) {
		telemetry.MetricUpstreamWebsocketConnected.DeleteLabelValues(c.metricLabels...)
	}
	c.metricLabels = labels
	v := 0.0
	if c.IsConnected() {
		v = 1
	}
	telemetry.MetricUpstreamWebsocketConnected.WithLabelValues(labels...).Set(v)
}

// readLoop owns the connection lifecycle: it reads the current connection
// and, when there is none, re-dials with backoff. pingLoop only closes a
// broken connection, so a nil conn always has a re-dial behind it.
func (c *WsJsonRpcClient) readLoop() {
	backoff := wsReconnectMin
	for {
		if c.appCtx.Err() != nil {
			return
		}

		c.connMu.Lock()
		conn, connectedAt, epoch := c.conn, c.connectedAt, c.epoch.Load()
		c.connMu.Unlock()

		if conn == nil {
			// Always wait before dialing so a peer that accepts and then
			// immediately drops the connection can't drive a hot loop.
			wait := backoff/2 + rand.N(backoff/2+1) // #nosec G404 -- reconnect jitter, not security-sensitive
			backoff = min(time.Duration(float64(backoff)*wsReconnectFactor), wsReconnectMax)
			c.logger.Info().Dur("backoff", wait).Msg("attempting websocket reconnection")
			select {
			case <-time.After(wait):
			case <-c.appCtx.Done():
				return
			}
			if err := c.connect(); err != nil {
				c.logger.Warn().Err(err).Msg("websocket reconnection failed")
				continue
			}
			c.logger.Info().Msg("websocket reconnected successfully")
			c.fireCallbacks(&c.onReconnectMu, c.onReconnectCbs)
			continue
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if c.appCtx.Err() != nil {
				return
			}
			var netErr net.Error
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Info().Msg("websocket connection closed normally")
			} else if errors.As(err, &netErr) && netErr.Timeout() {
				c.logger.Warn().Err(err).Dur("pongWait", c.pongWait).
					Msg("websocket peer silent beyond liveness deadline, reconnecting")
			} else {
				c.logger.Warn().Err(err).Msg("websocket read error, will reconnect")
			}
			// Only a connection that outlived a liveness window resets the
			// backoff; one dropped right after the handshake keeps growing it.
			if time.Since(connectedAt) >= c.pongWait {
				backoff = wsReconnectMin
			}
			c.teardownConn(conn, common.NewErrEndpointTransportFailure(c.Url, fmt.Errorf("websocket connection lost: %w", err)))
			c.fireCallbacks(&c.onDisconnectMu, c.onDisconnectCbs)
			continue
		}

		c.probing.CompareAndSwap(conn, nil)
		_ = conn.SetReadDeadline(time.Now().Add(c.pongWait))

		c.handleMessage(message, epoch)
	}
}

// fireCallbacks snapshots the callback map under rlock and invokes each
// callback synchronously, so disconnect callbacks always complete before
// the reconnect callbacks of the next connection run. Callbacks must
// therefore be fast and must not block on the WS client's request path.
func (c *WsJsonRpcClient) fireCallbacks(mu *sync.RWMutex, cbs map[string]func()) {
	mu.RLock()
	snapshot := make([]func(), 0, len(cbs))
	for _, cb := range cbs {
		snapshot = append(snapshot, cb)
	}
	mu.RUnlock()
	for _, cb := range snapshot {
		func() {
			defer c.recoverPanic("ws-upstream-callback")
			cb()
		}()
	}
}

// recoverPanic keeps a panic in a callback or a message handler from
// crashing the process. The read loop carries on with the next frame: the
// panic comes from the handler, not the connection, so reconnecting would
// only repeat it.
func (c *WsJsonRpcClient) recoverPanic(scope string) {
	if rec := recover(); rec != nil {
		telemetry.MetricUnexpectedPanicTotal.WithLabelValues(
			scope,
			fmt.Sprintf("project:%s upstream:%s", c.projectId, c.upstream.Id()),
			common.ErrorFingerprint(rec),
		).Inc()
		c.logger.Error().
			Interface("panic", rec).
			Str("stack", string(debug.Stack())).
			Msg("unexpected panic in websocket upstream client")
	}
}

// handleMessage dispatches one frame read from connection epoch.
func (c *WsJsonRpcClient) handleMessage(message []byte, epoch uint64) {
	defer c.recoverPanic("ws-upstream-message")
	var msg wsMessage
	if err := common.SonicCfg.Unmarshal(message, &msg); err != nil {
		c.logger.Warn().Err(err).Str("raw", string(message)).Msg("failed to parse websocket message")
		return
	}

	if msg.Method != "" && len(msg.ID) == 0 {
		c.handleNotification(msg.Method, msg.Params, epoch, message)
		return
	}

	if len(msg.ID) > 0 {
		idKey, err := normalizeIDKey(msg.ID)
		if err != nil {
			c.logger.Debug().Str("raw", string(msg.ID)).Msg("received response with non-numeric id")
			return
		}

		c.pendingMu.Lock()
		p, ok := c.pending[idKey]
		delete(c.pending, idKey)
		abandoned := ok && p.abandoned
		c.pendingMu.Unlock()

		if !ok {
			c.logger.Debug().Str("id", idKey).Msg("received response for unknown request ID")
			return
		}
		if p.sub != nil {
			subID, valid := c.parseSubscriptionID(msg.Result, p.sub.IDCodec)
			if valid {
				subID = strings.Clone(subID)
				if abandoned {
					if c.SubscriptionHandlerCount(subID, epoch) == 0 {
						method := p.sub.UnsubscribeMethod
						if method == "" {
							method = "eth_unsubscribe"
						}
						go c.UnsubscribeOrphanMethod(subID, method, epoch, p.sub.IDCodec)
					}
					return
				}
				c.attachSubscriptionHandler(subID, epoch, p.sub)
				p.sub.ID, p.sub.Epoch = subID, epoch
			}
		}
		if !abandoned {
			p.ch <- &wsPendingResult{message: message}
		}
		return
	}

	c.logger.Debug().Str("raw", string(message)).Msg("received unhandled websocket message")
}

func (c *WsJsonRpcClient) parseSubscriptionID(result json.RawMessage, codec WsSubscriptionIDCodec) (string, bool) {
	switch codec {
	case WsSubscriptionIDUint64:
		text := string(bytes.TrimSpace(result))
		id, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(id, 10), true
	default:
		var s string
		if err := common.SonicCfg.Unmarshal(result, &s); err != nil || s == "" {
			return "", false
		}
		return s, true
	}
}

func (c *WsJsonRpcClient) attachSubscriptionHandler(subID string, epoch uint64, sub *WsSubscription) {
	c.subHandlersMu.Lock()
	defer c.subHandlersMu.Unlock()
	if c.epoch.Load() != epoch {
		return
	}
	route, exists := c.subHandlers[subID]
	if !exists {
		route = &wsSubscriptionRoute{
			handlers:           map[string]func(WsSubscriptionNotification){sub.OwnerID: sub.Handler},
			onEvent:            sub.OnEvent,
			unsubscribeMethod:  sub.UnsubscribeMethod,
			notificationMethod: sub.NotificationMethod,
			codec:              sub.IDCodec,
			terminalCheck:      sub.TerminalCheck,
		}
		c.subHandlers[subID] = route
	} else {
		route.handlers[sub.OwnerID] = sub.Handler
	}
}

func (c *WsJsonRpcClient) handleNotification(method string, params []byte, epoch uint64, raw []byte) {
	var envelope struct {
		Subscription json.RawMessage `json:"subscription"`
		Result       json.RawMessage `json:"result"`
	}
	if err := common.SonicCfg.Unmarshal(params, &envelope); err != nil {
		c.logger.Warn().Err(err).Msg("failed to parse subscription notification params")
		return
	}

	subKey, codec := parseNotificationSubscriptionID(envelope.Subscription)
	if subKey == "" {
		c.logger.Debug().Str("raw", string(envelope.Subscription)).Msg("received notification with unusable subscription id")
		return
	}

	c.subHandlersMu.RLock()
	route, ok := c.subHandlers[subKey]
	c.subHandlersMu.RUnlock()

	if !ok {
		c.logger.Debug().Str("subscriptionId", subKey).Msg("received notification for unknown subscription")
		// Nothing owns it: cancel it, unless a subscribe in flight may be
		// about to claim it. Each later notification retries the cancel.
		if !c.subscribeInFlight() {
			go c.UnsubscribeOrphanMethod(strings.Clone(subKey), "eth_unsubscribe", epoch, codec)
		}
		return
	}

	if route.terminal.Load() {
		return
	}
	if route.terminalCheck != nil && route.terminalCheck(params) {
		route.terminal.Store(true)
	}

	n := WsSubscriptionNotification{
		SubscriptionID: subKey,
		Method:         method,
		Params:         params,
		Raw:            raw,
	}
	if route.onEvent != nil {
		route.onEvent(n)
	}

	c.subHandlersMu.RLock()
	handlers := make([]func(WsSubscriptionNotification), 0, len(route.handlers))
	for _, h := range route.handlers {
		handlers = append(handlers, h)
	}
	c.subHandlersMu.RUnlock()

	for _, h := range handlers {
		if h != nil {
			h(n)
		}
	}
}

func parseNotificationSubscriptionID(raw json.RawMessage) (string, WsSubscriptionIDCodec) {
	text := string(bytes.TrimSpace(raw))
	if id, err := strconv.ParseUint(text, 10, 64); err == nil {
		return strconv.FormatUint(id, 10), WsSubscriptionIDUint64
	}
	var s string
	if err := common.SonicCfg.Unmarshal(raw, &s); err == nil && s != "" {
		return s, WsSubscriptionIDString
	}
	return "", WsSubscriptionIDString
}

// subscribeInFlight reports whether an eth_subscribe still has a caller
// waiting for its response.
func (c *WsJsonRpcClient) subscribeInFlight() bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for _, p := range c.pending {
		if p.sub != nil && !p.abandoned {
			return true
		}
	}
	return false
}

// writeToConn writes to an explicit connection so callers that need to act
// on a write failure (e.g. pingLoop closing the broken conn) operate on the
// exact connection they wrote to, not whatever c.conn points at by then.
func (c *WsJsonRpcClient) writeToConn(conn *websocket.Conn, messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
		return err
	}
	return conn.WriteMessage(messageType, data)
}

// probe pings conn and gives it probeWait to answer: a pong or any frame
// restores the liveness deadline, and a half-open peer sends neither, so
// readLoop tears the connection down and fails its pending requests. A
// probe already armed on conn keeps its deadline.
func (c *WsJsonRpcClient) probe(conn *websocket.Conn) {
	if c.probing.Swap(conn) == conn {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(min(c.probeWait, c.pongWait)))
	if err := c.writeToConn(conn, websocket.PingMessage, nil); err != nil {
		_ = conn.Close()
	}
}

// UnsubscribeOrphan cancels an EVM subscription of connection epoch that
// nobody owns any more. Fire-and-forget: the response is dropped as unknown.
func (c *WsJsonRpcClient) UnsubscribeOrphan(subID string, epoch uint64) {
	c.UnsubscribeOrphanMethod(subID, "eth_unsubscribe", epoch, WsSubscriptionIDString)
}

// UnsubscribeOrphanMethod cancels a subscription of connection epoch that
// nobody owns any more, using the actual unsubscribe method and ID codec.
func (c *WsJsonRpcClient) UnsubscribeOrphanMethod(subID, method string, epoch uint64, codec WsSubscriptionIDCodec) {
	var params interface{}
	switch codec {
	case WsSubscriptionIDUint64:
		id, err := strconv.ParseUint(subID, 10, 64)
		if err != nil {
			return
		}
		params = []uint64{id}
	default:
		params = []string{subID}
	}
	body, err := common.SonicCfg.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      c.wireIDCounter.Add(1),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return
	}
	c.connMu.Lock()
	conn := c.conn
	live := c.epoch.Load() == epoch
	c.connMu.Unlock()
	if conn != nil && live {
		_ = c.writeToConn(conn, websocket.TextMessage, body)
	}
}

func (c *WsJsonRpcClient) pingLoop() {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.setConnectedMetric()
			c.connMu.Lock()
			conn := c.conn
			c.connMu.Unlock()
			if conn == nil {
				continue
			}
			if err := c.writeToConn(conn, websocket.PingMessage, nil); err != nil {
				// Only close it: readLoop's ReadMessage then fails and it
				// tears down and re-dials. Clearing c.conn here instead would
				// leave nobody to re-dial if readLoop was busy in a handler.
				c.logger.Warn().Err(err).Msg("websocket ping write failed, closing connection to force reconnect")
				_ = conn.Close()
			}
		case <-c.appCtx.Done():
			return
		}
	}
}

// normalizeIDKey parses the raw JSON-RPC id bytes into the key used in
// pending. Only the internally generated unsigned decimal ids are valid keys.
func normalizeIDKey(raw json.RawMessage) (string, error) {
	text := string(bytes.TrimSpace(raw))
	id, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(id, 10), nil
}

func (c *WsJsonRpcClient) shutdown() {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn != nil {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(wsWriteWait),
		)
	}
	c.teardownConn(conn, common.NewErrEndpointRequestCanceled(fmt.Errorf("websocket client shutting down")))
	// readLoop fires no callbacks once the app context is done, so the
	// connection shutdown drops is reported here: its subscriptions died too.
	if conn != nil {
		c.fireCallbacks(&c.onDisconnectMu, c.onDisconnectCbs)
	}
}
