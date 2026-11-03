package integrity

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Decoded is the lazily-parsed view of one upstream response, shared across
// the checks for a method. Each accessor parses its shape once and caches the
// result; a parse failure is sticky so checks see the same "cannot parse"
// outcome (and skip — chain-safety: unparseable data is never a violation).
type Decoded struct {
	method    string
	raw       []byte
	reqParams []any

	blockParsed bool
	block       *blockResult
	blockErr    error

	// blockTxs caches BlockTxs' per-tx parse: txShape, sigUniqueness and
	// signatureVerify all funnel through it, and re-parsing 20 txs per
	// check costs ~290us of shared work per getBlock response.
	blockTxsParsed bool
	blockTxs       []*parsedTx
	blockTxsErr    error

	txParsed bool
	tx       *txEnvelopeResult
	txErr    error

	sigListParsed bool
	sigList       []sigEntry
	sigListErr    error

	blocksListParsed bool
	blocksList       []int64
	blocksListErr    error

	commitmentParsed bool
	commitment       *blockCommitmentResult
	commitmentErr    error

	slotNumParsed bool
	slotNum       int64
	slotNumOK     bool

	// contextSlotParsed caches the envelope scan: responseSlot() in the
	// finality checks reads ContextSlot on every check, and the raw
	// unmarshal re-validates the whole document each time (~100us on a
	// 20-tx getBlock). Parse once, sticky like every other accessor.
	contextSlotParsed bool
	contextSlot       int64
	contextSlotOK     bool

	// chain is the network's verified-block index (Input.Chain), feeding the
	// commitment-tier link checks.
	chain *ChainState
	// finality is Input.Finality, exposed to checks that compare response
	// slots against the upstream's observed tips.
	finality FinalityResolver

	epochInfoParsed bool
	epochInfo       *epochInfoResult
	epochInfoErr    error

	genesisParsed bool
	genesis       string
}

func newDecoded(method string, raw []byte) *Decoded {
	return &Decoded{method: method, raw: raw}
}

// ---------- getBlock / getConfirmedBlock ----------

type blockResult struct {
	Blockhash         string            `json:"blockhash"`
	PreviousBlockhash string            `json:"previousBlockhash"`
	ParentSlot        *int64            `json:"parentSlot"`
	BlockHeight       *int64            `json:"blockHeight"`
	BlockTime         *int64            `json:"blockTime"`
	Transactions      []json.RawMessage `json:"transactions"`
	Rewards           []json.RawMessage `json:"rewards"`
	// Signatures is the top-level signature array Agave emits when the
	// request set transactionDetails:"signatures" (transactions is omitted
	// in that mode).
	Signatures []string `json:"signatures"`
}

func (d *Decoded) Block() (*blockResult, error) {
	if d.blockParsed {
		return d.block, d.blockErr
	}
	d.blockParsed = true
	var b blockResult
	if err := json.Unmarshal(d.raw, &b); err != nil {
		d.blockErr = err
		return nil, err
	}
	d.block = &b
	return d.block, nil
}

// Block parses to a non-nil value with non-empty hashes (i.e. the result is a
// real block object, not null / a string / a bare list).
func (d *Decoded) hasBlock() bool {
	b, err := d.Block()
	return err == nil && b != nil && b.Blockhash != ""
}

// RequestedSlot returns the slot the getBlock/getConfirmedBlock request asked
// for (params[0]), when present and numeric.
func (d *Decoded) RequestedSlot() (int64, bool) {
	return paramSlot(d.reqParams, 0)
}

// requestTransactionDetails returns the transactionDetails level a getBlock
// request asked for (params[1].transactionDetails), when present. Agave
// honors "full" (default), "accounts", "signatures", and "none" — the
// latter two change which top-level fields the response carries.
func (d *Decoded) requestTransactionDetails() (string, bool) {
	if len(d.reqParams) < 2 {
		return "", false
	}
	m, ok := d.reqParams[1].(map[string]any)
	if !ok {
		return "", false
	}
	td, ok := m["transactionDetails"].(string)
	return td, ok && td != ""
}

