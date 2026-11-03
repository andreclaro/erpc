package integrity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entry(slot int64, hashSeed byte) chainEntry {
	return chainEntry{
		slot:        slot,
		blockhash:   [32]byte{hashSeed},
		blockHeight: slot,
		parentSlot:  slot - 1,
		parentHash:  [32]byte{hashSeed - 1},
		blockTime:   1_700_000_000 + slot,
	}
}

func TestChainState_ObserveAndLookup(t *testing.T) {
	cs := NewChainState()
	cs.Observe(entry(100, 2))
	cs.Observe(entry(101, 3))

	e, ok := cs.Parent(100)
	require.True(t, ok)
	assert.Equal(t, int64(100), e.slot)
	assert.Equal(t, [32]byte{2}, e.blockhash)

	e, ok = cs.Entry(101)
	require.True(t, ok)
	assert.Equal(t, int64(101), e.slot)

	_, ok = cs.Parent(999)
	assert.False(t, ok)
}

func TestChainState_SameSlotKeepsFirstVerified(t *testing.T) {
	// Fork signal at an anchored slot: the FIRST verified block stays
	// pinned; a different block at the same slot does not replace it.
	cs := NewChainState()
	cs.Observe(entry(5, 10))
	cs.Observe(entry(5, 99)) // fork block — must be ignored by the index
	e, ok := cs.Parent(5)
	require.True(t, ok)
	assert.Equal(t, [32]byte{10}, e.blockhash, "first-seen pinning")
}

func TestChainState_MalformedEntriesDropped(t *testing.T) {
	cs := NewChainState()
	cs.Observe(chainEntry{slot: -1, blockhash: [32]byte{1}})
	_, ok := cs.Parent(-1)
	assert.False(t, ok)
	assert.Empty(t, len(cs.bySlot))
}

func TestChainState_PrunesWhenCapExceeded(t *testing.T) {
	// Contract: pruning is LAZY — it runs only when the index exceeds
	// chainIndexCap, dropping entries below (newest-at-that-moment -
	// chainIndexSpan). After a prune the index stays under the cap, so a
	// subsequent insert burst can leave entries older than the nominal
	// span in place. This bounds memory; it is not a hard recency window.
	cs := NewChainState()
	const total = chainIndexCap + 1000
	for s := int64(0); s < total; s++ {
		cs.Observe(entry(s, byte(s)))
	}
	require.Equal(t, int64(total-1), cs.newest)

	// The single prune happened at insert chainIndexCap (newest 8192,
	// floor 4096): everything below 4096 was dropped then, everything at
	// or above survived because the cap was never re-exceeded.
	for s := int64(0); s < 4096; s++ {
		_, ok := cs.Parent(s)
		assert.False(t, ok, "slot %d below the prune floor must be gone", s)
	}
	for s := int64(4096); s < total; s++ {
		_, ok := cs.Parent(s)
		assert.True(t, ok, "slot %d must survive", s)
	}
	require.LessOrEqual(t, len(cs.bySlot), chainIndexCap, "memory bound holds")
}

func TestChainState_NoteHeadNeverMovesBackwards(t *testing.T) {
	cs := NewChainState()
	cs.NoteHead("processed", 900)
	cs.NoteHead("processed", 800) // reorg evidence — must not regress the store
	got, ok := cs.LastHead("processed")
	require.True(t, ok)
	assert.Equal(t, int64(900), got)

	cs.NoteHead("processed", 950)
	got, _ = cs.LastHead("processed")
	assert.Equal(t, int64(950), got, "forward advance is allowed")
}

func TestChainState_HeadBucketsAreIndependent(t *testing.T) {
	cs := NewChainState()
	cs.NoteHead("processed", 900)
	cs.NoteHead("finalized", 100)

	got, ok := cs.LastHead("finalized")
	require.True(t, ok)
	assert.Equal(t, int64(100), got, "finalized bucket unaffected by processed")

	_, ok = cs.LastHead("confirmed")
	assert.False(t, ok, "untouched bucket has no entry")
}

func TestChainState_NilReceiverIsSafe(t *testing.T) {
	var cs *ChainState
	cs.Observe(entry(1, 1)) // must not panic
	_, ok := cs.Parent(1)
	assert.False(t, ok)
	_, ok = cs.Entry(1)
	assert.False(t, ok)
	cs.NoteHead("processed", 1)
	_, ok = cs.LastHead("processed")
	assert.False(t, ok)
}
