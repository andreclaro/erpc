package subscription

import (
	"sync"

	"github.com/rs/zerolog"
)

// Broadcaster broadcasts notifications to subscribers
type Broadcaster struct {
	registry *Registry
	logger   *zerolog.Logger
	wg       sync.WaitGroup
}

// NewBroadcaster creates a new broadcaster
func NewBroadcaster(registry *Registry, logger *zerolog.Logger) *Broadcaster {
	return &Broadcaster{
		registry: registry,
		logger:   logger,
	}
}

// Broadcast sends a notification to a specific subscription
func (b *Broadcaster) Broadcast(subID string, result interface{}) {
	subscriber, exists := b.registry.GetSubscriber(subID)
	if !exists {
		b.logger.Debug().
			Str("subId", subID).
			Msg("subscriber not found, skipping notification")
		return
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := subscriber.SendNotification(subID, result); err != nil {
			b.logger.Error().
				Err(err).
				Str("subId", subID).
				Msg("failed to send notification")
		}
	}()
}

// BroadcastToType sends a notification to all subscribers of a given type
func (b *Broadcaster) BroadcastToType(subType Type, result interface{}) {
	subIDs := b.registry.GetByType(subType)

	b.logger.Debug().
		Str("type", string(subType)).
		Int("count", len(subIDs)).
		Msg("broadcasting to type")

	for _, subID := range subIDs {
		b.Broadcast(subID, result)
	}
}

// Wait waits for all pending broadcasts to complete
func (b *Broadcaster) Wait() {
	b.wg.Wait()
}
