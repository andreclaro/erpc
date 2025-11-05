package subscription

import (
	"context"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

// ForwardFunc is a function that forwards JSON-RPC requests
type ForwardFunc func(ctx context.Context, req *common.NormalizedRequest) (*common.NormalizedResponse, error)

// HeadPoller polls for new block headers
type HeadPoller struct {
	ctx          context.Context
	cancel       context.CancelFunc
	registry     *Registry
	broadcaster  *Broadcaster
	forward      ForwardFunc
	pollInterval time.Duration
	logger       *zerolog.Logger

	mu        sync.Mutex
	lastBlock *BlockHeader
	running   bool
}

// BlockHeader represents a simplified block header for newHeads
type BlockHeader struct {
	Number           string `json:"number"`
	Hash             string `json:"hash"`
	ParentHash       string `json:"parentHash"`
	Timestamp        string `json:"timestamp"`
	Miner            string `json:"miner,omitempty"`
	GasLimit         string `json:"gasLimit,omitempty"`
	GasUsed          string `json:"gasUsed,omitempty"`
	BaseFeePerGas    string `json:"baseFeePerGas,omitempty"`
	TransactionsRoot string `json:"transactionsRoot,omitempty"`
	StateRoot        string `json:"stateRoot,omitempty"`
	ReceiptsRoot     string `json:"receiptsRoot,omitempty"`
}

// NewHeadPoller creates a new head poller
func NewHeadPoller(
	ctx context.Context,
	registry *Registry,
	broadcaster *Broadcaster,
	forward ForwardFunc,
	pollInterval time.Duration,
	logger *zerolog.Logger,
) *HeadPoller {
	ctx, cancel := context.WithCancel(ctx)
	return &HeadPoller{
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
func (p *HeadPoller) Type() Type {
	return TypeNewHeads
}

// Start starts the poller
func (p *HeadPoller) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()

	p.logger.Info().
		Dur("pollInterval", p.pollInterval).
		Msg("starting head poller")

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Poll immediately on start
	p.poll()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info().Msg("head poller context cancelled")
			return ctx.Err()
		case <-p.ctx.Done():
			p.logger.Info().Msg("head poller stopped")
			return nil
		case <-ticker.C:
			p.poll()
		}
	}
}

// Stop stops the poller
func (p *HeadPoller) Stop() {
	p.logger.Info().Msg("stopping head poller")
	p.cancel()
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
}

// poll fetches the latest block and notifies subscribers if it's new
func (p *HeadPoller) poll() {
	// Check if there are any subscribers
	count := p.registry.CountByType(TypeNewHeads)
	if count == 0 {
		p.logger.Debug().Msg("no newHeads subscribers, skipping poll")
		return
	}

	p.logger.Debug().
		Int("subscribers", count).
		Msg("polling for new head")

	// Create eth_getBlockByNumber request for latest block
	req := common.NewNormalizedRequest([]byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "eth_getBlockByNumber",
		"params": ["latest", false]
	}`))

	// Forward request using the network's forward function
	resp, err := p.forward(p.ctx, req)
	if err != nil {
		p.logger.Error().
			Err(err).
			Msg("failed to fetch latest block")
		return
	}

	if resp.IsResultEmptyish() {
		p.logger.Debug().Msg("empty response for latest block")
		return
	}

	// Extract block header from response
	header, err := p.extractBlockHeader(resp)
	if err != nil {
		p.logger.Error().
			Err(err).
			Msg("failed to extract block header")
		return
	}

	// Check if this is a new block
	p.mu.Lock()
	isNewBlock := p.lastBlock == nil || p.lastBlock.Number != header.Number
	if isNewBlock {
		p.lastBlock = header
	}
	p.mu.Unlock()

	if !isNewBlock {
		p.logger.Debug().
			Str("blockNumber", header.Number).
			Msg("block already processed")
		return
	}

	p.logger.Info().
		Str("blockNumber", header.Number).
		Str("blockHash", header.Hash).
		Int("subscribers", count).
		Msg("new block detected, broadcasting")

	// Broadcast to all newHeads subscribers
	p.broadcaster.BroadcastToType(TypeNewHeads, header)
}

// extractBlockHeader extracts block header from the response
func (p *HeadPoller) extractBlockHeader(resp *common.NormalizedResponse) (*BlockHeader, error) {
	// Get the JSON-RPC response
	jrr, err := resp.JsonRpcResponse()
	if err != nil {
		return nil, err
	}

	// Extract result - parse the raw result bytes
	resultBytes := jrr.GetResultBytes()
	if len(resultBytes) == 0 {
		return nil, common.NewErrJsonRpcExceptionInternal(
			0,
			common.JsonRpcErrorServerSideException,
			"empty block result",
			nil,
			nil,
		)
	}

	var resultMap map[string]interface{}
	if err := sonic.Unmarshal(resultBytes, &resultMap); err != nil {
		return nil, err
	}

	header := &BlockHeader{
		Number:           getString(resultMap, "number"),
		Hash:             getString(resultMap, "hash"),
		ParentHash:       getString(resultMap, "parentHash"),
		Timestamp:        getString(resultMap, "timestamp"),
		Miner:            getString(resultMap, "miner"),
		GasLimit:         getString(resultMap, "gasLimit"),
		GasUsed:          getString(resultMap, "gasUsed"),
		BaseFeePerGas:    getString(resultMap, "baseFeePerGas"),
		TransactionsRoot: getString(resultMap, "transactionsRoot"),
		StateRoot:        getString(resultMap, "stateRoot"),
		ReceiptsRoot:     getString(resultMap, "receiptsRoot"),
	}

	return header, nil
}

// getString safely extracts a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