// ---------- getTransaction ----------

type txEnvelopeResult struct {
	Slot        *int64          `json:"slot"`
	BlockTime   *int64          `json:"blockTime"`
	Transaction json.RawMessage `json:"transaction"`
}

func (d *Decoded) TxEnvelope() (*txEnvelopeResult, error) {
	if d.txParsed {
		return d.tx, d.txErr
	}
	d.txParsed = true
	var t txEnvelopeResult
	if err := json.Unmarshal(d.raw, &t); err != nil {
		d.txErr = err
		return nil, err
	}
	d.tx = &t
	return d.tx, nil
}

// ---------- getSignaturesForAddress ----------

type sigEntry struct {
	Signature string `json:"signature"`
	Slot      *int64 `json:"slot"`
	Err       any    `json:"err"`
}

func (d *Decoded) SigList() ([]sigEntry, error) {
	if d.sigListParsed {
		return d.sigList, d.sigListErr
	}
	d.sigListParsed = true
	if err := json.Unmarshal(d.raw, &d.sigList); err != nil {
		d.sigListErr = err
		return nil, err
	}
	return d.sigList, nil
}

// ---------- getBlocks / getBlocksWithLimit ----------

func (d *Decoded) BlocksList() ([]int64, error) {
	if d.blocksListParsed {
		return d.blocksList, d.blocksListErr
	}
	d.blocksListParsed = true
	var raw []json.RawMessage
	if err := json.Unmarshal(d.raw, &raw); err != nil {
		d.blocksListErr = err
		return nil, err
	}
	out := make([]int64, 0, len(raw))
	for _, r := range raw {
		n, ok := rawJSONInt64(r)
		if !ok {
			d.blocksListErr = errNotNumeric
			return nil, d.blocksListErr
		}
		out = append(out, n)
	}
	d.blocksList = out
	return out, nil
}

// ---------- getBlockCommitment ----------

type blockCommitmentResult struct {
	Commitment []json.RawMessage `json:"commitment"`
	TotalStake *int64            `json:"totalStake"`
}

func (d *Decoded) BlockCommitment() (*blockCommitmentResult, error) {
	if d.commitmentParsed {
		return d.commitment, d.commitmentErr
	}
	d.commitmentParsed = true
	var c blockCommitmentResult
	if err := json.Unmarshal(d.raw, &c); err != nil {
		d.commitmentErr = err
		return nil, err
	}
	d.commitment = &c
	return d.commitment, nil
}

// ---------- bare slot-number results (getSlot / getBlockHeight / ...) ----------

func (d *Decoded) SlotNumber() (int64, bool) {
	if d.slotNumParsed {
		return d.slotNum, d.slotNumOK
	}
	d.slotNumParsed = true
	n, ok := rawJSONInt64(d.raw)
	if !ok {
		return 0, false
	}
	d.slotNum, d.slotNumOK = n, true
	return n, true
}

// ---------- getEpochInfo ----------

type epochInfoResult struct {
	AbsoluteSlot *int64 `json:"absoluteSlot"`
	BlockHeight  *int64 `json:"blockHeight"`
	SlotIndex    *int64 `json:"slotIndex"`
	Epoch        *int64 `json:"epoch"`
}

func (d *Decoded) EpochInfo() (*epochInfoResult, error) {
	if d.epochInfoParsed {
		return d.epochInfo, d.epochInfoErr
	}
	d.epochInfoParsed = true
	var e epochInfoResult
	if err := json.Unmarshal(d.raw, &e); err != nil {
		d.epochInfoErr = err
		return nil, err
	}
	d.epochInfo = &e
	return d.epochInfo, nil
}

// ---------- getGenesisHash ----------

func (d *Decoded) GenesisHash() (string, bool) {
	if d.genesisParsed {
		return d.genesis, d.genesis != ""
	}
	d.genesisParsed = true
	if err := json.Unmarshal(d.raw, &d.genesis); err != nil {
		return "", false
	}
	return d.genesis, d.genesis != ""
}

