package erpc

import (
	"bytes"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/require"
)

func TestNewSvmJsonRpcResultResponsePreservesRequestID(t *testing.T) {
	for _, id := range []string{`"client"`, `null`, `9007199254740993`, `18446744073709551615`} {
		nq := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"accountSubscribe","params":[]}`))
		jr, err := nq.JsonRpcRequest()
		require.NoError(t, err)
		resp := newSvmJsonRpcResultResponse(nq, jr, []byte(`0`))
		var buf bytes.Buffer
		_, err = resp.WriteTo(&buf)
		require.NoError(t, err)
		require.Contains(t, buf.String(), `"id":`+id)
	}
}
