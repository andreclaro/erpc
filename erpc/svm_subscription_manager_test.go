package erpc

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/erpc/erpc/architecture/svm"
	"github.com/erpc/erpc/clients"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/indexer/adapters/wsclient"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

type fakeSvmUpstream struct {
	id           string
	vendorName   string
	networkId    string
	networkLabel string
	cfg          *common.UpstreamConfig
	logger       *zerolog.Logger
	vendor       common.Vendor
	tracker      common.HealthTracker
	client       clients.ClientInterface
}

func (u *fakeSvmUpstream) Id() string                                                     { return u.id }
func (u *fakeSvmUpstream) VendorName() string                                             { return u.vendorName }
func (u *fakeSvmUpstream) NetworkId() string                                              { return u.networkId }
func (u *fakeSvmUpstream) NetworkLabel() string                                           { return u.networkLabel }
func (u *fakeSvmUpstream) Config() *common.UpstreamConfig                                 { return u.cfg }
func (u *fakeSvmUpstream) Logger() *zerolog.Logger                                        { return u.logger }
func (u *fakeSvmUpstream) Vendor() common.Vendor                                          { return u.vendor }
func (u *fakeSvmUpstream) Tracker() common.HealthTracker                                  { return u.tracker }
func (u *fakeSvmUpstream) Forward(ctx context.Context, nq *common.NormalizedRequest, byPassMethodExclusion, isHedgeAttempt bool) (*common.NormalizedResponse, error) {
	return nil, nil
}
func (u *fakeSvmUpstream) Cordon(method, reason string)                       {}
func (u *fakeSvmUpstream) Uncordon(method, reason string)                     {}
func (u *fakeSvmUpstream) IgnoreMethod(method string)                         {}
func (u *fakeSvmUpstream) ShouldHandleMethod(method string) (bool, error)     { return true, nil }

func newTestWsJsonRpcClient(t *testing.T, ctx context.Context) *clients.WsJsonRpcClient {
	t.Helper()

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	u, _ := url.Parse("ws" + server.URL[4:])
	up := &fakeSvmUpstream{
		id:           "test-upstream",
		networkId:    "svm:mainnet-beta",
		networkLabel: "svm:mainnet-beta",
		cfg:          &common.UpstreamConfig{Id: "test-upstream"},
		logger:       &log.Logger,
	}
	ci, err := clients.NewWsJsonRpcClient(ctx, &log.Logger, "test", up, u, nil, nil)
	require.NoError(t, err)
	return ci.(*clients.WsJsonRpcClient)
}

func TestSplitSvmStateKey(t *testing.T) {
	cases := []struct {
		key, connID, subID string
	}{
		{"conn/1", "conn", "1"},
		{"conn/a/b", "conn/a", "b"},
		{"sub", "", "sub"},
	}
	for _, tc := range cases {
		connID, subID := splitSvmStateKey(tc.key)
		require.Equal(t, tc.connID, connID, tc.key)
		require.Equal(t, tc.subID, subID, tc.key)
	}
}

func TestGetSvmUpstreamClientState(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c1 := newTestWsJsonRpcClient(t, ctx)
	c2 := newTestWsJsonRpcClient(t, ctx)

	s1 := sm.getSvmUpstreamClientState(c1)
	s1b := sm.getSvmUpstreamClientState(c1)
	s2 := sm.getSvmUpstreamClientState(c2)

	require.Same(t, s1, s1b)
	require.NotSame(t, s1, s2)
	require.NotNil(t, s1.routes)
	require.NotNil(t, s2.routes)
}

func TestFinishSvmSubscription_RemovesStateAndRoute(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	sm.conns["conn"] = wsclient.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsJsonRpcClient(t, ctx)

	state := &svmSubscriptionState{
		key:               "conn/1",
		ownerID:           "1",
		client:            client,
		unsubscribeMethod: "accountUnsubscribe",
	}
	route := &svmUpstreamRoute{
		client:       client,
		upstreamID:   "42",
		epoch:        client.Epoch(),
		key:          "1/42",
		subscribers:  map[string]*svmSubscriptionState{state.key: state},
		projectID:    "test",
		networkLabel: "svm:mainnet-beta",
		upstreamId:   "test-upstream",
		metricKind:   "account",
	}
	state.route = route

	cs := sm.getSvmUpstreamClientState(client)
	cs.routes[route.key] = route
	sm.svmSubs.Store(state.key, state)
	sm.activeSubs.Store(1)

	sm.finishSvmSubscription(state, false)

	_, ok := sm.svmSubs.Load(state.key)
	require.False(t, ok)
	require.Equal(t, int64(0), sm.activeSubs.Load())

	cs.lifecycleMu.Lock()
	_, exists := cs.routes[route.key]
	cs.lifecycleMu.Unlock()
	require.False(t, exists)
}

