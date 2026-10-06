package xraystats

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"google.golang.org/grpc"
)

// Embedding the generated client keeps this test stub focused on QueryStats;
// CollectUsage never calls the other StatsService methods.
type statsClientStub struct {
	xraystatsproto.StatsServiceClient
	responses []*xraystatsproto.QueryStatsResponse
	errors    []error
	calls     int
}

func (client *statsClientStub) QueryStats(context.Context, *xraystatsproto.QueryStatsRequest, ...grpc.CallOption) (*xraystatsproto.QueryStatsResponse, error) {
	index := client.calls
	client.calls++
	if index < len(client.errors) && client.errors[index] != nil {
		return nil, client.errors[index]
	}
	if len(client.responses) == 0 {
		return nil, nil
	}
	if index >= len(client.responses) {
		index = len(client.responses) - 1
	}
	return client.responses[index], nil
}

func statsTag(t *testing.T, rule string) string {
	t.Helper()
	metadata, err := json.Marshal(map[string]string{
		"rule_id": rule, "customer_id": "customer-1", "entry_group_id": "entry-1",
		"exit_group_id": "exit-1", "protocol": "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tagPrefix + rule + "--" + base64.RawURLEncoding.EncodeToString(metadata)
}

func statsResponse(tag string, uplink, downlink int64) *xraystatsproto.QueryStatsResponse {
	return &xraystatsproto.QueryStatsResponse{Stat: []*xraystatsproto.Stat{
		{Name: "inbound>>>" + tag + ">>>traffic>>>uplink", Value: uplink},
		{Name: "inbound>>>" + tag + ">>>traffic>>>downlink", Value: downlink},
	}}
}

func statsWindow() usage.CollectionWindow {
	start := time.Unix(100, 0).UTC()
	return usage.CollectionWindow{StartedAt: start, EndedAt: start.Add(time.Second)}
}

func TestCollectUsageDoesNotAdvanceCursorWhenTagMetadataIsInvalid(t *testing.T) {
	valid := statsTag(t, "rule-1")
	invalid := tagPrefix + "malformed"
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(valid, 0, 0),
		{Stat: []*xraystatsproto.Stat{
			{Name: "inbound>>>" + valid + ">>>traffic>>>uplink", Value: 125},
			{Name: "inbound>>>" + invalid + ">>>traffic>>>uplink", Value: 7},
		}},
		statsResponse(valid, 140, 0),
	}}
	source := newWithClient(nil, client)
	if deltas, err := source.CollectUsage(context.Background(), statsWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("baseline = %#v, %v; want no delta", deltas, err)
	}
	if _, err := source.CollectUsage(context.Background(), statsWindow()); err == nil || !strings.Contains(err.Error(), "decode xray inbound tag") {
		t.Fatalf("invalid tag error = %v; want metadata decode failure", err)
	}
	deltas, err := source.CollectUsage(context.Background(), statsWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 140 {
		t.Fatalf("retry delta = %#v; want 140 bytes from the last successful baseline", deltas)
	}
}

func TestCollectUsageQueryFailureLeavesCursorForRetry(t *testing.T) {
	valid := statsTag(t, "rule-1")
	want := errors.New("stats temporarily unavailable")
	client := &statsClientStub{
		responses: []*xraystatsproto.QueryStatsResponse{statsResponse(valid, 100, 0), statsResponse(valid, 140, 0)},
		errors:    []error{nil, want, nil},
	}
	source := newWithClient(nil, client)
	if _, err := source.CollectUsage(context.Background(), statsWindow()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CollectUsage(context.Background(), statsWindow()); err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("query failure = %v; want wrapped temporary error", err)
	}
	deltas, err := source.CollectUsage(context.Background(), statsWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("retry delta = %#v; want 40 bytes", deltas)
	}
}
