package svm

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/erpc/erpc/common"
)

func TestDescribeSubscriptionMethod(t *testing.T) {
	tests := []struct {
		method, family, pair, notification string
		commitmentIndex                    int
	}{
		{"accountSubscribe", "account", "accountUnsubscribe", "accountNotification", 1},
		{"programSubscribe", "program", "programUnsubscribe", "programNotification", 1},
		{"logsSubscribe", "logs", "logsUnsubscribe", "logsNotification", 1},
		{"signatureSubscribe", "signature", "signatureUnsubscribe", "signatureNotification", 1},
		{"blockSubscribe", "block", "blockUnsubscribe", "blockNotification", 1},
		{"slotSubscribe", "slot", "slotUnsubscribe", "slotNotification", -1},
		{"slotsUpdatesSubscribe", "slotsUpdates", "slotsUpdatesUnsubscribe", "slotsUpdatesNotification", -1},
		{"rootSubscribe", "root", "rootUnsubscribe", "rootNotification", -1},
		{"voteSubscribe", "vote", "voteUnsubscribe", "voteNotification", -1},
	}
	for _, tt := range tests {
		op, d, ok := DescribeSubscriptionMethod(tt.method)
		if !ok {
			t.Fatalf("%s: expected known", tt.method)
		}
		if op != SubscriptionOperationSubscribe {
			t.Fatalf("%s: expected subscribe op, got %v", tt.method, op)
		}
		if d.Family != tt.family {
			t.Fatalf("%s: family got %q want %q", tt.method, d.Family, tt.family)
		}
		if d.UnsubscribeMethod != tt.pair {
			t.Fatalf("%s: pair got %q want %q", tt.method, d.UnsubscribeMethod, tt.pair)
		}
		if d.NotificationMethod != tt.notification {
			t.Fatalf("%s: notification got %q want %q", tt.method, d.NotificationMethod, tt.notification)
		}
		if d.CommitmentOptionsIndex != tt.commitmentIndex {
			t.Fatalf("%s: commitment index got %d want %d", tt.method, d.CommitmentOptionsIndex, tt.commitmentIndex)
		}
	}

	for _, tt := range tests {
		op, d, ok := DescribeSubscriptionMethod(tt.pair)
		if !ok {
			t.Fatalf("%s: expected known", tt.pair)
		}
		if op != SubscriptionOperationUnsubscribe {
			t.Fatalf("%s: expected unsubscribe op, got %v", tt.pair, op)
		}
		if d.SubscribeMethod != tt.method {
			t.Fatalf("%s: subscribe pair got %q want %q", tt.pair, d.SubscribeMethod, tt.method)
		}
	}

	op, d, ok := DescribeSubscriptionMethod("vendorFeedSubscribe")
	if !ok {
		t.Fatal("expected generic subscribe")
	}
	if op != SubscriptionOperationSubscribe {
		t.Fatalf("expected subscribe op, got %v", op)
	}
	if d.UnsubscribeMethod != "vendorFeedUnsubscribe" {
		t.Fatalf("generic pair got %q", d.UnsubscribeMethod)
	}
	if d.Family != "vendorFeed" {
		t.Fatalf("generic family got %q", d.Family)
	}
	if d.MetricKind != "custom" {
		t.Fatalf("generic kind got %q", d.MetricKind)
	}
	if d.Known {
		t.Fatal("generic method must not be known")
	}

	op, d, ok = DescribeSubscriptionMethod("vendorFeedUnsubscribe")
	if !ok || op != SubscriptionOperationUnsubscribe || d.SubscribeMethod != "vendorFeedSubscribe" {
		t.Fatalf("generic unsubscribe mismatch: op=%v d=%+v", op, d)
	}

	_, _, ok = DescribeSubscriptionMethod("getSlot")
	if ok {
		t.Fatal("expected getSlot not to be a subscription method")
	}
}

func TestParseLocalSubscriptionID(t *testing.T) {
	valid := []string{"0", "9007199254740993", "18446744073709551615"}
	for _, id := range valid {
		jr := common.NewJsonRpcRequest("accountUnsubscribe", nil)
		if err := common.SonicCfg.Unmarshal(
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"accountUnsubscribe","params":[`+id+`]}`), jr,
		); err != nil {
			t.Fatalf("unmarshal %s: %v", id, err)
		}
		got, err := ParseLocalSubscriptionID(jr)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", id, err)
		}
		if strconv.FormatUint(got, 10) != id {
			t.Fatalf("%s: got %d", id, got)
		}
	}

	invalid := []string{`-1`, `1.5`, `"1"`, `null`, `18446744073709551616`}
	for _, id := range invalid {
		jr := common.NewJsonRpcRequest("accountUnsubscribe", nil)
		if err := common.SonicCfg.Unmarshal(
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"accountUnsubscribe","params":[`+id+`]}`), jr,
		); err != nil {
			t.Fatalf("unmarshal %s: %v", id, err)
		}
		_, err := ParseLocalSubscriptionID(jr)
		if err == nil {
			t.Fatalf("%s: expected error", id)
		}
	}
}

func TestInjectSubscriptionCommitment(t *testing.T) {
	net := &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureSvm,
		Svm:          &common.SvmNetworkConfig{Cluster: "mainnet-beta", Commitment: "confirmed"},
	}}

	cases := []struct {
		name   string
		method string
		params []byte
		want   string
	}{
		{
			name:   "omitted commitment appends",
			method: "accountSubscribe",
			params: []byte(`["11111111111111111111111111111111"]`),
			want:   `"confirmed"`,
		},
		{
			name:   "explicit commitment preserved",
			method: "accountSubscribe",
			params: []byte(`["11111111111111111111111111111111",{"commitment":"processed"}]`),
			want:   `"processed"`,
		},
		{
			name:   "block processed clamped to confirmed",
			method: "blockSubscribe",
			params: []byte(`["all"]`),
			want:   `"confirmed"`,
		},
		{
			name:   "slot no injection",
			method: "slotSubscribe",
			params: []byte(`[]`),
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":` + string(tc.params) + `}`)
			nq := common.NewNormalizedRequest(body)
			_, d, _ := DescribeSubscriptionMethod(tc.method)
			if err := InjectSubscriptionCommitment(net, nq, d); err != nil {
				t.Fatalf("inject: %v", err)
			}
			jr, err := nq.JsonRpcRequest(context.Background())
			if err != nil {
				t.Fatalf("jsonrpc request: %v", err)
			}
			out, _ := common.SonicCfg.Marshal(jr.Params)
			got := string(out)
			if tc.want == "" {
				if got != string(tc.params) {
					t.Fatalf("want no mutation, got %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want substring %s", got, tc.want)
			}
		})
	}
}

func TestIsTerminalSignatureNotification(t *testing.T) {
	_, d, _ := DescribeSubscriptionMethod("signatureSubscribe")
	if !IsTerminalSignatureNotification(d, []byte(`{"result":{"value":{"err":null}}}`)) {
		t.Fatal("expected terminal object with err")
	}
	if IsTerminalSignatureNotification(d, []byte(`{"result":{"value":"receivedSignature"}}`)) {
		t.Fatal("receivedSignature must not be terminal")
	}
}


