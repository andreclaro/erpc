package integrity

// Token-authenticity checks. Solana exposes no tx Merkle root in RPC, so for
// token data the tamper-evidence is program ownership + the SPL binary
// layout: an account owned by the SPL token program MUST conform to the
// program's state layout, and token-specific query methods MUST return
// token-program-owned accounts. A synthesized response fails one of those
// immediately.
//
// Scope decisions (documented to avoid false positives):
//   - svm.auth.tokenProgram only covers getTokenAccountsByOwner/Delegate —
//     those methods assert token-ness by construction, so pinning the owner
//     program is sound there. getAccountInfo of an arbitrary address is NOT
//     pinned (any program may own it).
//   - svm.struct.tokenMintShape covers getAccountInfo where the owner IS a
//     token program and the data is exactly 82 bytes (canonical base mint,
//     incl. Token-2022 mints without extensions). Longer payloads are
//     extension-carrying variants whose type is ambiguous with token
//     accounts — skipped, never guessed.
//   - svm.struct.tokenAccountShape covers getTokenAccountsByOwner/Delegate
//     entries (every entry IS a token account there) with base64/base58
//     payload: minimum base layout + option tags + state byte. jsonParsed
//     responses are RPC-asserted structure — skipped.

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
)

func init() {
	register(tokenProgram)
	register(tokenMintShape)
	register(tokenAccountShape)
}

// Known SPL token program IDs (mainnet-beta stable, cluster-independent).
const (
	splTokenProgramID   = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	token2022ProgramID  = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEB"
	parsedTokenProgram  = "spl-token"
	parsedToken2022Prog = "spl-token-2022"
)

// SPL binary layout offsets (base layouts, cluster-agnostic).
const (
	mintLen = 82 // COption<mintAuthority>+pubkey+supply u64+decimals u8+isInitialized u8+COption<freezeAuthority>+pubkey

	tokenAccountMinLen = 165 // base SPL token account: mint32+owner32+amount u64+delegate COption32+state u8+isNative u8+delegatedAmount u64+closeAuthority COption32
	acctDelegateTagOff = 92  // u32 COption tag
	acctStateOff       = 108 // u8: 0=uninitialized 1=initialized 2=frozen
	acctCloseTagOff    = 144 // u32 COption tag
)

func isTokenProgramOwner(owner string) bool {
	return owner == splTokenProgramID || owner == token2022ProgramID
}

func isParsedTokenProgram(prog string) bool {
	return prog == parsedTokenProgram || prog == parsedToken2022Prog
}

// le32Tag reports whether b[off:off+4] is a valid SPL COption<u32> tag (0 or 1).
func le32Tag(b []byte, off int) bool {
	if off+4 > len(b) {
		return false
	}
	tag := binary.LittleEndian.Uint32(b[off : off+4])
	return tag <= 1
}

// tokenAcctView is one account-shaped value from a token or account query.
type tokenAcctView struct {
	owner string // owner program (all encodings)
	prog  string // jsonParsed data.program
	typ   string // jsonParsed data.parsed.type
	data  []byte // decoded binary payload; nil when undecodable/not binary
}