func TestFinishSvmSubscription_SharedRouteKeptUntilLastOwner(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	sm.conns["conn"] = wsclient.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsJsonRpcClient(t, ctx)

	state1 := &svmSubscriptionState{key: "conn/1", ownerID: "1", client: client, unsubscribeMethod: "accountUnsubscribe"}
	state2 := &svmSubscriptionState{key: "conn/2", ownerID: "2", client: client, unsubscribeMethod: "accountUnsubscribe"}
	route := &svmUpstreamRoute{
		client:       client,
		upstreamID:   "42",
		epoch:        client.Epoch(),
		key:          "1/42",
		subscribers:  map[string]*svmSubscriptionState{state1.key: state1, state2.key: state2},
		projectID:    "test",
		networkLabel: "svm:mainnet-beta",
		upstreamId:   "test-upstream",
		metricKind:   "account",
	}
	state1.route = route
	state2.route = route

	cs := sm.getSvmUpstreamClientState(client)
	cs.routes[route.key] = route
	sm.svmSubs.Store(state1.key, state1)
	sm.svmSubs.Store(state2.key, state2)
	sm.activeSubs.Store(2)

	sm.finishSvmSubscription(state1, false)

	_, ok := sm.svmSubs.Load(state1.key)
	require.False(t, ok)
	require.Equal(t, int64(1), sm.activeSubs.Load())

	cs.lifecycleMu.Lock()
	r, exists := cs.routes[route.key]
	cs.lifecycleMu.Unlock()
	require.True(t, exists)
	require.Len(t, r.subscribers, 1)

	sm.finishSvmSubscription(state2, false)
	require.Equal(t, int64(0), sm.activeSubs.Load())

	cs.lifecycleMu.Lock()
	_, exists = cs.routes[route.key]
	cs.lifecycleMu.Unlock()
	require.False(t, exists)
}

func TestFinishSvmSubscription_Idempotent(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	sm.conns["conn"] = wsclient.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsJsonRpcClient(t, ctx)

	state := &svmSubscriptionState{
		key:               "conn/1",
		ownerID:           "1",
		client:            client,
		unsubscribeMethod: "accountUnsubscribe",
	}
	route := &svmUpstreamRoute{
		client:       client,
		upstreamID:   "42",
		epoch:        client.Epoch(),
		key:          "1/42",
		subscribers:  map[string]*svmSubscriptionState{state.key: state},
		projectID:    "test",
		networkLabel: "svm:mainnet-beta",
		upstreamId:   "test-upstream",
		metricKind:   "account",
	}
	state.route = route

	cs := sm.getSvmUpstreamClientState(client)
	cs.routes[route.key] = route
	sm.svmSubs.Store(state.key, state)
	sm.activeSubs.Store(1)

	sm.finishSvmSubscription(state, false)
	sm.finishSvmSubscription(state, false)

	require.Equal(t, int64(0), sm.activeSubs.Load())
	_, ok := sm.svmSubs.Load(state.key)
	require.False(t, ok)
}

func TestCloneSvmRequest(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	body := []byte(`{"jsonrpc":"2.0","id":"client","method":"accountSubscribe","params":["pubkey",{"commitment":"confirmed"}]}`)
	nq := common.NewNormalizedRequest(body)
	jr, err := nq.JsonRpcRequest()
	require.NoError(t, err)

	clone, err := sm.cloneSvmRequest(nq, jr, "accountUnsubscribe", []interface{}{uint64(42)})
	require.NoError(t, err)

	cloneJr, err := clone.JsonRpcRequest()
	require.NoError(t, err)
	require.Equal(t, "accountUnsubscribe", cloneJr.Method)
	require.Equal(t, []interface{}{uint64(42)}, cloneJr.Params)
	require.Equal(t, jr.ID, cloneJr.ID)
}

