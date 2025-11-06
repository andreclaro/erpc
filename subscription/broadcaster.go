package subscription

import (
	"sync"

	"github.com/erpc/erpc/telemetry"
	"github.com/rs/zerolog"
)

// Broadcaster broadcasts notifications to subscribers
type Broadcaster struct {
	registry  *Registry
	logger    *zerolog.Logger
	wg        sync.WaitGroup
	projectId string
	networkId string
}

// NewBroadcaster creates a new broadcaster
func NewBroadcaster(registry *Registry, projectId, networkId string, logger *zerolog.Logger) *Broadcaster {
	return &Broadcaster{
		registry:  registry,
		logger:    logger,
		projectId: projectId,
		networkId: networkId,
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

	// Get subscription for type info
	sub, _ := b.registry.Get(subID)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := subscriber.SendNotification(subID, result); err != nil {
			b.logger.Error().
				Err(err).
				Str("subId", subID).
				Msg("failed to send notification")
			
			// Track error
			if sub != nil {
				telemetry.MetricWebSocketNotificationErrors.WithLabelValues(
					b.projectId,
					b.networkId,
					string(sub.Type),
					"send_failed",
				).Inc()
			}
		} else {
			// Track successful send
			if sub != nil {
				telemetry.MetricWebSocketNotificationsSent.WithLabelValues(
					b.projectId,
					b.networkId,
					string(sub.Type),
				).Inc()
			}
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
