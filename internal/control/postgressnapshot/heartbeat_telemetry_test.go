package postgressnapshot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestTelemetryIsOrderedDetachedAndIdentityScoped(t *testing.T) {
	s := &Store{}
	now := time.Now().UTC()
	sample := nodes.Node{ID: "n", CredentialHash: "identity", LastHeartbeatAt: &now, Resources: map[string]any{"host": map[string]any{"cpu_percent": float64(25)}}}
	s.publishHeartbeatTelemetry(sample)
	sample.Resources["host"].(map[string]any)["cpu_percent"] = float64(99)
	old := now.Add(-time.Second)
	s.publishHeartbeatTelemetry(nodes.Node{ID: "n", CredentialHash: "identity", LastHeartbeatAt: &old})
	items := []nodes.Node{{ID: "n", CredentialHash: "identity"}, {ID: "n", CredentialHash: "rotated"}, {ID: "n", CredentialHash: "identity", DeletedAt: &now}, {ID: "missing"}}
	s.mergeNodeTelemetry(items)
	if items[0].LastHeartbeatAt == nil || !items[0].LastHeartbeatAt.Equal(now) || items[0].Resources["host"].(map[string]any)["cpu_percent"] != float64(25) {
		t.Fatal("latest detached telemetry missing")
	}
	for _, node := range items[1:] {
		if node.LastHeartbeatAt != nil {
			t.Fatal("telemetry crossed identity/deletion boundary")
		}
	}
	items[0].Resources["host"].(map[string]any)["cpu_percent"] = float64(1)
	newer := now.Add(time.Second)
	items = []nodes.Node{{ID: "n", CredentialHash: "identity", LastHeartbeatAt: &newer}}
	s.mergeNodeTelemetry(items)
	if !items[0].LastHeartbeatAt.Equal(newer) {
		t.Fatal("older telemetry overwrote durable data")
	}
	s.telemetry["n"] = heartbeatTelemetry{node: sample, receivedAt: time.Now().Add(-heartbeatTelemetryRetention - time.Second)}
	items = []nodes.Node{{ID: "n", CredentialHash: "identity"}}
	s.mergeNodeTelemetry(items)
	if items[0].LastHeartbeatAt != nil {
		t.Fatal("expired telemetry kept node alive")
	}
}

func TestTelemetryConcurrentPublishAndRead(t *testing.T) {
	s := &Store{}
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := now.Add(time.Duration(i) * time.Second)
			s.publishHeartbeatTelemetry(nodes.Node{ID: "n", CredentialHash: "identity", LastHeartbeatAt: &at, Resources: map[string]any{"value": i}})
			for range 20 {
				s.mergeNodeTelemetry([]nodes.Node{{ID: "n", CredentialHash: "identity"}})
			}
		}(i)
	}
	wg.Wait()
	items := []nodes.Node{{ID: "n", CredentialHash: "identity"}}
	s.mergeNodeTelemetry(items)
	if !items[0].LastHeartbeatAt.Equal(now.Add(31 * time.Second)) {
		t.Fatal("concurrent publication lost latest sample")
	}
}

func TestDurableTelemetryResetsClockAndIdentity(t *testing.T) {
	s := &Store{}
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	s.publishHeartbeatTelemetry(nodes.Node{ID: "n", CredentialHash: "old", LastHeartbeatAt: &future})
	s.publishDurableHeartbeatTelemetry(nodes.Node{ID: "n", CredentialHash: "new", LastHeartbeatAt: &now})
	next := now.Add(time.Second)
	s.publishHeartbeatTelemetry(nodes.Node{ID: "n", CredentialHash: "new", LastHeartbeatAt: &next})
	items := []nodes.Node{{ID: "n", CredentialHash: "new", LastHeartbeatAt: &now}}
	s.mergeNodeTelemetry(items)
	if !items[0].LastHeartbeatAt.Equal(next) {
		t.Fatal("clock correction or identity rotation blocked live telemetry")
	}
}

func TestPostgresProbeSeesEveryHeartbeatWithoutSnapshotWrite(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	s, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	if err = s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "live-token", TokenHash: "live-token-hash", Name: "live", ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "live-token-hash", CredentialHash: "live-identity", Node: nodes.Node{ID: "live-node", Hostname: "live", CreatedAt: now, UpdatedAt: now}}, now, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	heartbeat := nodes.Heartbeat{Hostname: "live", Platform: "linux", Architecture: "amd64", BootID: "boot", Resources: map[string]any{"host": map[string]any{"cpu_percent": float64(1)}}}
	if _, err = s.UpdateHeartbeat(ctx, "live-node", heartbeat, now, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 15; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		heartbeat.Resources["host"].(map[string]any)["cpu_percent"] = float64(i)
		if _, err = s.UpdateHeartbeat(ctx, "live-node", heartbeat, at, audit.Event{}); err != nil {
			t.Fatal(err)
		}
		items, err := s.ListNodes(ctx)
		if err != nil || len(items) != 1 || !items[0].LastHeartbeatAt.Equal(at) || items[0].Resources["host"].(map[string]any)["cpu_percent"] != float64(i) {
			t.Fatalf("sample %d not visible immediately: %v", i, err)
		}
		items[0].Resources["host"].(map[string]any)["cpu_percent"] = float64(999)
	}
	var nextRevision int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&nextRevision); err != nil || nextRevision != revision {
		t.Fatal("telemetry rewrote full snapshot")
	}
	// Any business transaction must see recent liveness, preventing deletion
	// based on a stale persisted heartbeat inside the throttle window.
	if _, _, err = s.DeleteOffline(ctx, nodes.DeleteInput{NodeID: "live-node", DeletedBy: "admin-one", IdempotencyKey: "delete-live", RequestSHA256: "delete-live-hash", DeletedAt: now.Add(20 * time.Second), OnlineFor: 10 * time.Second}, audit.Event{}); err == nil {
		t.Fatal("deleted a currently live node using stale durable heartbeat")
	}
	if _, _, err = s.DeleteOffline(ctx, nodes.DeleteInput{NodeID: "live-node", DeletedBy: "admin-one", IdempotencyKey: "delete-offline", RequestSHA256: "delete-offline-hash", DeletedAt: now.Add(3 * time.Minute), OnlineFor: time.Minute}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListNodes(ctx)
	if err != nil || len(items) != 0 {
		t.Fatal("telemetry resurrected deleted node")
	}
}
