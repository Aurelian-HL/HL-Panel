package nezhamonitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

type bindingSource map[string]uint64

func (source bindingSource) ListNezhaBindings(context.Context) (map[string]uint64, error) {
	return source, nil
}

func TestDurableBindingUpdatesInventoryAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":1,"name":"A"},{"id":2,"name":"B"}]}`))
	}))
	defer server.Close()
	svc, err := New(server.URL, testPAT, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	bindings := bindingSource{}
	svc.SetBindingSource(bindings)
	if err := svc.ValidateAvailableServer(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	bindings["node-1"] = 2
	if err := svc.ValidateAvailableServer(context.Background(), 2); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("bound server accepted: %v", err)
	}
	items, err := svc.FetchInventory(context.Background())
	if err != nil || len(items) != 2 || items[1].NodeID != "node-1" || items[1].LinkStatus != "linked" {
		t.Fatalf("durable inventory: %+v, %v", items, err)
	}
	if err := svc.ValidateAvailableServer(context.Background(), 3); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("invisible server accepted: %v", err)
	}
	conflicting, err := New(server.URL, testPAT, map[string]uint64{"static-node": 2}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conflicting.SetBindingSource(bindings)
	if _, err := conflicting.FetchInventory(context.Background()); err == nil {
		t.Fatal("static and durable mappings to the same Nezha server were accepted")
	}
}

const testPAT = "secret-pat-value-123456789"

func TestFetchProjectsOfficialServerFields(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server" || r.Header.Get("Authorization") != "Bearer "+testPAT {
			t.Errorf("unexpected Nezha request path or authorization")
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":12,"name":"node A","created_at":"2026-09-01T12:00:00Z","last_active":"2026-10-04T07:59:58Z","geoip":{"ip":{"ipv4_addr":"198.51.100.4","ipv6_addr":"2001:db8::4"},"country_code":"CN"},"host":{"platform":"debian","platform_version":"13.2","arch":"x86_64","boot_time":1750000000,"version":"v2.3.5","mem_total":4096,"disk_total":8192},"state":{"cpu":12.5,"mem_used":1024,"disk_used":2048,"net_in_speed":11,"net_out_speed":22,"net_in_transfer":33,"net_out_transfer":44,"tcp_conn_count":2104,"udp_conn_count":9,"uptime":55},"note":"must not leak","uuid":"must not leak"}]}`))
	}))
	defer server.Close()
	svc, err := New(server.URL, testPAT, map[string]uint64{"node-1": 12}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return now }
	items, err := svc.Fetch(context.Background(), []string{"node-1", "node-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].LinkStatus != "linked" || items[0].Online == nil || !*items[0].Online || items[0].IPv4 != "198.51.100.4" || items[0].CountryCode != "CN" || *items[0].NetOutTransferBytes != 44 || items[1].LinkStatus != "unlinked" || items[1].Online != nil {
		t.Fatalf("unexpected monitoring projection: %+v", items)
	}
	if items[0].SampledAt == nil || !items[0].SampledAt.Equal(now.Add(-2*time.Second)) {
		t.Fatalf("unexpected sample time: %+v", items[0].SampledAt)
	}
	if items[0].HostPlatform != "debian" || items[0].HostPlatformVersion != "13.2" || items[0].HostArchitecture != "x86_64" || items[0].AgentVersion != "v2.3.5" || items[0].HostBootTime == nil || *items[0].HostBootTime != 1750000000 {
		t.Fatalf("unexpected host metadata: %+v", items[0])
	}
	if items[0].TCPConnCount == nil || *items[0].TCPConnCount != 2104 || items[0].UDPConnCount == nil || *items[0].UDPConnCount != 9 || items[1].TCPConnCount != nil {
		t.Fatalf("unexpected connection counts: %+v", items)
	}
	if items[0].RegisteredAt == nil || !items[0].RegisteredAt.Equal(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected creation time: %+v", items[0].RegisteredAt)
	}
	if items[1].RegisteredAt != nil || items[1].HostBootTime != nil {
		t.Fatalf("unlinked node has host metadata: %+v", items[1])
	}
	encoded, err := json.Marshal(items[0])
	if err != nil || strings.Contains(string(encoded), "must not leak") || !strings.Contains(string(encoded), `"host_boot_time":1750000000`) || !strings.Contains(string(encoded), `"registered_at":"2026-09-01T12:00:00Z"`) || !strings.Contains(string(encoded), `"tcp_conn_count":2104`) {
		t.Fatalf("private Nezha fields were exposed: %s, %v", encoded, err)
	}
}

