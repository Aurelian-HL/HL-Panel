package postgressnapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// The padding models snapshot storage pressure, not a production engine bundle.
func desiredReadFixture(t testing.TB, padded bool, node nodes.Node, config generations.NodeConfigGeneration) (*memoryrepo.Store, []byte) {
	t.Helper()
	_, base := usageSubjectFixture(t, padded)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	put := func(key string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		doc[key] = raw
	}
	nodeRaw, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	put("Nodes", map[string]struct {
		Node           json.RawMessage
		CredentialHash string
	}{node.ID: {Node: nodeRaw, CredentialHash: "synthetic-desired-credential"}})
	put("NodesByCredential", map[string]string{"synthetic-desired-credential": node.ID})
	put("NodeConfigsByNode", map[string]map[int64]generations.NodeConfigGeneration{node.ID: {config.Generation: config}})
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	state, err := memoryrepo.DecodeSnapshotAtVersion(raw, memoryrepo.SnapshotVersion)
	if err != nil {
		t.Fatal(err)
	}
	return state, raw
}

func desiredReadInputs() (nodes.Node, generations.NodeConfigGeneration) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	config := []byte(`{"services":[]}`)
	return nodes.Node{ID: "desired-node", DesiredGeneration: 2, AppliedGeneration: 1, CreatedAt: now, UpdatedAt: now},
		generations.NodeConfigGeneration{ID: "desired-config", NodeID: "desired-node", Generation: 2,
			Engine: agentv1.EngineGOST, Config: config, ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256(config)), CreatedAt: now}
}

func TestDesiredReadProjectionTracksAuthoritativeState(t *testing.T) {
	ctx, dsn := context.Background(), isolatedDatabaseURL(t)
	writer, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	node, config := desiredReadInputs()
	_, raw := desiredReadFixture(t, false, node, config)
	replace := func(payload []byte, bump bool) {
		t.Helper()
		query := `UPDATE nyvp_control_snapshots SET payload=$1`
		if bump {
			query += `,revision=revision+1`
		}
		if _, err := writer.db.ExecContext(ctx, query, payload); err != nil {
			t.Fatal(err)
		}
	}
	replace(raw, true)
	got, applied, err := reader.DesiredNodeConfig(ctx, node.ID)
	if err != nil || applied != 1 || got.Generation != 2 || !bytes.Equal(got.Config, config.Config) {
		t.Fatal("desired configuration mismatch", err)
	}
	cached := reader.readCache.forwarding
	if cached == nil {
		t.Fatal("desired read did not populate detached projection")
	}
	// Returned confidential config bytes must never alias the cached view.
	got.Config[0] = '!'
	got, _, err = reader.DesiredNodeConfig(ctx, node.ID)
	if err != nil || !bytes.Equal(got.Config, config.Config) || reader.readCache.forwarding != cached {
		t.Fatal("warm read reused mutable bytes or decoded snapshot", err)
	}
	if _, applied, err := reader.DesiredNodeConfig(ctx, "unknown-node"); !errors.Is(err, faults.ErrNotFound) || applied != 0 {
		t.Fatal("unknown node accepted", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*nodes.Node, *generations.NodeConfigGeneration)
		bump bool
	}{
		{"other_instance_new_generation", func(n *nodes.Node, c *generations.NodeConfigGeneration) { n.DesiredGeneration = 3; c.Generation = 3 }, true},
		{"same_revision_payload_repair", func(_ *nodes.Node, c *generations.NodeConfigGeneration) {
			c.Config = []byte(`{"services":[],"chains":[]}`)
			c.ConfigSHA256 = fmt.Sprintf("%x", sha256.Sum256(c.Config))
		}, false},
		{"same_revision_applied_receipt", func(n *nodes.Node, _ *generations.NodeConfigGeneration) { n.AppliedGeneration = n.DesiredGeneration }, false},
		{"deleted_node", func(n *nodes.Node, _ *generations.NodeConfigGeneration) {
			at := n.UpdatedAt.Add(time.Hour)
			n.DeletedAt = &at
		}, true},
		{"no_desired_generation", func(n *nodes.Node, _ *generations.NodeConfigGeneration) { n.DesiredGeneration = 0 }, true},
		{"missing_desired_payload", func(n *nodes.Node, _ *generations.NodeConfigGeneration) { n.DesiredGeneration = 9 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, c := desiredReadInputs()
			tc.edit(&n, &c)
			baseline, payload := desiredReadFixture(t, false, n, c)
			replace(payload, tc.bump)
			want, wantApplied, wantErr := baseline.DesiredNodeConfig(ctx, n.ID)
			got, gotApplied, gotErr := reader.DesiredNodeConfig(ctx, n.ID)
			if !errors.Is(gotErr, wantErr) || gotApplied != wantApplied || got.Generation != want.Generation ||
				got.ConfigSHA256 != want.ConfigSHA256 || !bytes.Equal(got.Config, want.Config) {
				t.Fatal("projection differs from authoritative memory semantics", gotErr)
			}
		})
	}
	replace(raw, true)
	if _, _, err := reader.DesiredNodeConfig(ctx, node.ID); err != nil {
		t.Fatal("restoration not observed", err)
	}
	// Ephemeral host telemetry cannot forge an applied-generation acknowledgement.
	at := node.UpdatedAt.Add(time.Minute)
	reader.publishHeartbeatTelemetry(nodes.Node{ID: node.ID, CredentialHash: "synthetic-desired-credential", LastHeartbeatAt: &at, AppliedGeneration: 100})
	if got, applied, err := reader.DesiredNodeConfig(ctx, node.ID); err != nil || applied != 1 || got.Generation != 2 {
		t.Fatal("host telemetry changed durable generation", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := reader.DesiredNodeConfig(canceled, node.ID); err == nil {
		t.Fatal("canceled request served private cached config")
	}
	replace([]byte(`{"Version":24}`), false)
	if _, _, err := reader.DesiredNodeConfig(ctx, node.ID); err == nil || errors.Is(err, faults.ErrNotFound) {
		t.Fatal("corrupt database served cached configuration", err)
	}
	reader.Close()
	if _, _, err := reader.DesiredNodeConfig(ctx, node.ID); err == nil {
		t.Fatal("unavailable database served cached configuration")
	}
}

func TestDesiredReadCostOnIsolatedPostgres(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, config := desiredReadInputs()
	_, raw := desiredReadFixture(t, true, node, config)
	if _, err := store.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.DesiredNodeConfig(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	measure := func(name string, operation func() error) {
		t.Helper()
		started := time.Now()
		for i := 0; i < 25; i++ {
			if err := operation(); err != nil {
				t.Fatal(name, err)
			}
		}
		t.Logf("%s iterations=25 mean=%s snapshot_bytes=%d", name, time.Since(started)/25, len(raw))
	}
	measure("previous_desired_full_snapshot", func() error {
		_, err := transact(ctx, store, false, func(s *memoryrepo.Store) (struct{}, error) {
			_, _, err := s.DesiredNodeConfig(ctx, node.ID)
			return struct{}{}, err
		})
		return err
	})
	measure("validated_desired_projection", func() error {
		_, _, err := store.DesiredNodeConfig(ctx, node.ID)
		return err
	})
}
