package jsonrpc

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/erpc/erpc/common"
)

// pathSegment is one step of a parsed dot-path: either an object key or a
// numeric array index.
type pathSegment struct {
	key   string
	index int
	isIdx bool
}

// parseDotPath parses a "$"-rooted dot path into segments. Supported grammar:
//
//	$                     root only
//	$.a.b                 object keys
//	$.a[0].b / $.a.0.b    array indexes (bracket or bare-numeric segment)
//
// A bare-numeric segment is treated as an array index whenever the current
// value is a slice, and as an object key otherwise — chains vary in how they
// key maps, and the walker never invents a resolution the data doesn't support.
func parseDotPath(path string) ([]pathSegment, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	if path == "$" {
		return nil, true
	}
	if !strings.HasPrefix(path, "$.") && !strings.HasPrefix(path, "$[") {
		return nil, false
	}
	rest := path[1:]
	var segs []pathSegment
	i := 0
	for i < len(rest) {
		switch rest[i] {
		case '.':
			i++
		case '[':
			j := strings.IndexByte(rest[i:], ']')
			if j < 0 {
				return nil, false
			}
			idx, err := strconv.Atoi(strings.TrimSpace(rest[i+1 : i+j]))
			if err != nil || idx < 0 {
				return nil, false
			}
			segs = append(segs, pathSegment{index: idx, isIdx: true})
			i += j + 1
		default:
			j := i
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			segs = append(segs, pathSegment{key: rest[i:j]})
			i = j
		}
	}
	return segs, true
}

// walkSegments resolves parsed segments against a decoded JSON value. The
// boolean reports whether the FULL path resolved; an explicit JSON null at the
// leaf returns (nil, true) so IsNull-style matchers can distinguish
// "present but null" from "missing".
func walkSegments(root interface{}, segs []pathSegment) (interface{}, bool) {
	cur := root
	for _, seg := range segs {
		if seg.isIdx || isArrayIndexSegment(seg, cur) {
			idx := seg.index
			if !seg.isIdx {
				idx, _ = strconv.Atoi(seg.key)
			}
			arr, ok := cur.([]interface{})
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			continue
		}
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		v, exists := m[seg.key]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func isArrayIndexSegment(seg pathSegment, cur interface{}) bool {
	if seg.isIdx {
		return true
	}
	if _, ok := cur.([]interface{}); !ok {
		return false
	}
	_, err := strconv.Atoi(seg.key)
	return err == nil
}

// walkDotPath resolves a "$"-rooted dot path against a decoded JSON value.
func walkDotPath(root interface{}, path string) (interface{}, bool) {
	segs, ok := parseDotPath(path)
	if !ok {
		return nil, false
	}
	return walkSegments(root, segs)
}

// walkScopedPath resolves a classification/probe path. The configured paths are
// whole-response paths ($.error.cause.name, $.result.status), so the primary
// attempt walks from the response root. Defensively — because operators write
// both styles — a full-path miss retries against the named subtree, both with
// the scope prefix stripped ($.error.cause.name under the error object) and
// subtree-relative ($.cause.name under the error object).
func walkScopedPath(root map[string]interface{}, subtree interface{}, scopeKey, path string) (interface{}, bool) {
	if v, ok := walkDotPath(root, path); ok {
		return v, true
	}
	if subtree == nil {
		return nil, false
	}
	if segs, ok := parseDotPath(path); ok && len(segs) > 0 && !segs[0].isIdx && segs[0].key == scopeKey {
		if v, ok := walkSegments(subtree, segs[1:]); ok {
			return v, true
		}
	}
	return walkDotPath(subtree, path)
}

// stringifyMatchValue renders a decoded JSON value for comparison. JSON numbers
// arrive as float64 — 24.0 must compare equal to the config string "24", so
// float64 uses FormatFloat with precision -1 (no trailing ".0"). The boolean is
// false for JSON null / Go nil, so Equals/In never match a null.
func stringifyMatchValue(v interface{}) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case int32:
		return strconv.FormatInt(int64(t), 10), true
	case fmt.Stringer:
		return t.String(), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// matchEqualsIn compares an extracted value against the Equals/In members of a
// matcher config. Both sides are stringified first (see stringifyMatchValue), so
// chain codes that arrive as numbers (Starknet 24) or strings (XRPL
// "txnNotFound") both work — that cross-encoding tolerance is the point of the
// stringification rule.
func matchEqualsIn(extracted interface{}, equals interface{}, in []interface{}) bool {
	if equals != nil {
		es, ok := stringifyMatchValue(equals)
		if !ok {
			return false
		}
		vs, vok := stringifyMatchValue(extracted)
		return vok && es == vs
	}
	if len(in) > 0 {
		vs, vok := stringifyMatchValue(extracted)
		if !vok {
			return false
		}
		for _, m := range in {
			if ms, ok := stringifyMatchValue(m); ok && ms == vs {
				return true
			}
		}
	}
	return false
}

// matchClassificationMatch evaluates one JsonRpcClassificationMatchConfig
// against a whole-response root. ErrorPath inspects the JSON-RPC error subtree,
// ResultPath the result subtree; IsNull matches JSON null, a missing key, or a
// mid-walk absence — per spec, "missing" reads as null for the IsNull form only.
func matchClassificationMatch(m *common.JsonRpcClassificationMatchConfig, root map[string]interface{}, resultSub, errSub interface{}) bool {
	if m == nil {
		return false
	}
	path := m.ResultPath
	subtree := resultSub
	scope := "result"
	if m.ErrorPath != "" {
		path = m.ErrorPath
		subtree = errSub
		scope = "error"
	}
	if path == "" {
		return false
	}
	v, ok := walkScopedPath(root, subtree, scope, path)
	if m.IsNull {
		return !ok || v == nil
	}
	if !ok {
		return false
	}
	return matchEqualsIn(v, m.Equals, m.In)
}

// matchUnhealthyWhen evaluates a poller probe's UnhealthyWhen matcher against
// the probe response root. A path that does not resolve never matches — an
// absent signal is not evidence of unhealthiness.
func matchUnhealthyWhen(m *common.JsonRpcValueMatcherConfig, root map[string]interface{}) bool {
	if m == nil || m.Path == "" {
		return false
	}
	v, ok := walkDotPath(root, m.Path)
	if !ok {
		return false
	}
	return matchEqualsIn(v, m.Equals, m.In)
}

// buildResponseRoot materializes a whole-response object {"result": …, "error":
// …} from a parsed JSON-RPC response, so configured paths can address both
// subtrees with one walker. Absent members are omitted (not nulled) so IsNull
// can tell "missing" from "present but null".
func buildResponseRoot(jrr *common.JsonRpcResponse) map[string]interface{} {
	root := map[string]interface{}{}
	if jrr == nil {
		return root
	}
	if rb := jrr.GetResultBytes(); len(rb) > 0 {
		var decoded interface{}
		if err := json.Unmarshal(rb, &decoded); err == nil {
			root["result"] = decoded
		}
	}
	if jrr.Error != nil {
		errObj := map[string]interface{}{
			"code":    jrr.Error.Code,
			"message": jrr.Error.Message,
		}
		if jrr.Error.Data != nil {
			errObj["data"] = jrr.Error.Data
		}
		root["error"] = errObj
	}
	return root
}

// parseDataFinalityState mirrors the accepted values of
// common.DataFinalityState.UnmarshalYAML. Anything unmapped — including an
// operator typo in a finalityFromParam map value — degrades to Unknown rather
// than inventing a class.
func parseDataFinalityState(s string) common.DataFinalityState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "finalized", "0":
		return common.DataFinalityStateFinalized
	case "unfinalized", "1":
		return common.DataFinalityStateUnfinalized
	case "realtime", "2":
		return common.DataFinalityStateRealtime
	case "unknown", "3":
		return common.DataFinalityStateUnknown
	}
	return common.DataFinalityStateUnknown
}

