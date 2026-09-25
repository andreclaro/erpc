package integrity

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecoded_ParseErrorIsSticky(t *testing.T) {
	d := newDecoded("getBlock", []byte("{not json"))
	_, err1 := d.Block()
	require.Error(t, err1)
	_, err2 := d.Block()
	assert.Equal(t, err1, err2, "parse failure must be sticky — checks must never see a flaky re-parse")

	b, err := d.hasBlock(), error(nil)
	_ = err
	assert.False(t, b, "unparseable raw is not a block")
}

func TestDecoded_BlockResultIsCached(t *testing.T) {
	d := newDecoded("getBlock", []byte(`{"blockhash":"abc","parentSlot":99}`))
	b1, err := d.Block()
	require.NoError(t, err)
	b2, err := d.Block()
	require.NoError(t, err)
	assert.Same(t, b1, b2, "lazy parse must cache the same value")
	assert.Equal(t, int64(99), *b1.ParentSlot)
}

func TestDecoded_RequestedSlotParamKinds(t *testing.T) {
	cases := []struct {
		name   string
		params []any
		want   int64
		ok     bool
	}{
		{"float64 (sonic default)", []any{float64(100)}, 100, true},
		{"string slot", []any{"123"}, 123, true},
		{"int64", []any{int64(55)}, 55, true},
		{"absent", nil, 0, false},
		{"non-numeric string", []any{"latest"}, 0, false},
		{"config object at [1]", []any{float64(7), map[string]any{"commitment": "finalized"}}, 7, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDecoded("getBlock", []byte(`{}`))
			d.reqParams = c.params
			got, ok := d.RequestedSlot()
			assert.Equal(t, c.ok, ok)
			if c.ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}

func TestDecoded_SlotNumberRejectsNonIntegral(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`950`, 950, true},
		{`0`, 0, true},
		{`-5`, -5, true},
		{`950.5`, 0, false}, // float: shape gate, not a coercion
		{`"950"`, 0, false}, // string-encoded number: rejected
		{`null`, 0, false},
		{`{}`, 0, false},
	}
	for _, c := range cases {
		d := newDecoded("getSlot", []byte(c.raw))
		got, ok := d.SlotNumber()
		assert.Equal(t, c.ok, ok, "raw %s", c.raw)
		if c.ok {
			assert.Equal(t, c.want, got, "raw %s", c.raw)
		}
	}
}

func TestDecoded_ContextSlot(t *testing.T) {
	d := newDecoded("getAccountInfo", []byte(`{"context":{"slot":500},"value":null}`))
	got, ok := d.ContextSlot()
	assert.True(t, ok)
	assert.Equal(t, int64(500), got)

	d = newDecoded("getAccountInfo", []byte(`{"value":null}`))
	_, ok = d.ContextSlot()
	assert.False(t, ok, "envelope without context.slot")

	d = newDecoded("getAccountInfo", []byte(`{"context":{"slot":"500"},"value":null}`))
	_, ok = d.ContextSlot()
	assert.False(t, ok, "string slot is not a context slot")
}

func TestDecoded_SlotPerMethod(t *testing.T) {
	cases := []struct {
		method string
		params []any
		raw    string
		want   int64
		ok     bool
	}{
		// Method names are lowercased by the engine before newDecoded — the
		// switch here is on the lowercased forms, so tests mirror production.
		{"getblock", []any{float64(100)}, `{}`, 100, true},
		{"gettransaction", nil, `{"slot":42,"transaction":{}}`, 42, true},
		{"getblocks", nil, `[10,20,30]`, 30, true},
		{"getslot", nil, `950`, 950, true},
		{"getepochinfo", nil, `{"absoluteSlot":777}`, 777, true},
		{"getsignaturesforaddress", nil, `[{"slot":55}]`, 55, true},
		{"gethealth", nil, `{"slot":1}`, 0, false}, // unknown method: no slot claim
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			d := newDecoded(c.method, []byte(c.raw))
			d.reqParams = c.params
			got, ok := d.Slot()
			assert.Equal(t, c.ok, ok)
			if c.ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}

func TestDecoded_RequestCommitmentsScansAllParams(t *testing.T) {
	d := newDecoded("getBlock", []byte(`{}`))
	d.reqParams = []any{
		float64(100),
		map[string]any{"commitment": "finalized"},
		"noise",
		map[string]any{"commitment": ""}, // empty ignored
		map[string]any{"other": "x"},
	}
	assert.Equal(t, []string{"finalized"}, d.RequestCommitments())

	d.reqParams = []any{float64(100)}
	assert.Empty(t, d.RequestCommitments())
}

func TestDecoded_GenesisHash(t *testing.T) {
	d := newDecoded("getGenesisHash", []byte(`"abc123"`))
	got, ok := d.GenesisHash()
	assert.True(t, ok)
	assert.Equal(t, "abc123", got)

	d = newDecoded("getGenesisHash", []byte(`123`))
	_, ok = d.GenesisHash()
	assert.False(t, ok, "non-string result has no genesis hash")
}

func TestDecoded_RawJSONInt64(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`1`, 1, true},
		{`-7`, -7, true},
		{`1.5`, 0, false},
		{`"1"`, 0, false},
		{`null`, 0, false},
		{``, 0, false},
	}
	for _, c := range cases {
		got, ok := rawJSONInt64([]byte(c.raw))
		assert.Equal(t, c.ok, ok, "raw %q", c.raw)
		if c.ok {
			assert.Equal(t, c.want, got, "raw %q", c.raw)
		}
	}
}

func TestDecoded_BlocksListRejectsNonNumeric(t *testing.T) {
	d := newDecoded("getBlocks", []byte(`[10,"20"]`))
	_, err := d.BlocksList()
	assert.Error(t, err, "mixed list is not a slot list")
	assert.True(t, errors.Is(err, errNotNumeric))
}

func TestDecoded_ParseTxNotVerifiableShapes(t *testing.T) {
	// Wire-string encodings and jsonParsed-style instructions carry no
	// verifiable material — errNotVerifiable, never a hard parse error.
	_, err := parseTx([]byte(`"AAAA"`))
	assert.True(t, errors.Is(err, errNotVerifiable))

	parsedIx := []byte(`{"signatures":[],"message":{"accountKeys":["11111111111111111111111111111111"],"header":{"numRequiredSignatures":0},"recentBlockhash":"11111111111111111111111111111111","instructions":[{"programIdIndex":0,"accounts":[],"parsed":{"info":{}}}]}}`)
	_, err = parseTx(parsedIx)
	assert.True(t, errors.Is(err, errNotVerifiable), "jsonParsed instruction loses raw triplet")

	emptyData := []byte(`{"signatures":[],"message":{"accountKeys":["11111111111111111111111111111111"],"header":{"numRequiredSignatures":0},"recentBlockhash":"11111111111111111111111111111111","instructions":[{"programIdIndex":0,"accounts":[],"data":""}]}}`)
	tx, err := parseTx(emptyData)
	require.NoError(t, err, "empty-string data is legal empty instruction data")
	assert.True(t, tx.HasMessage)
	assert.Len(t, tx.Message.Instructions, 1)
	assert.Empty(t, tx.Message.Instructions[0].Data)
}
