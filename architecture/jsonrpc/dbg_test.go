package jsonrpc

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/erpc/erpc/common"
)

func TestDbg429(t *testing.T) {
	jrErr := common.NewErrJsonRpcExceptionExternal(-32603, "slow down", "")
	fmt.Printf("jrErr type %T code %d\n", jrErr, jrErr.Code)
	jr := common.MustNewJsonRpcResponse(1, nil, jrErr)
	fmt.Printf("jr nil? %v jr.Error nil? %v code %d\n", jr == nil, jr.Error == nil, jr.Error.Code)
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: 429, Header: http.Header{}}
	err := e.Extract(r, nil, jr, newJsonRpcStub())
	fmt.Printf("err: %v\n", err)
}
