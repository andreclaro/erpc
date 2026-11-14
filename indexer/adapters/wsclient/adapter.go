// Package wsclient holds the subscriptions of one client WebSocket
// connection. Each subscription is a tap that writes its own notifications.
package wsclient

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by AddSubscription once the adapter was drained.
var ErrClosed = errors.New("connection closed")

// ErrLimitExceeded is returned by AddSubscription when the connection
// already holds the maximum number of subscriptions.
var ErrLimitExceeded = errors.New("subscription limit exceeded")

// ErrSubscriptionIDExhausted is returned by AddSvmSubscription when the
// connection has allocated every u64 id.
var ErrSubscriptionIDExhausted = errors.New("subscription id exhausted")

// Adapter is the subscription set of one client connection.
type Adapter struct {
	mu              sync.RWMutex
	drained         bool
	subs            map[string]*clientSub // clientSubId -> sub
	nextSvmID       uint64
	svmIDsExhausted bool
}

type clientSub struct {
	id        string
	kind      EventKind
	networkID string

	// svm metadata, empty for EVM subscriptions.
	family             string
	metricKind         string
	subscribeMethod    string
	notificationMethod string
	clientSubIDJSON    json.RawMessage

	// opened is closed once the client has the subscription id: nothing is
	// written before it, as geth sends the id first.
	opened   chan struct{}
	openOnce sync.Once
	// removed is set before the unsubscribe answer is written; a
	// notification written after that answer sees it and is dropped.
	removed atomic.Bool
	// stop ends the subscription's tap.
	stop func()
}

func (s *clientSub) describe() Subscription {
	return Subscription{
		ClientSubID:        s.id,
		ClientSubIDJSON:    s.clientSubIDJSON,
		Kind:               s.kind,
		NetworkID:          s.networkID,
		Family:             s.family,
		MetricKind:         s.metricKind,
		SubscribeMethod:    s.subscribeMethod,
		NotificationMethod: s.notificationMethod,
		removed:            &s.removed,
	}
}

// New creates the adapter of a client connection.
func New() *Adapter {
	return &Adapter{subs: make(map[string]*clientSub)}
}

// Open lets clientSubID's notifications flow, once the response carrying
// its id is written.
func (a *Adapter) Open(clientSubID string) {
	a.mu.RLock()
	sub := a.subs[clientSubID]
	a.mu.RUnlock()
	if sub != nil {
		sub.openOnce.Do(func() { close(sub.opened) })
	}
}

// AddSubscription registers an EVM subscription whose tap writes after
// opened is closed by Open; stop ends that tap when the subscription is
// removed. It fails with ErrLimitExceeded when the connection already holds
// maxSubs, and with ErrClosed once the adapter was drained.
func (a *Adapter) AddSubscription(clientSubID, networkID string, kind EventKind, maxSubs int, stop func()) (Subscription, <-chan struct{}, error) {
	return a.register(&clientSub{
		id:        clientSubID,
		kind:      kind,
		networkID: networkID,
		opened:    make(chan struct{}),
		stop:      stop,
	}, maxSubs)
}

// AddSvmSubscription registers an SVM subscription with a monotonic numeric
// id. It fails with ErrClosed, ErrLimitExceeded, or ErrSubscriptionIDExhausted.
func (a *Adapter) AddSvmSubscription(
	networkID, family, metricKind, subscribeMethod, notificationMethod string,
	maxSubs int,
	stop func(),
) (Subscription, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drained {
		return Subscription{}, ErrClosed
	}
	if len(a.subs) >= maxSubs {
		return Subscription{}, ErrLimitExceeded
	}
	if a.svmIDsExhausted {
		return Subscription{}, ErrSubscriptionIDExhausted
	}
	id := a.nextSvmID
	if id == math.MaxUint64 {
		a.svmIDsExhausted = true
	} else {
		a.nextSvmID++
	}
	idStr := strconv.FormatUint(id, 10)
	sub := &clientSub{
		id:                 idStr,
		networkID:          networkID,
		family:             family,
		metricKind:         metricKind,
		subscribeMethod:    subscribeMethod,
		notificationMethod: notificationMethod,
		clientSubIDJSON:    strconv.AppendUint(nil, id, 10),
		opened:             make(chan struct{}),
		stop:               stop,
	}
	a.subs[idStr] = sub
	return sub.describe(), nil
}

func (a *Adapter) register(sub *clientSub, maxSubs int) (Subscription, <-chan struct{}, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drained {
		return Subscription{}, nil, ErrClosed
	}
	if len(a.subs) >= maxSubs {
		return Subscription{}, nil, ErrLimitExceeded
	}
	a.subs[sub.id] = sub
	return sub.describe(), sub.opened, nil
}

// RemoveSubscription marks a subscription removed, removes it and stops
// its tap.
func (a *Adapter) RemoveSubscription(clientSubID string) (kind EventKind, existed bool) {
	a.mu.Lock()
	sub, ok := a.subs[clientSubID]
	delete(a.subs, clientSubID)
	a.mu.Unlock()
	if !ok {
		return 0, false
	}
	sub.removed.Store(true)
	sub.stop()
	return sub.kind, true
}

// Drain removes every subscription and makes later AddSubscription calls
// fail. It returns the subscriptions it removed; a concurrent
// RemoveSubscription owns any it removed first.
func (a *Adapter) Drain() []Subscription {
	a.mu.Lock()
	subs := a.subs
	a.drained = true
	a.subs = make(map[string]*clientSub)
	a.mu.Unlock()

	out := make([]Subscription, 0, len(subs))
	for _, sub := range subs {
		sub.removed.Store(true)
		sub.stop()
		out = append(out, sub.describe())
	}
	return out
}

// Subscription describes one subscription.
type Subscription struct {
	ClientSubID        string
	ClientSubIDJSON    json.RawMessage
	Kind               EventKind
	NetworkID          string
	Family             string
	MetricKind         string
	SubscribeMethod    string
	NotificationMethod string

	removed *atomic.Bool
}

// Removed reports whether the subscription was removed: its tap must not
// write any more.
func (s Subscription) Removed() bool {
	return s.removed.Load()
}

// Count returns the number of active subscriptions.
func (a *Adapter) Count() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.subs)
}
