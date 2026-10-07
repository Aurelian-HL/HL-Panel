package nezhamonitor

import (
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"strings"
	"testing"
	"time"
)

func TestNativeMetricsPersistedProjectionAndOffline(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-91 * time.Second)
	used, total := uint64(100), uint64(200)
	views := []nodes.View{{Node: nodes.Node{ID: "native", Name: "node", CredentialHash: "never-show", LastHeartbeatAt: &now,
		Resources: map[string]any{"host": &agentv1.HostSnapshot{MemoryUsedBytes: &used, MemoryTotalBytes: &total}}}},
		{Node: nodes.Node{ID: "offline", LastHeartbeatAt: &old}}}
	data, _ := json.Marshal(views)
	if err := json.Unmarshal(data, &views); err != nil {
		t.Fatal(err)
	}
	items := MergeNative([]Item{{NodeID: "native", LinkStatus: "linked"}}, views, now)
	if len(items) != 2 || items[0].Source != "hl" || !*items[0].Online || *items[0].MemoryUsedBytes != used || items[0].CPUPercent != nil || *items[1].Online {
		t.Fatalf("wrong native projection: %+v", items)
	}
	encoded, _ := json.Marshal(items)
	if strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "never-show") {
		t.Fatal("credential leaked")
	}
}

func TestNativeAddressFallbackAndGeographyPreservation(t *testing.T) {
	for _, test := range []struct {
		name, reported, observed, dial, upstreamIP, wantIP, wantCountry string
	}{
		{"reported", "8.8.8.8", "1.1.1.1", "9.9.9.9", "8.8.8.8", "8.8.8.8", "US"},
		{"observed", "", "1.1.1.1", "9.9.9.9", "8.8.8.8", "1.1.1.1", ""},
		{"registered", "", "", "9.9.9.9", "9.9.9.9", "9.9.9.9", "US"},
		{"private", "10.1.2.3", "1.1.1.1", "node.example.test", "8.8.8.8", "1.1.1.1", ""},
		{"no public address", "10.1.2.3", "127.0.0.1", "node.example.test", "8.8.8.8", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := MergeNative([]Item{{NodeID: "node", LinkStatus: "linked", IPv4: test.upstreamIP, CountryCode: "US"}}, []nodes.View{{Node: nodes.Node{
				ID: "node", DialHost: test.dial, Resources: map[string]any{"host": &agentv1.HostSnapshot{IPv4: test.reported}, "observed_ip": test.observed},
			}}}, time.Now())
			if items[0].IPv4 != test.wantIP || items[0].CountryCode != test.wantCountry {
				t.Fatalf("wrong address/geography: %+v", items[0])
			}
		})
	}
	items := MergeNative(nil, []nodes.View{{Node: nodes.Node{ID: "v6", DialHost: "2606:4700:4700::1111"}}}, time.Now())
	if items[0].IPv4 != "" || items[0].IPv6 != "2606:4700:4700::1111" {
		t.Fatal("IPv6 fallback was lost or misclassified")
	}
}