// ---------- transaction parsing ----------

// parsedTx is a normalized view of a transaction, accepting both the
// encoding:"json" and encoding:"jsonParsed" shapes (accountKeys as bare
// base58 strings or {pubkey,...} objects).
type parsedTx struct {
	Signatures []string
	Message    parsedMessage
	HasMessage bool
}

type parsedMessage struct {
	// Version: 0 = legacy, 1 = v0 (address-table-lookup) message.
	Version               int
	NumRequiredSignatures int
	NumReadonlySigned     int
	NumReadonlyUnsigned   int
	AccountKeys           []string
	RecentBlockhash       string
	Instructions          []parsedInstr
	Lookups               []parsedLookup
}

type parsedInstr struct {
	ProgramIDIndex int
	Accounts       []byte
	Data           []byte
}

type parsedLookup struct {
	AccountKey      string
	WritableIndexes []byte
	ReadonlyIndexes []byte
}

// rawKey unmarshals a base58 string or a {pubkey: ...} object (jsonParsed).
type rawKey struct {
	Pubkey string `json:"pubkey"`
}

func parseTx(raw json.RawMessage) (*parsedTx, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '"' {
		// encoding base64/base58/raw wire or transactionDetails:"signatures" —
		// no verifiable message material in this encoding; checks skip.
		return nil, errNotVerifiable
	}
	var obj struct {
		Signatures []string `json:"signatures"`
		Version    any      `json:"version"`
		Message    struct {
			AccountKeys []json.RawMessage `json:"accountKeys"`
			Header      struct {
				NumRequiredSignatures       int `json:"numRequiredSignatures"`
				NumReadonlySignedAccounts   int `json:"numReadonlySignedAccounts"`
				NumReadonlyUnsignedAccounts int `json:"numReadonlyUnsignedAccounts"`
			} `json:"header"`
			RecentBlockhash     string            `json:"recentBlockhash"`
			Instructions        []json.RawMessage `json:"instructions"`
			AddressTableLookups []json.RawMessage `json:"addressTableLookups"`
		} `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, err
	}
	tx := &parsedTx{Signatures: obj.Signatures}
	msg := &tx.Message
	msg.NumRequiredSignatures = obj.Message.Header.NumRequiredSignatures
	msg.NumReadonlySigned = obj.Message.Header.NumReadonlySignedAccounts
	msg.NumReadonlyUnsigned = obj.Message.Header.NumReadonlyUnsignedAccounts
	msg.RecentBlockhash = obj.Message.RecentBlockhash
	if len(obj.Message.AccountKeys) == 0 {
		return nil, errNotVerifiable
	}
	for _, rk := range obj.Message.AccountKeys {
		var key string
		trimmedKey := bytes.TrimSpace(rk)
		if len(trimmedKey) > 0 && trimmedKey[0] == '{' {
			var k rawKey
			if err := json.Unmarshal(trimmedKey, &k); err != nil {
				return nil, err
			}
			key = k.Pubkey
		} else {
			if err := json.Unmarshal(trimmedKey, &key); err != nil {
				return nil, err
			}
		}
		if key == "" {
			return nil, errNotVerifiable
		}
		msg.AccountKeys = append(msg.AccountKeys, key)
	}
	for _, ri := range obj.Message.Instructions {
		inst, err := parseInstr(ri)
		if err != nil {
			return nil, err
		}
		msg.Instructions = append(msg.Instructions, inst)
	}
	for _, rl := range obj.Message.AddressTableLookups {
		lk, err := parseLookup(rl)
		if err != nil {
			return nil, err
		}
		msg.Lookups = append(msg.Lookups, lk)
	}
	// Version: v0 iff the message carries address-table lookups, or the tx
	// declares version 0 explicitly.
	msg.Version = 0
	if len(msg.Lookups) > 0 {
		msg.Version = 1
	} else if v, ok := obj.Version.(float64); ok && v == 0 {
		msg.Version = 1
	} else if s, ok := obj.Version.(string); ok && s != "legacy" && s != "" {
		return nil, errNotVerifiable
	}
	tx.HasMessage = true
	return tx, nil
}

// parseInstr decodes one instruction. An empty-string data field carries
// genuinely empty instruction data (base58 of zero bytes is "") — that is
// legal and must not fail decode. A data field that is ABSENT marks a
// jsonParsed-style instruction: getBlock/getTransaction with
// encoding:"jsonParsed" replace raw instructions with a digested
// {"program", "parsed": ...} object (known-program instructions lose the
// raw triplet entirely), so the signed wire bytes are not reconstructible —
// the transaction is unverifiable rather than malformed.
func parseInstr(raw json.RawMessage) (parsedInstr, error) {
	var obj struct {
		ProgramIDIndex int             `json:"programIdIndex"`
		Accounts       []int           `json:"accounts"`
		Data           *string         `json:"data"`
		Parsed         json.RawMessage `json:"parsed"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return parsedInstr{}, err
	}
	if obj.Parsed != nil || obj.Data == nil {
		return parsedInstr{}, errNotVerifiable
	}
	inst := parsedInstr{ProgramIDIndex: obj.ProgramIDIndex}
	for _, a := range obj.Accounts {
		if a < 0 || a > 255 {
			return parsedInstr{}, errOutOfRange
		}
		inst.Accounts = append(inst.Accounts, byte(a))
	}
	if *obj.Data != "" {
		data, err := base58Decode(*obj.Data)
		if err != nil {
			return parsedInstr{}, err
		}
		inst.Data = data
	}
	return inst, nil
}

