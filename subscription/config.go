package subscription

import "time"

// Config holds the configuration for subscriptions
type Config struct {
	PollInterval time.Duration
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		PollInterval: 2 * time.Second, // Default 2 seconds as specified
	}
}
