package erpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erpc/erpc/architecture/svm"
	"github.com/erpc/erpc/clients"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/indexer/adapters/wsclient"
	"github.com/erpc/erpc/telemetry"
	"github.com/erpc/erpc/upstream"
	"github.com/gorilla/websocket"
)

// svmSubscriptionState is one client subscription on one upstream connection.
type svmSubscriptionState struct {
	key               string
	ownerID           string
	upstream          common.Upstream
	client            *clients.WsJsonRpcClient
	route             *svmUpstreamRoute
	unsubscribeMethod string
	delivery          *svmDelivery
	terminal          atomic.Bool
	cleanupOnce       sync.Once
}

// svmUpstreamClientState holds all routes of one upstream WebSocket client.
type svmUpstreamClientState struct {
	lifecycleMu sync.Mutex
	routes      map[string]*svmUpstreamRoute
}

// svmUpstreamRoute is one upstream subscription ID shared by 1 or more local
// subscribers. It stays pinned to the epoch that created it.
type svmUpstreamRoute struct {
	client       *clients.WsJsonRpcClient
	upstreamID   string
	epoch        uint64
	key          string
	subscribers  map[string]*svmSubscriptionState
	terminal     atomic.Bool
	projectID    string
	networkLabel string
	upstreamId   string
	metricKind   string
}

// deliveryRef lets the adapter stop closure close the delivery that is created
// after the subscription is registered.
type deliveryRef struct {
	delivery *svmDelivery
}

func (r *deliveryRef) Close() {
	if r.delivery != nil {
		r.delivery.Close()
	}
}