// parseCounterValue decodes a probe-extracted value per the configured
// encoding: auto (default) accepts a JSON number, a decimal string, or a
// 0x/0X-prefixed hex string; number|string|hex force one form. An unparseable
// sample is an error — the caller skips that counter's tick, never the loop.
func parseCounterValue(raw interface{}, encoding string) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "number":
		f, ok := raw.(float64)
		if !ok {
			return 0, fmt.Errorf("encoding=number requires a JSON number, got %T", raw)
		}
		return int64(f), nil
	case "string":
		s, ok := raw.(string)
		if !ok {
			return 0, fmt.Errorf("encoding=string requires a decimal string, got %T", raw)
		}
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("encoding=string cannot parse %q: %w", s, err)
		}
		return v, nil
	case "hex":
		s, ok := raw.(string)
		if !ok {
			return 0, fmt.Errorf("encoding=hex requires a 0x-prefixed string, got %T", raw)
		}
		v, err := parseHexInt(s)
		if err != nil {
			return 0, err
		}
		return v, nil
	default: // "auto" and anything unexpected: accept every well-formed form
		switch t := raw.(type) {
		case float64:
			return int64(t), nil
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				return 0, fmt.Errorf("encoding=auto cannot parse empty string")
			}
			if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
				return parseHexInt(s)
			}
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("encoding=auto cannot parse %q: %w", s, err)
			}
			return v, nil
		default:
			return 0, fmt.Errorf("encoding=auto cannot parse %T", raw)
		}
	}
}

func parseHexInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return 0, fmt.Errorf("hex value %q is missing 0x prefix", s)
	}
	v, err := strconv.ParseInt(s[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse hex value %q: %w", s, err)
	}
	return v, nil
}

// parseRangeStart keeps the start of the FIRST range in a (possibly disjoint)
// ledger range string: "32570-8812345" → 32570, "32570-8812345,99000000-100000000"
// → 32570. Anything else — "empty" (unsynced XRPL nodes), a non-numeric start, a
// non-range value — reports unknown; the availability floor is best-effort and
// must never collapse to 0.
func parseRangeStart(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	dash := strings.IndexByte(s, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(s[:dash]), 10, 64)
	if err != nil || start <= 0 {
		return 0, false
	}
	return start, true
}