func parseLookup(raw json.RawMessage) (parsedLookup, error) {
	var obj struct {
		AccountKey      string `json:"accountKey"`
		WritableIndexes []int  `json:"writableIndexes"`
		ReadonlyIndexes []int  `json:"readonlyIndexes"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return parsedLookup{}, err
	}
	lk := parsedLookup{AccountKey: obj.AccountKey}
	for _, i := range obj.WritableIndexes {
		if i < 0 || i > 255 {
			return parsedLookup{}, errOutOfRange
		}
		lk.WritableIndexes = append(lk.WritableIndexes, byte(i))
	}
	for _, i := range obj.ReadonlyIndexes {
		if i < 0 || i > 255 {
			return parsedLookup{}, errOutOfRange
		}
		lk.ReadonlyIndexes = append(lk.ReadonlyIndexes, byte(i))
	}
	return lk, nil
}

// BlockTxs iterates the block's transactions, normalizing each into a parsedTx.
// A transaction the check cannot fully model (wire-string encoding, missing
// message) yields errNotVerifiable for that entry — callers skip the whole
// check on ANY such entry (chain-safety). The parse is cached and sticky:
// every check that inspects transactions shares one parse per response.
func (d *Decoded) BlockTxs() ([]*parsedTx, error) {
	if d.blockTxsParsed {
		return d.blockTxs, d.blockTxsErr
	}
	d.blockTxsParsed = true
	b, err := d.Block()
	if err != nil {
		d.blockTxsErr = err
		return nil, err
	}
	if b.Transactions == nil {
		d.blockTxsErr = errNotVerifiable
		return nil, errNotVerifiable
	}
	out := make([]*parsedTx, 0, len(b.Transactions))
	for _, rt := range b.Transactions {
		tx, err := parseTx(rt)
		if err != nil {
			d.blockTxsErr = err
			return nil, err
		}
		out = append(out, tx)
	}
	d.blockTxs = out
	return out, nil
}

// TxForVerify returns the parsed transaction for getTransaction.
func (d *Decoded) TxForVerify() (*parsedTx, error) {
	t, err := d.TxEnvelope()
	if err != nil {
		return nil, err
	}
	if len(t.Transaction) == 0 {
		return nil, errNotVerifiable
	}
	return parseTx(t.Transaction)
}

// ---------- context envelope (result.context.slot) ----------

// ContextSlot extracts result.context.slot for envelope-carrying methods. ok
// is false when the envelope or slot is absent.
func (d *Decoded) ContextSlot() (int64, bool) {
	if d.contextSlotParsed {
		return d.contextSlot, d.contextSlotOK
	}
	d.contextSlotParsed = true
	var env struct {
		Context struct {
			Slot json.RawMessage `json:"slot"`
		} `json:"context"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(d.raw, &env); err != nil {
		return 0, false
	}
	if len(env.Context.Slot) == 0 {
		return 0, false
	}
	d.contextSlot, d.contextSlotOK = rawJSONInt64(env.Context.Slot)
	return d.contextSlot, d.contextSlotOK
}

// ---------- chain index feed ----------

// observeBlock records a fully-verified block in the chain index.
func observeBlock(cs *ChainState, d *Decoded) {
	b, err := d.Block()
	if err != nil || b == nil || b.Blockhash == "" || b.ParentSlot == nil {
		return
	}
	self, err := base58Decode(b.Blockhash)
	if err != nil || len(self) != 32 {
		return
	}
	prev, err := base58Decode(b.PreviousBlockhash)
	if err != nil || len(prev) != 32 {
		return
	}
	slot, ok := d.RequestedSlot()
	if !ok || slot < 0 {
		return
	}
	height := int64(-1)
	if b.BlockHeight != nil {
		height = *b.BlockHeight
	}
	bt := int64(-1)
	if b.BlockTime != nil {
		bt = *b.BlockTime
	}
	cs.Observe(chainEntry{
		slot:        slot,
		blockhash:   toHash32(self),
		blockHeight: height,
		parentSlot:  *b.ParentSlot,
		parentHash:  toHash32(prev),
		blockTime:   bt,
	})
}

// ---------- shared helpers ----------

// Slot is the best-known slot for this response, used for finality resolution.
func (d *Decoded) Slot() (int64, bool) {
	switch d.method {
	case "getblock", "getconfirmedblock":
		return d.RequestedSlot()
	case "gettransaction":
		if t, err := d.TxEnvelope(); err == nil && t.Slot != nil {
			return *t.Slot, true
		}
	case "getblocks", "getblockswithlimit":
		if l, err := d.BlocksList(); err == nil && len(l) > 0 {
			return l[len(l)-1], true
		}
	case "getslot", "getblockheight", "gettransactioncount":
		return d.SlotNumber()
	case "getepochinfo":
		if e, err := d.EpochInfo(); err == nil && e.AbsoluteSlot != nil {
			return *e.AbsoluteSlot, true
		}
	case "getsignaturesforaddress":
		if l, err := d.SigList(); err == nil && len(l) > 0 && l[0].Slot != nil {
			return *l[0].Slot, true
		}
	}
	return 0, false
}

func paramSlot(params []any, i int) (int64, bool) {
	if len(params) <= i {
		return 0, false
	}
	switch p := params[i].(type) {
	case float64:
		return int64(p), true
	case json.Number:
		n, err := p.Int64()
		return n, err == nil
	case int64:
		return p, true
	case int:
		return int64(p), true
	case string:
		n, err := strconv.ParseInt(p, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// RequestCommitments scans the request params' config objects for a
// "commitment" string.
func (d *Decoded) RequestCommitments() []string {
	var out []string
	for _, p := range d.reqParams {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if c, ok := m["commitment"].(string); ok && c != "" {
			out = append(out, c)
		}
	}
	return out
}

// rawJSONInt64 parses a JSON value as an integer (rejects floats, strings,
// null — the shape gate, not a coercion).
func rawJSONInt64(raw json.RawMessage) (int64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return i, true
		}
		// Reject non-integral numbers explicitly.
		if _, err := n.Float64(); err == nil {
			return 0, false
		}
	}
	return 0, false
}

var (
	errNotVerifiable = errString("encoding or shape carries no verifiable material")
	errNotNumeric    = errString("expected a JSON number")
	errOutOfRange    = errString("index out of u8 range")
)

type errString string

func (e errString) Error() string { return string(e) }