// tokenAccountViews extracts account views from getAccountInfo (single value)
// and getTokenAccountsByOwner/Delegate (value array). Absent/empty values
// yield no views.
func tokenAccountViews(d *Decoded) []tokenAcctView {
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(d.raw, &env); err != nil || len(env.Value) == 0 {
		return nil
	}
	trimmed := trimSpace(env.Value)
	switch {
	case trimmed[0] == '{':
		if v, ok := parseAcctView(env.Value); ok {
			return []tokenAcctView{v}
		}
	case trimmed[0] == '[':
		var arr []struct {
			Account json.RawMessage `json:"account"`
		}
		if err := json.Unmarshal(env.Value, &arr); err != nil {
			return nil
		}
		out := make([]tokenAcctView, 0, len(arr))
		for _, e := range arr {
			if v, ok := parseAcctView(e.Account); ok {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

// parseAcctView decodes one account object: owner string plus data in
// ["<payload>","base64"|"base58"] or jsonParsed {program, parsed:{type}}.
func parseAcctView(raw json.RawMessage) (tokenAcctView, bool) {
	var a struct {
		Owner string          `json:"owner"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return tokenAcctView{}, false
	}
	v := tokenAcctView{owner: a.Owner}
	if len(a.Data) == 0 {
		return v, true
	}
	t := trimSpace(a.Data)
	if len(t) == 0 {
		return v, true
	}
	if t[0] == '[' {
		var pair []json.RawMessage
		if err := json.Unmarshal(a.Data, &pair); err != nil || len(pair) != 2 {
			return v, true
		}
		var payload, enc string
		if json.Unmarshal(pair[0], &payload) != nil || json.Unmarshal(pair[1], &enc) != nil {
			return v, true
		}
		switch enc {
		case "base64":
			if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
				v.data = b
			}
		case "base58":
			if b, err := base58Decode(payload); err == nil {
				v.data = b
			}
		}
		return v, true
	}
	if t[0] == '{' {
		var p struct {
			Program string `json:"program"`
			Parsed  struct {
				Type string `json:"type"`
			} `json:"parsed"`
		}
		if json.Unmarshal(a.Data, &p) == nil {
			v.prog = p.Program
			v.typ = p.Parsed.Type
		}
	}
	return v, true
}

// svm.auth.tokenProgram — token-specific queries must return token-program
// accounts. A response naming a bogus owner (or parsed program) is
// synthesized token data.
var tokenProgram = &Check{
	ID:      "svm.auth.tokenProgram",
	Family:  FamilyAuthenticity,
	Class:   Deterministic,
	Methods: []string{"gettokenaccountsbyowner", "gettokenaccountsbydelegate"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		views := tokenAccountViews(d)
		if len(views) == 0 {
			return Skipped
		}
		for i, v := range views {
			switch {
			case v.prog != "":
				if !isParsedTokenProgram(v.prog) {
					return failf("token account [%d]: jsonParsed names program %q — not an SPL token program", i, v.prog)
				}
			case v.owner != "":
				if !isTokenProgramOwner(v.owner) {
					return failf("token account [%d]: owner %q is not an SPL token program (synthesized token data?)", i, v.owner)
				}
			}
		}
		return nil
	},
}

// svm.struct.tokenMintShape — an account owned by the token program with the
// canonical 82-byte payload MUST be a base mint: valid COption tags and a
// boolean isInitialized byte.
var tokenMintShape = &Check{
	ID:      "svm.struct.tokenMintShape",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getaccountinfo", "getaccountinfobase64"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		verified := false
		for i, v := range tokenAccountViews(d) {
			if !isTokenProgramOwner(v.owner) || v.data == nil {
				continue
			}
			if len(v.data) != mintLen {
				// Extension-carrying variants are ambiguous with token
				// accounts — another check's territory; never guess.
				continue
			}
			verified = true
			b := v.data
			if !le32Tag(b, 0) {
				return failf("mint [%d]: mintAuthority COption tag %d is not 0/1", i, binary.LittleEndian.Uint32(b[0:4]))
			}
			if !le32Tag(b, 46) {
				return failf("mint [%d]: freezeAuthority COption tag %d is not 0/1", i, binary.LittleEndian.Uint32(b[46:50]))
			}
			if b[45] > 1 {
				return failf("mint [%d]: isInitialized byte %d is not 0/1", i, b[45])
			}
		}
		if !verified {
			return Skipped
		}
		return nil
	},
}

// svm.struct.tokenAccountShape — getTokenAccountsByOwner/Delegate entries
// with a binary payload must carry the base SPL token-account layout.
var tokenAccountShape = &Check{
	ID:      "svm.struct.tokenAccountShape",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"gettokenaccountsbyowner", "gettokenaccountsbydelegate"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		views := tokenAccountViews(d)
		if len(views) == 0 {
			return Skipped
		}
		verified := false
		for i, v := range views {
			if v.data == nil {
				continue // jsonParsed — RPC-asserted structure
			}
			verified = true
			b := v.data
			if len(b) < tokenAccountMinLen {
				return failf("token account [%d]: %d-byte payload is below the %d-byte base SPL layout", i, len(b), tokenAccountMinLen)
			}
			if !le32Tag(b, acctDelegateTagOff) {
				return failf("token account [%d]: delegate COption tag %d is not 0/1", i, binary.LittleEndian.Uint32(b[acctDelegateTagOff:acctDelegateTagOff+4]))
			}
			if !le32Tag(b, acctCloseTagOff) {
				return failf("token account [%d]: closeAuthority COption tag %d is not 0/1", i, binary.LittleEndian.Uint32(b[acctCloseTagOff:acctCloseTagOff+4]))
			}
			if st := b[acctStateOff]; st > 2 {
				return failf("token account [%d]: state byte %d is not 0/1/2 (uninitialized/initialized/frozen)", i, st)
			}
		}
		if !verified {
			return Skipped
		}
		return nil
	},
}
