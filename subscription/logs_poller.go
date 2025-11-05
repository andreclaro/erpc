package subscription

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

// LogsPoller polls for new logs matching subscription filters
type LogsPoller struct {
	ctx          context.Context
	cancel       context.CancelFunc
	registry     *Registry
	broadcaster  *Broadcaster
	forward      ForwardFunc
	pollInterval time.Duration
	logger       *zerolog.Logger

	mu              sync.Mutex
	lastBlockNumber string
	running         bool
}

// NewLogsPoller creates a new logs poller
func NewLogsPoller(
	ctx context.Context,
	registry *Registry,
	broadcaster *Broadcaster,
	forward ForwardFunc,
	pollInterval time.Duration,
	logger *zerolog.Logger,
) *LogsPoller {
	ctx, cancel := context.WithCancel(ctx)
	return &LogsPoller{
		ctx:          ctx,
		cancel:       cancel,
		registry:     registry,
		broadcaster:  broadcaster,
		forward:      forward,
		pollInterval: pollInterval,
		logger:       logger,
	}
}

// Type returns the subscription type this poller handles
func (p *LogsPoller) Type() Type {
	return TypeLogs
}

// Start starts the poller
func (p *LogsPoller) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()

	p.logger.Info().
		Dur("pollInterval", p.pollInterval).
		Msg("starting logs poller")

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Poll immediately on start
	p.poll()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info().Msg("logs poller context cancelled")
			return ctx.Err()
		case <-p.ctx.Done():
			p.logger.Info().Msg("logs poller stopped")
			return nil
		case <-ticker.C:
			p.poll()
		}
	}
}

// Stop stops the poller
func (p *LogsPoller) Stop() {
	p.logger.Info().Msg("stopping logs poller")
	p.cancel()
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
}

// poll fetches logs and notifies subscribers
func (p *LogsPoller) poll() {
	// Check if there are any subscribers
	subIDs := p.registry.GetByType(TypeLogs)
	if len(subIDs) == 0 {
		p.logger.Debug().Msg("no logs subscribers, skipping poll")
		return
	}

	p.logger.Debug().
		Int("subscribers", len(subIDs)).
		Msg("polling for logs")

	// Get current block number
	currentBlockNum, err := p.getCurrentBlockNumber()
	if err != nil {
		p.logger.Error().Err(err).Msg("failed to get current block number")
		return
	}

	// Determine fromBlock
	p.mu.Lock()
	fromBlock := p.lastBlockNumber
	if fromBlock == "" {
		fromBlock = currentBlockNum // First poll, start from current
	}
	p.lastBlockNumber = currentBlockNum
	p.mu.Unlock()

	// For each subscription, fetch logs matching its filter
	for _, subID := range subIDs {
		sub, exists := p.registry.Get(subID)
		if !exists {
			continue
		}

		// Parse filter from subscription params
		filter, err := ParseLogFilter(sub.Params)
		if err != nil {
			p.logger.Error().
				Err(err).
				Str("subId", subID).
				Msg("failed to parse log filter")
			continue
		}

		// Fetch logs for this filter
		logs, err := p.fetchLogs(filter, fromBlock, currentBlockNum)
		if err != nil {
			p.logger.Error().
				Err(err).
				Str("subId", subID).
				Msg("failed to fetch logs")
			continue
		}

		// Send each log as a separate notification
		for _, log := range logs {
			if filter.MatchesLog(&log) {
				p.broadcaster.Broadcast(subID, log)
			}
		}

		if len(logs) > 0 {
			p.logger.Debug().
				Str("subId", subID).
				Int("logCount", len(logs)).
				Msg("sent log notifications")
		}
	}
}

// getCurrentBlockNumber fetches the current block number
func (p *LogsPoller) getCurrentBlockNumber() (string, error) {
	req := common.NewNormalizedRequest([]byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "eth_blockNumber",
		"params": []
	}`))

	resp, err := p.forward(p.ctx, req)
	if err != nil {
		return "", err
	}

	if resp.IsResultEmptyish() {
		return "", fmt.Errorf("empty response for eth_blockNumber")
	}

	jrr, err := resp.JsonRpcResponse()
	if err != nil {
		return "", err
	}

	resultBytes := jrr.GetResultBytes()
	var blockNum string
	if err := sonic.Unmarshal(resultBytes, &blockNum); err != nil {
		return "", err
	}

	return blockNum, nil
}

// fetchLogs fetches logs from the chain
func (p *LogsPoller) fetchLogs(filter *LogFilter, fromBlock, toBlock string) ([]Log, error) {
	// Build eth_getLogs request
	filterMap := map[string]interface{}{
		"fromBlock": fromBlock,
		"toBlock":   toBlock,
	}

	if filter.Address != nil {
		filterMap["address"] = filter.Address
	}

	if len(filter.Topics) > 0 {
		filterMap["topics"] = filter.Topics
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_getLogs",
		"params":  []interface{}{filterMap},
	}

	reqBytes, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req := common.NewNormalizedRequest(reqBytes)
	resp, err := p.forward(p.ctx, req)
	if err != nil {
		return nil, err
	}

	if resp.IsResultEmptyish() {
		return []Log{}, nil // No logs found
	}

	jrr, err := resp.JsonRpcResponse()
	if err != nil {
		return nil, err
	}

	resultBytes := jrr.GetResultBytes()
	var logs []Log
	if err := sonic.Unmarshal(resultBytes, &logs); err != nil {
		return nil, err
	}

	return logs, nil
}