func TestFetchInventoryIncludesUnmappedServersOnce(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server" || r.Header.Get("Authorization") != "Bearer "+testPAT {
			t.Errorf("unexpected Nezha request")
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":2,"name":"machine B","last_active":"2026-10-04T07:59:58Z","geoip":{"ip":{"ipv4_addr":"198.51.100.2"}},"state":{"cpu":10}},{"id":1,"name":"machine A","last_active":"2026-10-04T07:59:58Z","geoip":{"ip":{"ipv4_addr":"198.51.100.1"}},"state":{"cpu":20}}]}`))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name    string
		mapping map[string]uint64
		status  string
		nodeID  string
	}{
		{"without HL nodes", nil, "unmanaged", "nezha:1"},
		{"mapped HL node", map[string]uint64{"hl-node": 1}, "linked", "hl-node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := New(server.URL, testPAT, tc.mapping, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			svc.now = func() time.Time { return now }
			items, err := svc.FetchInventory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 || items[0].NodeID != tc.nodeID || items[0].LinkStatus != tc.status || *items[0].NezhaServerID != 1 || items[0].Online == nil || !*items[0].Online || items[1].NodeID != "nezha:2" || items[1].IPv4 != "198.51.100.2" {
				t.Fatalf("unexpected inventory: %+v", items)
			}
		})
	}
}

func TestFetchFailsClosedWithoutLeakingPAT(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", http.StatusUnauthorized, testPAT},
		{"application-error", http.StatusOK, `{"success":false,"error":"` + testPAT + `"}`},
		{"malformed", http.StatusOK, `{broken`},
		{"trailing", http.StatusOK, `{"success":true,"data":[]} {}`},
		{"over-limit", http.StatusOK, `{"success":true,"data":[]}` + strings.Repeat(" ", maxResponseBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			svc, err := New(server.URL, testPAT, map[string]uint64{"node-1": 1}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			items, err := svc.Fetch(context.Background(), []string{"node-1"})
			if err == nil || strings.Contains(err.Error(), testPAT) || len(items) != 1 || items[0].LinkStatus != "linked" || items[0].Online != nil {
				t.Fatalf("unexpected failure handling: items=%+v err=%v", items, err)
			}
		})
	}
}

func TestFetchTimeoutAndUnlinkedSkipsUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer server.Close()
	svc, err := New(server.URL, testPAT, map[string]uint64{"node-1": 1}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Fetch(context.Background(), []string{"node-1"}); err == nil || strings.Contains(err.Error(), testPAT) {
		t.Fatalf("timeout must fail without PAT leak: %v", err)
	}
	if items, err := svc.Fetch(context.Background(), []string{"node-2"}); err != nil || items[0].LinkStatus != "unlinked" {
		t.Fatalf("unlinked node should not depend on upstream: %+v %v", items, err)
	}
}

func TestNewRejectsNonLoopbackAndInvalidMapping(t *testing.T) {
	for _, address := range []string{"http://example.com:8008", "http://localhost:8008", "https://127.0.0.1:8008", "http://127.0.0.1:8008/other", "http://127.0.0.1:8008/?x=1"} {
		if _, err := New(address, testPAT, nil, time.Second); err == nil {
			t.Errorf("accepted unsafe URL %q", address)
		}
	}
	if err := ValidateMapping(map[string]uint64{"a": 1, "b": 1}); err == nil {
		t.Fatal("duplicate Nezha server mapping accepted")
	}
}