func TestHandleSvmNotification_TerminalAlwaysFinishes(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	sm.conns["conn"] = wsclient.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsJsonRpcClient(t, ctx)

	// Build a minimal WsConnection with a fake writer so the terminal
	// notification can be queued synchronously.
	wsc := &WsConnection{
		id:        "conn",
		ctx:       ctx,
		cancel:    cancel,
		logger:    &log.Logger,
		project:   &PreparedProject{Config: &common.ProjectConfig{Id: "test"}},
		networkId: "svm:mainnet-beta",
	}
	wsc.w.out = make(chan wsOut, 1)
	wsc.w.ended = make(chan struct{})
	wsc.w.stop = make(chan struct{})

	adapter := wsclient.New()
	sub, err := adapter.AddSvmSubscription("svm:mainnet-beta", "signature", "signature", "signatureSubscribe", "signatureNotification", 100, func() {})
	require.NoError(t, err)
	delivery := newSvmDelivery(wsc, sub)
	state := &svmSubscriptionState{
		key:               "conn/1",
		ownerID:           "1",
		client:            client,
		unsubscribeMethod: "signatureUnsubscribe",
		delivery:          delivery,
	}
	route := &svmUpstreamRoute{
		client:       client,
		upstreamID:   "42",
		epoch:        client.Epoch(),
		key:          "1/42",
		subscribers:  map[string]*svmSubscriptionState{state.key: state},
		projectID:    "test",
		networkLabel: "svm:mainnet-beta",
		upstreamId:   "test-upstream",
		metricKind:   "signature",
	}
	state.route = route

	cs := sm.getSvmUpstreamClientState(client)
	cs.routes[route.key] = route
	sm.svmSubs.Store(state.key, state)
	sm.activeSubs.Store(1)

	_, desc, _ := svm.DescribeSubscriptionMethod("signatureSubscribe")
	terminalParams := []byte(`{"result":{"context":{"slot":1},"value":{"err":null}}}`)
	sm.handleSvmNotification(state, clients.WsSubscriptionNotification{Params: terminalParams}, desc)

	require.Eventually(t, func() bool {
		_, ok := sm.svmSubs.Load(state.key)
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "terminal notification must always finish the subscription")
	require.True(t, state.terminal.Load())
}

func TestHandleSvmNotification_NonTerminalDoesNotFinish(t *testing.T) {
	sm := NewSubscriptionManager(&log.Logger)
	sm.conns["conn"] = wsclient.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsJsonRpcClient(t, ctx)

	wsc := &WsConnection{
		id:        "conn",
		ctx:       ctx,
		cancel:    cancel,
		logger:    &log.Logger,
		project:   &PreparedProject{Config: &common.ProjectConfig{Id: "test"}},
		networkId: "svm:mainnet-beta",
	}
	wsc.w.out = make(chan wsOut, 1)
	wsc.w.ended = make(chan struct{})
	wsc.w.stop = make(chan struct{})

	adapter := wsclient.New()
	sub, err := adapter.AddSvmSubscription("svm:mainnet-beta", "account", "account", "accountSubscribe", "accountNotification", 100, func() {})
	require.NoError(t, err)
	delivery := newSvmDelivery(wsc, sub)
	state := &svmSubscriptionState{
		key:               "conn/1",
		ownerID:           "1",
		client:            client,
		unsubscribeMethod: "accountUnsubscribe",
		delivery:          delivery,
	}
	route := &svmUpstreamRoute{
		client:       client,
		upstreamID:   "42",
		epoch:        client.Epoch(),
		key:          "1/42",
		subscribers:  map[string]*svmSubscriptionState{state.key: state},
		projectID:    "test",
		networkLabel: "svm:mainnet-beta",
		upstreamId:   "test-upstream",
		metricKind:   "account",
	}
	state.route = route

	cs := sm.getSvmUpstreamClientState(client)
	cs.routes[route.key] = route
	sm.svmSubs.Store(state.key, state)
	sm.activeSubs.Store(1)

	_, desc, _ := svm.DescribeSubscriptionMethod("accountSubscribe")
	sm.handleSvmNotification(state, clients.WsSubscriptionNotification{Params: []byte(`{"result":{"value":1}}`)}, desc)

	time.Sleep(100 * time.Millisecond)
	_, ok := sm.svmSubs.Load(state.key)
	require.True(t, ok)
	require.False(t, state.terminal.Load())
	require.Equal(t, int64(1), sm.activeSubs.Load())
}

func TestNewSvmJsonRpcResultResponsePreservesRequestID(t *testing.T) {
	for _, id := range []string{`"client"`, `null`, `9007199254740993`, `18446744073709551615`} {
		nq := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"accountSubscribe","params":[]}`))
		jr, err := nq.JsonRpcRequest()
		require.NoError(t, err)
		resp := newSvmJsonRpcResultResponse(nq, jr, []byte(`0`))
		var buf bytes.Buffer
		_, err = resp.WriteTo(&buf)
		require.NoError(t, err)
		require.Contains(t, buf.String(), `"id":`+id)
	}
}
