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
