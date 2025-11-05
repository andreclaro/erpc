package subscription

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Type represents the type of subscription
type Type string

const (
	TypeNewHeads Type = "newHeads"
	TypeLogs     Type = "logs"
)

// Subscription represents an active subscription
type Subscription struct {
	ID           string
	Type         Type
	Params       interface{} // Type-specific parameters (e.g., filter for logs)
	ConnectionID string
	CreatedAt    time.Time
}

// Notification represents a notification to be sent to subscribers
type Notification struct {
	SubscriptionID string
	Result         interface{}
}

// Subscriber is the interface for components that can receive notifications
type Subscriber interface {
	SendNotification(subID string, result interface{}) error
	ConnectionID() string
}

// Registry manages all subscriptions for a network
type Registry struct {
	mu            sync.RWMutex
	subscriptions map[string]*Subscription // subID -> Subscription
	byType        map[Type][]string        // type -> []subID
	byConnection  map[string][]string      // connectionID -> []subID
	subscribers   map[string]Subscriber    // subID -> Subscriber
	logger        *zerolog.Logger
}

// NewRegistry creates a new subscription registry
func NewRegistry(logger *zerolog.Logger) *Registry {
	return &Registry{
		subscriptions: make(map[string]*Subscription),
		byType:        make(map[Type][]string),
		byConnection:  make(map[string][]string),
		subscribers:   make(map[string]Subscriber),
		logger:        logger,
	}
}

// Add adds a new subscription
func (r *Registry) Add(sub *Subscription, subscriber Subscriber) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.subscriptions[sub.ID] = sub
	r.subscribers[sub.ID] = subscriber

	// Index by type
	typeList := r.byType[sub.Type]
	typeList = append(typeList, sub.ID)
	r.byType[sub.Type] = typeList

	// Index by connection
	connList := r.byConnection[sub.ConnectionID]
	connList = append(connList, sub.ID)
	r.byConnection[sub.ConnectionID] = connList

	r.logger.Debug().
		Str("subId", sub.ID).
		Str("type", string(sub.Type)).
		Str("connectionId", sub.ConnectionID).
		Msg("subscription added")

	return nil
}

// Remove removes a subscription by ID
func (r *Registry) Remove(subID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sub, exists := r.subscriptions[subID]
	if !exists {
		return nil // Already removed
	}

	// Remove from main map
	delete(r.subscriptions, subID)
	delete(r.subscribers, subID)

	// Remove from type index
	typeList := r.byType[sub.Type]
	r.byType[sub.Type] = r.removeFromSliceValue(typeList, subID)

	// Remove from connection index
	connList := r.byConnection[sub.ConnectionID]
	r.byConnection[sub.ConnectionID] = r.removeFromSliceValue(connList, subID)

	r.logger.Debug().
		Str("subId", subID).
		Str("type", string(sub.Type)).
		Msg("subscription removed")

	return nil
}

// RemoveByConnection removes all subscriptions for a connection
func (r *Registry) RemoveByConnection(connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	subIDs := r.byConnection[connectionID]
	for _, subID := range subIDs {
		sub := r.subscriptions[subID]
		if sub != nil {
			// Remove from type index
			typeList := r.byType[sub.Type]
			r.byType[sub.Type] = r.removeFromSliceValue(typeList, subID)
		}
		delete(r.subscriptions, subID)
		delete(r.subscribers, subID)
	}

	delete(r.byConnection, connectionID)

	r.logger.Debug().
		Str("connectionId", connectionID).
		Int("count", len(subIDs)).
		Msg("removed all subscriptions for connection")
}

// GetByType returns all subscription IDs of a given type
func (r *Registry) GetByType(subType Type) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	subIDs := r.byType[subType]
	result := make([]string, len(subIDs))
	copy(result, subIDs)
	return result
}

// GetSubscriber returns the subscriber for a subscription
func (r *Registry) GetSubscriber(subID string) (Subscriber, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	subscriber, exists := r.subscribers[subID]
	return subscriber, exists
}

