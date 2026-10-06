package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"google.golang.org/grpc"
)

type statsClientStub struct {
	responses []*xraystatsproto.QueryStatsResponse
	err       error
	calls     int
}

func (client *statsClientStub) QueryStats(context.Context, *xraystatsproto.QueryStatsRequest, ...grpc.CallOption) (*xraystatsproto.QueryStatsResponse, error) {
	if client.err != nil {
		return nil, client.err
	}
	index := client.calls
	client.calls++
	if index >= len(client.responses) {
		index = len(client.responses) - 1
	}
	return client.responses[index], nil
}

func statsTagForTest(t *testing.T) string {
	t.Helper()
	metadata := map[string]string{
		"rule_id": "rule-1", "customer_id": "customer-1", "entry_group_id": "entry-1",
		"exit_group_id": "exit-1", "protocol": "tcp",
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return "vless-reality-rule-1--" + base64.RawURLEncoding.EncodeToString(raw)
}

func statsResponse(tag string, uplink, downlink int64) *xraystatsproto.QueryStatsResponse {
	return &xraystatsproto.QueryStatsResponse{Stat: []*xraystatsproto.Stat{
		{Name: "inbound>>>" + tag + ">>>traffic>>>uplink", Value: uplink},
		{Name: "inbound>>>" + tag + ">>>traffic>>>downlink", Value: downlink},
	}}
}

func usageWindow() CollectionWindow {
	start := time.Unix(100, 0).UTC()
	return CollectionWindow{StartedAt: start, EndedAt: start.Add(time.Second)}
}

func TestXrayStatsSourceUsesCumulativeDeltasAndBaseline(t *testing.T) {
	tag := statsTagForTest(t)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(tag, 100, 40), statsResponse(tag, 125, 55),
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil {
		t.Fatal(err)
	} else if len(deltas) != 0 {
		t.Fatalf("first sample = %#v, want baseline only", deltas)
	}
	deltas, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 40 || deltas[0].CustomerActualBytes != 40 {
		t.Fatalf("second sample = %#v, want 40-byte bidirectional delta", deltas)
	}
}

func TestXrayFirstTrafficAfterEmptySampleSurvivesCollectionRollback(t *testing.T) {
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{{}, statsResponse(statsTagForTest(t), 84, 2490569)}}
	source := NewXrayStatsSourceWithClient(client, nil)
	_, _ = source.CollectUsage(context.Background(), usageWindow())
	for attempt := 0; attempt < 2; attempt++ {
		if err := source.BeginCollection(); err != nil {
			t.Fatal(err)
		}
		got, err := source.CollectUsage(context.Background(), usageWindow())
		if err != nil || len(got) != 1 || got[0].RuleActualBytes != 2490653 {
			t.Fatalf("first transfer, attempt %d: %v %v", attempt, got, err)
		}
		if attempt == 0 {
			source.RollbackCollection()
		} else {
			source.CommitCollection()
		}
	}
	if got, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(got) != 0 {
		t.Fatalf("same counters counted twice: %v %v", got, err)
	}
}

func TestXrayPartialSampleDoesNotForgetOtherRuleCounters(t *testing.T) {
	tag := statsTagForTest(t)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(tag, 100, 20),
		{Stat: []*xraystatsproto.Stat{{Name: "outbound>>>other>>>traffic>>>uplink", Value: 50}}},
		statsResponse(tag, 125, 35),
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	_, _ = source.CollectUsage(context.Background(), usageWindow())
	_, _ = source.CollectUsage(context.Background(), usageWindow())
	got, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil || len(got) != 1 || got[0].RuleActualBytes != 40 {
		t.Fatalf("reappearing stats must use preserved cursor: %v %v", got, err)
	}
}

func TestDecodeStatsTagExtractsLegacyRuleID(t *testing.T) {
	metadata, ok := decodeStatsTag("vless-reality-fwd_rule-legacy")
	if !ok {
		t.Fatal("legacy stats tag was rejected")
	}
	if metadata.RuleID != "rule-legacy" || !metadata.Legacy || metadata.CustomerID != "" || metadata.EntryGroup != "" || metadata.ExitGroup != "" || metadata.Protocol != "" {
		t.Fatalf("legacy stats metadata = %#v, want only rule identity and legacy marker", metadata)
	}
}