// SubscribeSvm opens one SVM subscription on one selected upstream.
func (sm *SubscriptionManager) SubscribeSvm(
	ctx context.Context,
	wsc *WsConnection,
	nq *common.NormalizedRequest,
	project *PreparedProject,
	networkId string,
) (resp *common.NormalizedResponse, open func(), err error) {
	start := time.Now()
	method, _ := nq.Method()
	lg := sm.logger.With().Str("connId", wsc.id).Str("networkId", networkId).Str("method", method).Logger()

	nw, err := project.GetNetwork(ctx, networkId)
	if err != nil {
		return nil, nil, err
	}
	nq.SetNetwork(nw)

	if err := svm.ValidateSubscriptionRequestID(ctx, nq); err != nil {
		return nil, nil, err
	}

	op, desc, ok := svm.DescribeSubscriptionMethod(method)
	if !ok || op != svm.SubscriptionOperationSubscribe {
		return nil, nil, common.NewErrNotImplemented(fmt.Sprintf("method not supported: %s", method))
	}

	if err := svm.InjectSubscriptionCommitment(nw, nq, desc); err != nil {
		return nil, nil, err
	}

	if err := acquireSubscribeRateLimits(ctx, project, nw, nq); err != nil {
		return nil, nil, err
	}

	reqFinality := nq.Finality(ctx)
	telemetry.CounterHandle(telemetry.MetricNetworkRequestsReceived,
		project.Config.Id, nw.Label(), method,
		reqFinality.String(), nq.UserId(), nq.AgentName(),
	).Inc()

	jrReq, err := nq.JsonRpcRequest()
	if err != nil {
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	conn := sm.getOrCreateConn(wsc, nw.Label())
	if conn == nil {
		err := wsclient.ErrClosed
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	maxSubs := wsc.server.serverCfg.WebSocket.MaxSubscriptionsPerConnection
	if conn.Count() >= maxSubs {
		err := common.NewErrSubscriptionLimitExceeded(maxSubs)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	maxTotal := wsc.server.serverCfg.WebSocket.MaxSubscriptions
	if maxTotal > 0 && sm.activeSubs.Load() >= int64(maxTotal) {
		telemetry.CounterHandle(telemetry.MetricWebsocketRejectedTotal, project.Config.Id, wsRejectSubGlobal).Inc()
		err := common.NewErrInstanceSubscriptionLimitExceeded(maxTotal)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	var dref deliveryRef
	sub, addErr := conn.AddSvmSubscription(
		networkId, desc.Family, desc.MetricKind, desc.SubscribeMethod, desc.NotificationMethod,
		maxSubs, func() { dref.Close() },
	)
	if addErr != nil {
		if errors.Is(addErr, wsclient.ErrLimitExceeded) {
			addErr = common.NewErrSubscriptionLimitExceeded(maxSubs)
		}
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, addErr)
		return nil, nil, addErr
	}

	delivery := newSvmDelivery(wsc, sub)
	dref.delivery = delivery

	if !sm.reserveSub(maxTotal) {
		conn.RemoveSubscription(sub.ClientSubID)
		telemetry.CounterHandle(telemetry.MetricWebsocketRejectedTotal, project.Config.Id, wsRejectSubGlobal).Inc()
		err := common.NewErrInstanceSubscriptionLimitExceeded(maxTotal)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	stateKey := wsc.id + "/" + sub.ClientSubID
	state := &svmSubscriptionState{
		key:               stateKey,
		ownerID:           sub.ClientSubID,
		unsubscribeMethod: desc.UnsubscribeMethod,
		delivery:          delivery,
	}

	up, err := nw.selectDirectUpstream(ctx, nq, desc.UnsubscribeMethod)
	if err != nil {
		sm.finishSvmSubscription(state, false)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}
	state.upstream = up

	uup, ok := up.(*upstream.Upstream)
	if !ok || uup.Client == nil {
		sm.finishSvmSubscription(state, false)
		err := common.NewErrNoUpstreamsFound(project.Config.Id, nw.Id())
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}
	wsClient, ok := uup.Client.(*clients.WsJsonRpcClient)
	if !ok {
		sm.finishSvmSubscription(state, false)
		err := common.NewErrNoUpstreamsFound(project.Config.Id, nw.Id())
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}
	state.client = wsClient

	subReq, err := sm.cloneSvmRequest(nq, jrReq, method, nil)
	if err != nil {
		sm.finishSvmSubscription(state, false)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	wsSub := &clients.WsSubscription{
		OwnerID:            state.ownerID,
		IDCodec:            clients.WsSubscriptionIDUint64,
		UnsubscribeMethod:  desc.UnsubscribeMethod,
		NotificationMethod: desc.NotificationMethod,
		Handler:            func(n clients.WsSubscriptionNotification) { sm.handleSvmNotification(state, n, desc) },
		OnEvent: func(n clients.WsSubscriptionNotification) {
			labels := []string{project.Config.Id, nw.Label(), up.Id(), desc.MetricKind}
			telemetry.MetricUpstreamSubscriptionEventsTotal.WithLabelValues(labels...).Inc()
			telemetry.MetricUpstreamSubscriptionEventBytesTotal.WithLabelValues(labels...).Add(float64(len(n.Raw)))
		},
	}
	if desc.TerminalSignature {
		wsSub.TerminalCheck = func(params json.RawMessage) bool {
			return svm.IsTerminalSignatureNotification(desc, params)
		}
	}
	subCtx := clients.WithWsSubscription(ctx, wsSub)

	clientState := sm.getSvmUpstreamClientState(wsClient)

	wsClient.SetOnDisconnect(stateKey, func() {
		wsc.stopAsync(websocket.CloseTryAgainLater, "upstream subscription disconnected; reconnect and resubscribe")
	})

	// Forward is performed outside lifecycleMu so a slow upstream cannot stall
	// subscribe/unsubscribe/finish operations for every other route on this
	// client. The route table is updated only after the upstream subscription id
	// is known and the epoch is verified to still match.
	upstreamResp, fwdErr := up.Forward(subCtx, subReq, false, false)
	if fwdErr != nil {
		sm.finishSvmSubscription(state, false)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, fwdErr)
		return nil, nil, fwdErr
	}
	if upstreamResp != nil {
		defer upstreamResp.Release()
	}

	if wsSub.ID == "" {
		sm.finishSvmSubscription(state, false)
		err := common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorInvalidArgument, "upstream returned an invalid subscription id", nil, nil)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	routeKey := strconv.FormatUint(wsSub.Epoch, 10) + "/" + wsSub.ID

	clientState.lifecycleMu.Lock()
	if wsSub.Epoch != wsClient.Epoch() {
		clientState.lifecycleMu.Unlock()
		sm.finishSvmSubscription(state, false)
		err := common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorInvalidArgument, "upstream connection changed before subscription could start", nil, nil)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}

	route, exists := clientState.routes[routeKey]
	if !exists {
		route = &svmUpstreamRoute{
			client:       wsClient,
			upstreamID:   wsSub.ID,
			epoch:        wsSub.Epoch,
			key:          routeKey,
			subscribers:  make(map[string]*svmSubscriptionState),
			projectID:    project.Config.Id,
			networkLabel: nw.Label(),
			upstreamId:   up.Id(),
			metricKind:   desc.MetricKind,
		}
		clientState.routes[routeKey] = route
		telemetry.MetricUpstreamSubscriptionsActive.WithLabelValues(route.projectID, route.networkLabel, route.upstreamId, route.metricKind).Inc()
	}
	if route.terminal.Load() {
		clientState.lifecycleMu.Unlock()
		sm.finishSvmSubscription(state, false)
		err := common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorInvalidArgument, "subscription ended before it could start", nil, nil)
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, nil, err
	}
	state.route = route
	route.subscribers[stateKey] = state
	sm.svmSubs.Store(stateKey, state)
	clientState.lifecycleMu.Unlock()

	sm.recordSuccessMetrics(project, nw, method, reqFinality, start, nq)
	lg.Info().
		Str("clientSubId", sub.ClientSubID).
		Str("upstreamSubId", wsSub.ID).
		Str("upstreamId", up.Id()).
		Msg("svm subscription established")
	return newSvmJsonRpcResultResponse(nq, jrReq, sub.ClientSubIDJSON), func() { _ = delivery.Open() }, nil
}

// UnsubscribeSvm removes one SVM subscription from its connection.
func (sm *SubscriptionManager) UnsubscribeSvm(
	ctx context.Context,
	wsc *WsConnection,
	nq *common.NormalizedRequest,
	project *PreparedProject,
	networkId string,
) (*common.NormalizedResponse, error) {
	start := time.Now()
	method, _ := nq.Method()

	nw, err := project.GetNetwork(ctx, networkId)
	if err != nil {
		return nil, err
	}
	nq.SetNetwork(nw)

	if err := svm.ValidateSubscriptionRequestID(ctx, nq); err != nil {
		return nil, err
	}

	if err := acquireSubscribeRateLimits(ctx, project, nw, nq); err != nil {
		return nil, err
	}

	reqFinality := nq.Finality(ctx)
	telemetry.CounterHandle(telemetry.MetricNetworkRequestsReceived,
		project.Config.Id, nw.Label(), method,
		reqFinality.String(), nq.UserId(), nq.AgentName(),
	).Inc()

	jrReq, err := nq.JsonRpcRequest()
	if err != nil {
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, err
	}

	localID, err := svm.ParseLocalSubscriptionID(jrReq)
	if err != nil {
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, err
	}

	stateKey := wsc.id + "/" + strconv.FormatUint(localID, 10)
	v, ok := sm.svmSubs.Load(stateKey)
	if !ok {
		err := svm.InvalidSubscriptionID()
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, err
	}
	state := v.(*svmSubscriptionState)

	if state.route == nil {
		sm.finishSvmSubscription(state, false)
		sm.recordSuccessMetrics(project, nw, method, reqFinality, start, nq)
		return newSvmJsonRpcResultResponse(nq, jrReq, []byte("true")), nil
	}

	clientState := sm.getSvmUpstreamClientState(state.route.client)

	clientState.lifecycleMu.Lock()
	otherOwners := len(state.route.subscribers) - 1
	if otherOwners > 0 {
		delete(state.route.subscribers, stateKey)
		clientState.lifecycleMu.Unlock()
		sm.finishSvmSubscription(state, false)
		sm.recordSuccessMetrics(project, nw, method, reqFinality, start, nq)
		return newSvmJsonRpcResultResponse(nq, jrReq, []byte("true")), nil
	}
	clientState.lifecycleMu.Unlock()

	upstreamID, err := strconv.ParseUint(state.route.upstreamID, 10, 64)
	if err != nil {
		sm.finishSvmSubscription(state, false)
		sm.recordSuccessMetrics(project, nw, method, reqFinality, start, nq)
		return newSvmJsonRpcResultResponse(nq, jrReq, []byte("true")), nil
	}

	unsubReq, err := sm.cloneSvmRequest(nq, jrReq, state.unsubscribeMethod, []interface{}{upstreamID})
	if err != nil {
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, err
	}

	unsubSub := &clients.WsSubscription{
		ID:                state.route.upstreamID,
		Epoch:             state.route.epoch,
		IDCodec:           clients.WsSubscriptionIDUint64,
		UnsubscribeMethod: state.unsubscribeMethod,
	}
	unsubCtx := clients.WithWsSubscription(ctx, unsubSub)

	upstreamResp, err := state.upstream.Forward(unsubCtx, unsubReq, false, false)
	if err != nil {
		sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
		return nil, err
	}
	if upstreamResp != nil {
		defer upstreamResp.Release()
	}

	jrr, perr := upstreamResp.JsonRpcResponse(ctx)
	if perr == nil && jrr != nil {
		if result := jrr.GetResultBytes(); len(result) > 0 {
			var ok bool
			if uerr := common.SonicCfg.Unmarshal(result, &ok); uerr == nil && !ok {
				err := svm.InvalidSubscriptionID()
				sm.recordFailureMetrics(project, nw, method, reqFinality, start, nq, err)
				sm.finishSvmSubscription(state, false)
				return nil, err
			}
		}
	}

	sm.finishSvmSubscription(state, false)
	sm.recordSuccessMetrics(project, nw, method, reqFinality, start, nq)
	return newSvmJsonRpcResultResponse(nq, jrReq, []byte("true")), nil
}

// finishSvmSubscription cleans up one SVM subscription exactly once. When
// unsubscribeUpstream is true and this was the last owner of its route, an
// orphan unsubscribe is sent to the upstream.
func (sm *SubscriptionManager) finishSvmSubscription(state *svmSubscriptionState, unsubscribeUpstream bool) {
	state.cleanupOnce.Do(func() {
		connID, _ := splitSvmStateKey(state.key)
		sm.connMu.Lock()
		conn := sm.conns[connID]
		if conn != nil {
			conn.RemoveSubscription(state.ownerID)
		}
		sm.connMu.Unlock()

		sm.svmSubs.Delete(state.key)
		sm.activeSubs.Add(-1)

		if state.client != nil {
			state.client.RemoveOnDisconnect(state.key)
		}

		if state.route == nil {
			return
		}

		route := state.route
		clientState := sm.getSvmUpstreamClientState(route.client)

		clientState.lifecycleMu.Lock()
		delete(route.subscribers, state.key)
		route.client.UnregisterSubscriptionHandlerOwner(state.ownerID, route.upstreamID, route.epoch)
		final := len(route.subscribers) == 0
		if final {
			delete(clientState.routes, route.key)
		}
		clientState.lifecycleMu.Unlock()

		if final {
			telemetry.MetricUpstreamSubscriptionsActive.WithLabelValues(route.projectID, route.networkLabel, route.upstreamId, route.metricKind).Dec()
		}

		if final && unsubscribeUpstream && route.epoch == route.client.Epoch() {
			go route.client.UnsubscribeOrphanMethod(route.upstreamID, state.unsubscribeMethod, route.epoch, clients.WsSubscriptionIDUint64)
		}
	})
}

func (sm *SubscriptionManager) handleSvmNotification(state *svmSubscriptionState, n clients.WsSubscriptionNotification, desc svm.SubscriptionDescriptor) {
	if state.terminal.Load() {
		return
	}
	terminal := desc.TerminalSignature && svm.IsTerminalSignatureNotification(desc, n.Params)
	done, err := state.delivery.Offer(n, terminal)
	if err != nil && !terminal {
		return
	}
	if terminal {
		state.terminal.Store(true)
		go func() {
			if done != nil {
				<-done
			}
			sm.finishSvmSubscription(state, false)
		}()
	}
}

func (sm *SubscriptionManager) cloneSvmRequest(
	nq *common.NormalizedRequest,
	jrReq *common.JsonRpcRequest,
	method string,
	params []interface{},
) (*common.NormalizedRequest, error) {
	clone := jrReq.Clone()
	clone.Method = method
	if params != nil {
		clone.Params = params
	}
	req := common.NewNormalizedRequestFromJsonRpcRequest(clone)
	req.SetNetwork(nq.Network())
	if user := nq.User(); user != nil {
		req.SetUser(user)
	}
	req.SetClientIP(nq.ClientIP())
	if nq.ForwardHeaders != nil {
		req.ForwardHeaders = nq.ForwardHeaders.Clone()
	}
	dirs := nq.Directives().Clone()
	dirs.UpstreamPolicies = common.UpstreamPoliciesTimeoutOnly
	req.SetDirectives(dirs)
	return req, nil
}

func (sm *SubscriptionManager) getSvmUpstreamClientState(client *clients.WsJsonRpcClient) *svmUpstreamClientState {
	if v, ok := sm.svmUpstreamClients.Load(client); ok {
		return v.(*svmUpstreamClientState)
	}
	cs := &svmUpstreamClientState{routes: make(map[string]*svmUpstreamRoute)}
	if v, loaded := sm.svmUpstreamClients.LoadOrStore(client, cs); loaded {
		return v.(*svmUpstreamClientState)
	}
	return cs
}

func splitSvmStateKey(key string) (connID, subID string) {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}