// Get returns a subscription by ID
func (r *Registry) Get(subID string) (*Subscription, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sub, exists := r.subscriptions[subID]
	return sub, exists
}

// Count returns the total number of subscriptions
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.subscriptions)
}

// CountByType returns the number of subscriptions of a given type
func (r *Registry) CountByType(subType Type) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byType[subType])
}

// CountByConnection returns the number of subscriptions for a connection
func (r *Registry) CountByConnection(connectionID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byConnection[connectionID])
}

// removeFromSliceValue removes an item from a slice and returns the new slice
func (r *Registry) removeFromSliceValue(slice []string, item string) []string {
	for i, v := range slice {
		if v == item {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

// Manager manages subscriptions and coordinates pollers
type Manager struct {
	ctx      context.Context
	cancel   context.CancelFunc
	registry *Registry
	pollers  map[Type]Poller
	logger   *zerolog.Logger
	wg       sync.WaitGroup
}

// Poller is the interface for subscription pollers
type Poller interface {
	Start(ctx context.Context) error
	Stop()
	Type() Type
}

// NewManager creates a new subscription manager
func NewManager(ctx context.Context, logger *zerolog.Logger) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{
		ctx:      ctx,
		cancel:   cancel,
		registry: NewRegistry(logger),
		pollers:  make(map[Type]Poller),
		logger:   logger,
	}
}

// Registry returns the subscription registry
func (m *Manager) Registry() *Registry {
	return m.registry
}

// RegisterPoller registers a poller for a subscription type
func (m *Manager) RegisterPoller(poller Poller) {
	m.pollers[poller.Type()] = poller
	m.logger.Info().
		Str("type", string(poller.Type())).
		Msg("poller registered")
}

// Start starts the subscription manager and all pollers
func (m *Manager) Start() error {
	m.logger.Info().Msg("starting subscription manager")

	for subType, poller := range m.pollers {
		m.wg.Add(1)
		go func(st Type, p Poller) {
			defer m.wg.Done()
			if err := p.Start(m.ctx); err != nil {
				m.logger.Error().
					Err(err).
					Str("type", string(st)).
					Msg("poller failed")
			}
		}(subType, poller)
	}

	return nil
}

// Stop stops the subscription manager and all pollers
func (m *Manager) Stop() {
	m.logger.Info().Msg("stopping subscription manager")
	m.cancel()

	for _, poller := range m.pollers {
		poller.Stop()
	}

	m.wg.Wait()
	m.logger.Info().Msg("subscription manager stopped")
}

// Subscribe creates a new subscription
func (m *Manager) Subscribe(subType Type, params interface{}, subscriber Subscriber) (string, error) {
	// Generate subscription ID
	subID := generateSubscriptionID()

	sub := &Subscription{
		ID:           subID,
		Type:         subType,
		Params:       params,
		ConnectionID: subscriber.ConnectionID(),
		CreatedAt:    time.Now(),
	}

	if err := m.registry.Add(sub, subscriber); err != nil {
		return "", err
	}

	m.logger.Info().
		Str("subId", subID).
		Str("type", string(subType)).
		Msg("subscription created")

	return subID, nil
}

// Unsubscribe removes a subscription
func (m *Manager) Unsubscribe(subID string) bool {
	_, exists := m.registry.Get(subID)
	if !exists {
		return false
	}

	m.registry.Remove(subID)
	m.logger.Info().
		Str("subId", subID).
		Msg("subscription removed")

	return true
}

// UnsubscribeConnection removes all subscriptions for a connection
func (m *Manager) UnsubscribeConnection(connectionID string) {
	count := m.registry.CountByConnection(connectionID)
	m.registry.RemoveByConnection(connectionID)
	m.logger.Info().
		Str("connectionId", connectionID).
		Int("count", count).
		Msg("removed all subscriptions for connection")
}
