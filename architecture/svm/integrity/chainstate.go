package integrity

// ChainState is the SVM integrity module's in-memory slot-chain index: the
// blocks this module has fully verified, keyed by slot, so later blocks can be
// anchored to already-trusted parents (parentBlockhash / parentSlot /
// blockHeight links) without consensus validation or auxiliary fetches.
//
// Trust rule (chain-safety): a block enters the index ONLY when every enabled
// check passed on it — a block with a recorded mismatch or a rejection never
// becomes ground truth for later blocks. A node serving a fork that later
// reorgs is reconciled naturally: the fork's blocks remain in the index, but a
// post-reorg child whose parent matches the canonical branch links cleanly;
// a mismatch against the stale fork entry is a ReorgSensitive violation,
// resolved per finality (recorded, not rejected, while unfinalized).
//
// Phase-2 scope: single-process memory only (the EVM ChainView's shared-state
// connector and reorg-window pruning land with the full commitment tier).

import "sync"

// chainEntry is one verified block's link-relevant fields.
type chainEntry struct {
	slot        int64
	blockhash   [32]byte
	blockHeight int64 // -1 when the block carries no blockHeight (very old blocks)
	parentSlot  int64
	parentHash  [32]byte
}

// chainIndexCap bounds the index; beyond it, entries more than chainIndexSpan
// slots behind the newest observed slot are dropped.
const (
	chainIndexCap  = 8192
	chainIndexSpan = 4096
)

// ChainState is a per-network slot index. The zero value is ready to use.
type ChainState struct {
	mu     sync.RWMutex
	bySlot map[int64]chainEntry
	newest int64
}

// NewChainState returns an empty index.
func NewChainState() *ChainState {
	return &ChainState{bySlot: make(map[int64]chainEntry, 256)}
}

// Observe records a verified block. slot must be the block's own slot
// (the requested slot for getBlock). Silently drops malformed entries
// (negative slot, undecodable hashes) and prunes by chainIndexCap/Span.
// Same-slot entries keep the FIRST verified block (first-seen pinning): a
// later different block at an already-anchored slot is a fork signal, not a
// replacement — link checks judge it, not the index.
func (c *ChainState) Observe(e chainEntry) {
	if c == nil || e.slot < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bySlot == nil {
		c.bySlot = make(map[int64]chainEntry, 256)
	}
	if _, exists := c.bySlot[e.slot]; exists {
		return
	}
	c.bySlot[e.slot] = e
	if e.slot > c.newest {
		c.newest = e.slot
	}
	if len(c.bySlot) > chainIndexCap {
		floor := c.newest - chainIndexSpan
		for s := range c.bySlot {
			if s < floor {
				delete(c.bySlot, s)
			}
		}
	}
}

// Parent returns the verified entry for the given slot, when present.
func (c *ChainState) Parent(slot int64) (chainEntry, bool) {
	if c == nil {
		return chainEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.bySlot[slot]
	return e, ok
}
