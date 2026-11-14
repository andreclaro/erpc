package erpc

import (
	"encoding/json"
	"testing"

	"github.com/erpc/erpc/clients"
	"github.com/erpc/erpc/indexer/adapters/wsclient"
	"github.com/stretchr/testify/require"
)

func TestSvmDeliveryRewritePreservesPayload(t *testing.T) {
	d := &svmDelivery{
		sub: wsclient.Subscription{ClientSubIDJSON: []byte("0")},
	}
	raw := json.RawMessage(`{"jsonrpc":"2.0","method":"rootNotification","vendorTop":9007199254740993,"params":{"result":18446744073709551615,"subscription":42,"vendorParam":{"keep":true}}}`)
	out, err := d.rewrite(clients.WsSubscriptionNotification{
		SubscriptionID: "42",
		Method:         "rootNotification",
		Params:         json.RawMessage(`{"result":18446744073709551615,"subscription":42,"vendorParam":{"keep":true}}`),
		Raw:            raw,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"jsonrpc":"2.0","method":"rootNotification","vendorTop":9007199254740993,"params":{"result":18446744073709551615,"subscription":0,"vendorParam":{"keep":true}}}`, string(out))
}

func TestSvmDeliveryOfferRequiresSubscriptionID(t *testing.T) {
	d := &svmDelivery{}
	_, err := d.Offer(clients.WsSubscriptionNotification{Raw: json.RawMessage(`{}`)}, false)
	require.Error(t, err)
}
