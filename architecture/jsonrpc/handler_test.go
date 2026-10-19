package jsonrpc

import (
	"context"
	"errors"
	"testing"

	"github.com/erpc/erpc/common"
)

// The generic architecture carries no protocol logic: every pre-forward hook
// declines to handle (false/nil/nil) and every post-forward hook passes the
// (resp, err) pair through untouched.
func TestHandler_AllHooksPassThrough(t *testing.T) {
	t.Parallel()
	h := NewJsonRpcArchitectureHandler()
	ctx := context.Background()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getQuote","params":[]}`))
	resp := common.NewNormalizedResponse()
	sentinel := errors.New("upstream boom")

	handled, r, err := h.HandleProjectPreForward(ctx, nil, req)
	if handled || r != nil || err != nil {
		t.Errorf("HandleProjectPreForward = (%v, %v, %v), want (false, nil, nil)", handled, r, err)
	}

	handled, r, err = h.HandleNetworkPreForward(ctx, nil, nil, req)
	if handled || r != nil || err != nil {
		t.Errorf("HandleNetworkPreForward = (%v, %v, %v), want (false, nil, nil)", handled, r, err)
	}

	gotResp, gotErr := h.HandleNetworkPostForward(ctx, nil, req, resp, sentinel)
	if gotResp != resp || !errors.Is(gotErr, sentinel) {
		t.Errorf("HandleNetworkPostForward = (%v, %v), want pass-through of (%v, %v)", gotResp, gotErr, resp, sentinel)
	}

	handled, r, err = h.HandleUpstreamPreForward(ctx, nil, nil, req, false)
	if handled || r != nil || err != nil {
		t.Errorf("HandleUpstreamPreForward = (%v, %v, %v), want (false, nil, nil)", handled, r, err)
	}

	gotResp, gotErr = h.HandleUpstreamPostForward(ctx, nil, nil, req, resp, sentinel, false)
	if gotResp != resp || !errors.Is(gotErr, sentinel) {
		t.Errorf("HandleUpstreamPostForward = (%v, %v), want pass-through of (%v, %v)", gotResp, gotErr, resp, sentinel)
	}
}

func TestHandler_NewJsonRpcErrorExtractor(t *testing.T) {
	t.Parallel()
	h := NewJsonRpcArchitectureHandler()
	ext := h.NewJsonRpcErrorExtractor()
	if ext == nil {
		t.Fatal("NewJsonRpcErrorExtractor returned nil")
	}
	if _, ok := ext.(*JsonRpcErrorExtractor); !ok {
		t.Fatalf("expected *JsonRpcErrorExtractor, got %T", ext)
	}
}

// The handler registers itself for the jsonrpc architecture via init() —
// pipelines resolve it through the registry, never through this package.
func TestHandler_RegistersItself(t *testing.T) {
	t.Parallel()
	reg, err := common.GetArchitectureHandler(common.ArchitectureJsonRpc)
	if err != nil {
		t.Fatalf("jsonrpc architecture not registered: %v", err)
	}
	if _, ok := reg.(*JsonRpcArchitectureHandler); !ok {
		t.Fatalf("registered handler is %T, want *JsonRpcArchitectureHandler", reg)
	}
}
