package subscription

import (
	"encoding/json"
	"strings"
)

// LogFilter represents an Ethereum log filter for subscriptions
type LogFilter struct {
	Address   interface{} `json:"address,omitempty"`   // string or []string
	Topics    []interface{} `json:"topics,omitempty"`    // []string or [][]string or null
	FromBlock string      `json:"fromBlock,omitempty"` // Not used in subscriptions
	ToBlock   string      `json:"toBlock,omitempty"`   // Not used in subscriptions
}

// ParseLogFilter parses a log filter from params
func ParseLogFilter(params interface{}) (*LogFilter, error) {
	if params == nil {
		return &LogFilter{}, nil
	}

	// Convert params to JSON and back to LogFilter
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	var filter LogFilter
	if err := json.Unmarshal(jsonBytes, &filter); err != nil {
		return nil, err
	}

	return &filter, nil
}

// MatchesLog checks if a log matches this filter
func (f *LogFilter) MatchesLog(log *Log) bool {
	// Check address filter
	if !f.matchesAddress(log.Address) {
		return false
	}

	// Check topics filter
	if !f.matchesTopics(log.Topics) {
		return false
	}

	return true
}

// matchesAddress checks if the log address matches the filter
func (f *LogFilter) matchesAddress(logAddress string) bool {
	if f.Address == nil {
		return true // No address filter
	}

	// Normalize addresses for comparison
	logAddr := strings.ToLower(logAddress)

	switch addr := f.Address.(type) {
	case string:
		return strings.ToLower(addr) == logAddr
	case []interface{}:
		for _, a := range addr {
			if aStr, ok := a.(string); ok {
				if strings.ToLower(aStr) == logAddr {
					return true
				}
			}
		}
		return false
	case []string:
		for _, a := range addr {
			if strings.ToLower(a) == logAddr {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// matchesTopics checks if the log topics match the filter
func (f *LogFilter) matchesTopics(logTopics []string) bool {
	if len(f.Topics) == 0 {
		return true // No topic filter
	}

	// Ethereum allows up to 4 topics
	for i, filterTopic := range f.Topics {
		if i >= len(logTopics) {
			// Filter has more topics than log
			if filterTopic != nil {
				return false
			}
			continue
		}

		if !matchesTopic(filterTopic, logTopics[i]) {
			return false
		}
	}

	return true
}

// matchesTopic checks if a single topic matches the filter
func matchesTopic(filterTopic interface{}, logTopic string) bool {
	if filterTopic == nil {
		return true // null matches any topic
	}

	logTopicLower := strings.ToLower(logTopic)

	switch ft := filterTopic.(type) {
	case string:
		return strings.ToLower(ft) == logTopicLower
	case []interface{}:
		// Array of topics - OR logic
		for _, t := range ft {
			if tStr, ok := t.(string); ok {
				if strings.ToLower(tStr) == logTopicLower {
					return true
				}
			}
		}
		return false
	case []string:
		for _, t := range ft {
			if strings.ToLower(t) == logTopicLower {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Log represents an Ethereum log entry
type Log struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

