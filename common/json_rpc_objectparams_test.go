package common

import (
	"testing"

	"github.com/erpc/erpc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { util.ConfigureTestLogger() }

// NEAR and other by-name services require object-form params; the typed
// Params view wraps the object as a single-element array (the documented
// finalityFromParam lookup convention), while ObjectParams carries the
// verbatim object for wire re-emission.
func TestJsonRpcRequest_ObjectParamsRoundTrip(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"block","params":{"finality":"final"}}`)

	var req JsonRpcRequest
	require.NoError(t, req.UnmarshalJSON(body))

	// Typed view: wrapped single-element array.
	require.Len(t, req.Params, 1)
	obj, ok := req.Params[0].(map[string]interface{})
	require.True(t, ok, "wrapped param must be the object")
	assert.Equal(t, "final", obj["finality"])

	// Verbatim carrier: the original object, untouched.
	wire := req.ObjectParams()
	require.NotNil(t, wire)
	assert.Equal(t, "final", wire["finality"])

	// Key-scan introspection (finalityFromParam convention): finds the key
	// inside any object param.
	found := ""
	for _, p := range req.Params {
		if m, isObj := p.(map[string]interface{}); isObj {
			if v, isStr := m["finality"].(string); isStr && v != "" {
				found = v
				break
			}
		}
	}
	assert.Equal(t, "final", found)
}

func TestJsonRpcRequest_ObjectParamsClone(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"block","params":{"finality":"final"}}`)
	var req JsonRpcRequest
	require.NoError(t, req.UnmarshalJSON(body))

	clone := req.Clone()
	require.NotNil(t, clone.ObjectParams())
	assert.Equal(t, "final", clone.ObjectParams()["finality"])

	// Mutating the clone's typed view must not corrupt the original carrier.
	clone.Params[0].(map[string]interface{})["finality"] = "optimistic"
	assert.Equal(t, "final", req.ObjectParams()["finality"])
}

func TestJsonRpcRequest_ArrayParamsUnchanged(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_blockNumber","params":[]}`)
	var req JsonRpcRequest
	require.NoError(t, req.UnmarshalJSON(body))
	assert.Nil(t, req.ObjectParams(), "array-form params must not set the object carrier")
	require.Len(t, req.Params, 0)
}

func TestJsonRpcRequest_EthQueryStillDropsParams(t *testing.T) {
	// The MIP-16 behavior (eth_query* with non-array params → params dropped
	// so the query parser answers -32602) must survive the object-params path.
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_queryBalance","params":{"bad":"object"}}`)
	var req JsonRpcRequest
	require.NoError(t, req.UnmarshalJSON(body))
	assert.Nil(t, req.Params)
	assert.Nil(t, req.ObjectParams())
	assert.Equal(t, "eth_queryBalance", req.Method)
}

func TestJsonRpcRequest_MalformedParamsStillRejected(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"m","params":"a string"}`,
		`{"jsonrpc":"2.0","id":1,"method":"m","params":42}`,
		`{"jsonrpc":"2.0","id":1,"method":"m","params":[}`,
	} {
		var req JsonRpcRequest
		assert.Error(t, req.UnmarshalJSON([]byte(body)), "body %s", body)
	}
}
