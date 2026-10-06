package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gostWindow() CollectionWindow {
	start := time.Unix(100, 0).UTC()
	return CollectionWindow{StartedAt: start, EndedAt: start.Add(time.Second)}
}

func gostServer(t *testing.T, bodies ...string) (*httptest.Server, *int) {
	t.Helper()
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/metrics" {
			http.NotFound(response, request)
			return
		}
		index := call
		call++
		if index >= len(bodies) {
			index = len(bodies) - 1
		}
		response.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = response.Write([]byte(bodies[index]))
	}))
	t.Cleanup(server.Close)
	return server, &call
}

func sourceForServer(t *testing.T, server *httptest.Server) *GOSTStatsSource {
	t.Helper()
	address := strings.TrimPrefix(server.URL, "http://")
	return NewGOSTStatsSourceWithClient(address, server.Client())
}

func gostPayload(input, output int) string {
	return fmt.Sprintf("gost_service_transfer_input_bytes_total{service=\"forward-rule-1\",client=\"198.51.100.1\"} %d\n"+
		"gost_service_transfer_input_bytes_total{service=\"forward-rule-1\",client=\"198.51.100.2\"} 0\n"+
		"gost_service_transfer_output_bytes_total{service=\"forward-rule-1\",client=\"198.51.100.1\"} %d\n", input, output)
}

func TestGOSTStatsSourceUsesCumulativeServiceDeltas(t *testing.T) {
	server, _ := gostServer(t, gostPayload(100, 20), gostPayload(125, 35))
	source := sourceForServer(t, server)
	if deltas, err := source.CollectUsage(context.Background(), gostWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("baseline = %#v, %v", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleID != "rule-1" || deltas[0].LegacyRuleID != "rule-1" || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("delta = %#v, want legacy rule-1 40 bytes", deltas)
	}
}

func TestGOSTStatsSourceIgnoresUnmanagedServices(t *testing.T) {
	server, _ := gostServer(t,
		"gost_service_transfer_input_bytes_total{service=\"other-service\"} 900\n"+
			"gost_service_transfer_input_bytes_total{service=\"forward-rule-1\"} 100\n",
		"gost_service_transfer_input_bytes_total{service=\"other-service\"} 1200\n"+
			"gost_service_transfer_input_bytes_total{service=\"forward-rule-1\"} 140\n")
	source := sourceForServer(t, server)
	if deltas, err := source.CollectUsage(context.Background(), gostWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("baseline = %#v, %v", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleID != "rule-1" || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("unmanaged service affected deltas = %#v", deltas)
	}
}

func TestGOSTStatsSourceIgnoresAggregateCountersWithoutService(t *testing.T) {
	server, _ := gostServer(t,
		"gost_service_transfer_input_bytes_total 900\n"+
			"gost_service_transfer_input_bytes_total{service=\"forward-rule-1\"} 100\n",
		"gost_service_transfer_input_bytes_total 1200\n"+
			"gost_service_transfer_input_bytes_total{service=\"forward-rule-1\"} 140\n")
	source := sourceForServer(t, server)
	if deltas, err := source.CollectUsage(context.Background(), gostWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("baseline = %#v, %v", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].RuleID != "rule-1" || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("aggregate counter affected deltas = %#v", deltas)
	}
}

func TestGOSTStatsSourcePreservesBaselineForEmptyAndResets(t *testing.T) {
	server, _ := gostServer(t, gostPayload(100, 0), "# HELP gost_services Current number of services\n# TYPE gost_services gauge\ngost_services 0\n", gostPayload(130, 0), gostPayload(5, 2), gostPayload(9, 3))
	source := sourceForServer(t, server)
	_, _ = source.CollectUsage(context.Background(), gostWindow())
	if deltas, err := source.CollectUsage(context.Background(), gostWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("empty response = %#v, %v", deltas, err)
	}
	deltas, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 30 {
		t.Fatalf("after empty = %#v, %v", deltas, err)
	}
	if deltas, err := source.CollectUsage(context.Background(), gostWindow()); err != nil || len(deltas) != 0 {
		t.Fatalf("reset = %#v, %v", deltas, err)
	}
	deltas, err = source.CollectUsage(context.Background(), gostWindow())
	if err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 5 {
		t.Fatalf("post-reset = %#v, %v", deltas, err)
	}
}

func TestGOSTStatsSourceRollbackRestoresCursor(t *testing.T) {
	server, _ := gostServer(t, gostPayload(100, 0), gostPayload(140, 0), gostPayload(140, 0))
	source := sourceForServer(t, server)
	_, _ = source.CollectUsage(context.Background(), gostWindow())
	if err := source.BeginCollection(); err != nil {
		t.Fatal(err)
	}
	deltas, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("collection = %#v, %v", deltas, err)
	}
	source.RollbackCollection()
	if err := source.BeginCollection(); err != nil {
		t.Fatal(err)
	}
	deltas, err = source.CollectUsage(context.Background(), gostWindow())
	if err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 40 {
		t.Fatalf("retry after rollback = %#v, %v", deltas, err)
	}
	source.CommitCollection()
}

func TestParseGOSTValuePreservesLargeDecimalCounters(t *testing.T) {
	const raw = "9223372036854775807"
	got, err := parseGOSTValue(raw)
	if err != nil || got != int64(^uint64(0)>>1) {
		t.Fatalf("parseGOSTValue(%q) = %d, %v", raw, got, err)
	}
}

func TestParseGOSTValueRejectsRoundedExponentCounters(t *testing.T) {
	if _, err := parseGOSTValue("9007199254740993"); err != nil {
		t.Fatalf("exact large decimal counter rejected: %v", err)
	}
	if got, err := parseGOSTValue("9007199254740993e0"); err != nil || got != 9007199254740993 {
		t.Fatalf("exact large exponent counter = %d, %v", got, err)
	}
	if _, err := parseGOSTValue("9.223372036854776e18"); err == nil {
		t.Fatal("rounded exponent counter was accepted")
	}
	if got, err := parseGOSTValue("1e3"); err != nil || got != 1000 {
		t.Fatalf("integral exponent counter = %d, %v", got, err)
	}
}
