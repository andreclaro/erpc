package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// generateSubscriptionID generates a unique subscription ID
func generateSubscriptionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if random fails
		return fmt.Sprintf("0x%d", time.Now().UnixNano())
	}
	return "0x" + hex.EncodeToString(b)
}