func TestXrayStatsSourceEmitsLegacyRuleCounterDelta(t *testing.T) {
	tag := "vless-reality-fwd_rule-legacy"
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(tag, 100, 25),
		statsResponse(tag, 145, 40),
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("first legacy sample = %#v, %v; want baseline only", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 {
		t.Fatalf("legacy counter deltas = %#v, want one rule delta", deltas)
	}
	delta := deltas[0]
	if delta.RuleID != "rule-legacy" || delta.LegacyRuleID != "rule-legacy" {
		t.Fatalf("legacy identifiers = rule %q, marker %q", delta.RuleID, delta.LegacyRuleID)
	}
	if delta.CustomerID != "" || delta.EntryGroupID != "" || delta.ExitGroupID != "" || delta.Protocol != "" {
		t.Fatalf("legacy delta unexpectedly contains ownership metadata: %#v", delta)
	}
	if delta.RuleActualBytes != 60 || delta.CustomerActualBytes != 60 || delta.EntryMultiplierMicros != 1_000_000 || delta.ExitMultiplierMicros != 1_000_000 {
		t.Fatalf("legacy byte delta = %#v, want 60 bytes and unit multipliers", delta)
	}
}

func TestXrayStatsSourceTreatsCounterResetAsNewBaseline(t *testing.T) {
	tag := statsTagForTest(t)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(tag, 100, 20), statsResponse(tag, 10, 4), statsResponse(tag, 12, 7),
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	for i := 0; i < 2; i++ {
		if _, err := source.CollectUsage(context.Background(), usageWindow()); err != nil {
			t.Fatal(err)
		}
	}
	deltas, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 5 {
		t.Fatalf("post-reset sample = %#v, want 5-byte delta", deltas)
	}
}

func TestXrayStatsSourcePreservesBaselineAcrossTransientEmptyResponse(t *testing.T) {
	tag := statsTagForTest(t)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		statsResponse(tag, 100, 20),
		{Stat: nil},
		statsResponse(tag, 125, 35),
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("baseline sample = %#v, %v", deltas, err)
	}
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("transient empty sample = %#v, %v", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("post-empty sample = %#v, want 40-byte delta", deltas)
	}
}

func TestXrayStatsSourceFallsBackToOutboundCounters(t *testing.T) {
	tag := statsTagForTest(t)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		{Stat: []*xraystatsproto.Stat{{Name: "outbound>>>" + tag + ">>>traffic>>>uplink", Value: 11}, {Name: "outbound>>>" + tag + ">>>traffic>>>downlink", Value: 5}}},
		{Stat: []*xraystatsproto.Stat{{Name: "outbound>>>" + tag + ">>>traffic>>>uplink", Value: 19}, {Name: "outbound>>>" + tag + ">>>traffic>>>downlink", Value: 9}}},
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	if _, err := source.CollectUsage(context.Background(), usageWindow()); err != nil {
		t.Fatal(err)
	}
	deltas, err := source.CollectUsage(context.Background(), usageWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleActualBytes != 12 {
		t.Fatalf("outbound fallback = %#v, want 12-byte delta", deltas)
	}
}

func TestXrayStatsSourceSkipsInvalidMetadataAndEmptyResponses(t *testing.T) {
	valid := statsTagForTest(t)
	badProtocolRaw, _ := json.Marshal(map[string]string{"rule_id": "r", "customer_id": "c", "entry_group_id": "e", "protocol": "icmp"})
	badProtocol := "vless-reality-r--" + base64.RawURLEncoding.EncodeToString(badProtocolRaw)
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{
		{Stat: nil},
		{Stat: []*xraystatsproto.Stat{
			{Name: "inbound>>>not-a-tag>>>traffic>>>uplink", Value: 8},
			{Name: "inbound>>>" + badProtocol + ">>>traffic>>>uplink", Value: 8},
			{Name: "inbound>>>" + valid + ">>>traffic>>>uplink", Value: 8},
		}},
		{Stat: []*xraystatsproto.Stat{
			{Name: "inbound>>>not-a-tag>>>traffic>>>uplink", Value: 9},
			{Name: "inbound>>>" + badProtocol + ">>>traffic>>>uplink", Value: 9},
			{Name: "inbound>>>" + valid + ">>>traffic>>>uplink", Value: 16},
		}},
	}}
	source := NewXrayStatsSourceWithClient(client, nil)
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("empty baseline = %#v, %v", deltas, err)
	}
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 8 {
		t.Fatalf("first traffic after empty baseline = %#v, %v", deltas, err)
	}
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 8 {
		t.Fatalf("invalid metadata handling = %#v, %v", deltas, err)
	}
}

func TestXrayStatsSourceHandlesNilResponse(t *testing.T) {
	source := NewXrayStatsSourceWithClient(&statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{nil}}, nil)
	if deltas, err := source.CollectUsage(context.Background(), usageWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("nil response = %#v, %v", deltas, err)
	}
}

func TestXrayStatsSourceReturnsQueryErrors(t *testing.T) {
	want := errors.New("stats unavailable")
	source := NewXrayStatsSourceWithClient(&statsClientStub{err: want}, nil)
	if _, err := source.CollectUsage(context.Background(), usageWindow()); err == nil || !errors.Is(err, want) {
		t.Fatalf("CollectUsage() error = %v, want wrapped query error", err)
	}
}

func TestValidateStatsAddressRequiresLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:10085", "[::1]:10085", "localhost:10085"} {
		if err := validateStatsAddress(address); err != nil {
			t.Errorf("validateStatsAddress(%q) = %v", address, err)
		}
	}
	for _, address := range []string{"", "127.0.0.1", "192.0.2.1:10085"} {
		if err := validateStatsAddress(address); err == nil {
			t.Errorf("validateStatsAddress(%q) accepted non-loopback/invalid address", address)
		}
	}
}
