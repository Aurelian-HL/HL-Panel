package gost_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
)

func TestRealGOSTFirstConnectionIsAccountedAfterEmptySample(t *testing.T) {
	binary := verifiedGOST(t)
	payload := strings.Repeat("HL-panel-first-transfer\n", 4096)
	target := markerTarget(t, payload)
	port := unusedLoopbackPort(t)
	rule := forwarding.Rule{ID: "fwd-first", Name: "first connection", CustomerID: "customer-test", EntryGroupID: "entry-test", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: port, Targets: []forwarding.Target{target}, SelectionPolicy: forwarding.SelectionRoundRobin, Status: forwarding.StatusPendingActivation, Revision: 1}
	raw, err := gostconfig.CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var config gostconfig.Configuration
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	metrics := net.JoinHostPort("127.0.0.1", strconv.Itoa(unusedLoopbackPort(t)))
	config.Metrics.Addr = metrics
	raw, _ = json.Marshal(config)
	// Probe the metrics listener only: the rule still has no connection/counter.
	startGOST(t, binary, raw, metrics)
	source, err := usage.NewGOSTStatsSource(metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	window := usage.CollectionWindow{StartedAt: time.Now().Add(-time.Second), EndedAt: time.Now()}
	got, err := source.CollectUsage(context.Background(), window)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty rule baseline: %v %v", got, err)
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	response, err := io.ReadAll(connection)
	_ = connection.Close()
	if err != nil || string(response) != payload {
		t.Fatalf("forwarded payload mismatch: bytes=%d error=%v", len(response), err)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		got, err = source.CollectUsage(context.Background(), window)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			continue
		}
		if len(got) != 1 || got[0].RuleID != rule.ID || got[0].RuleActualBytes != int64(len(payload)) {
			t.Fatalf("first connection accounting: %v", got)
		}
		if repeated, err := source.CollectUsage(context.Background(), window); err != nil || len(repeated) != 0 {
			t.Fatalf("unchanged counters recharged: %v %v", repeated, err)
		}
		t.Logf("first real GOST transfer accounted exactly: %d bytes", len(payload))
		return
	}
	t.Fatal("first connection bytes were dropped")
}
