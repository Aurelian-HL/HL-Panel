package memoryrepo

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestIdentityReadViewDetachesSecretsTelemetryAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	commandAt := now.Add(-time.Minute)
	hash := []byte("isolated-password-hash")
	store := New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: hash})
	if err := store.CreateSession(ctx, auth.Session{AdminID: "admin", TokenHash: "session", ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	store.nodes["node"] = nodes.Node{
		ID: "node", CredentialHash: "credential", LastHeartbeatAt: &now, ControlCommandUpdatedAt: &commandAt,
		Capabilities: []string{"gost"}, EngineVersions: map[string]string{"gost": "v3"},
		Resources: map[string]any{"host": map[string]any{"ipv4": "192.0.2.1"}},
	}
	store.nodesByCredential["credential"] = "node"
	view := store.IdentityReadView()
	admin, err := view.AdministratorByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.PasswordHash[0] ^= 1
	node, err := view.NodeByCredentialHash(ctx, "credential")
	if err != nil {
		t.Fatal(err)
	}
	node.Capabilities[0] = "changed"
	node.EngineVersions["gost"] = "changed"
	node.Resources["host"].(map[string]any)["ipv4"] = "changed"
	*node.LastHeartbeatAt = now.Add(time.Minute)
	*node.ControlCommandUpdatedAt = now.Add(time.Minute)
	if err := store.UpdateAdministratorPassword(ctx, "admin", hash, []byte("new-password-hash"), true, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	store.nodes["node"].Resources["host"].(map[string]any)["ipv4"] = "source-changed"
	store.nodesByCredential["credential"] = "other-node"
	unchanged, err := view.AdministratorByID(ctx, "admin")
	if err != nil || !bytes.Equal(unchanged.PasswordHash, hash) || unchanged.MustChangePassword {
		t.Fatal("source or result mutation changed the detached identity")
	}
	node, err = view.NodeByCredentialHash(ctx, "credential")
	if err != nil || node.ID != "node" || node.Capabilities[0] != "gost" || node.EngineVersions["gost"] != "v3" || node.Resources["host"].(map[string]any)["ipv4"] != "192.0.2.1" || !node.LastHeartbeatAt.Equal(now) || !node.ControlCommandUpdatedAt.Equal(commandAt) {
		t.Fatal("node identity or nested telemetry was shared")
	}
	if _, err := view.SessionByTokenHash(ctx, "session", now); err != nil {
		t.Fatal("source mutation changed detached session", err)
	}
	if _, err := view.SessionByTokenHash(ctx, "session", now.Add(time.Hour)); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached view extended session expiry", err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				n, err := view.NodeByCredentialHash(ctx, "credential")
				if err != nil {
					t.Error(err)
					return
				}
				n.Resources["host"].(map[string]any)["ipv4"] = "parallel-result"
				*n.ControlCommandUpdatedAt = now
			}
		}()
	}
	workers.Wait()
}

func TestIdentityReadViewControlBindingsAndOverview(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	s := New(auth.Administrator{ID: "admin", Username: "admin"})
	s.deviceGroups["group"] = groups.DeviceGroup{ID: "group"}
	s.nodes["node"] = nodes.Node{ID: "node", CredentialHash: "current", NezhaServerID: 42,
		LastHeartbeatAt: &old, DesiredGeneration: 2, AppliedGeneration: 1, LastApplyStatus: "failed",
		ControlCommandID: "command", ControlCommand: "restart", ControlCommandStatus: "pending", ControlCommandUpdatedAt: &old}
	s.nodes["deleted"] = nodes.Node{ID: "deleted", NezhaServerID: 99, DeletedAt: &now}
	view := s.IdentityReadView()
	delete(s.nodes, "node")
	delete(s.deviceGroups, "group")
	bindings, err := view.ListNezhaBindings(ctx)
	if err != nil || len(bindings) != 1 || bindings["node"] != 42 {
		t.Fatal("binding projection changed", err)
	}
	bindings["node"] = 7
	again, err := view.ListNezhaBindings(ctx)
	if err != nil || again["node"] != 42 {
		t.Fatal("binding result mutated projection", err)
	}
	for _, id := range []string{"deleted", "missing"} {
		if _, err := view.ControlForNode(ctx, id); !errors.Is(err, faults.ErrNotFound) {
			t.Fatal("control allowed unavailable node", err)
		}
	}
	command, err := view.ControlForNode(ctx, "node")
	if err != nil || command.CommandID != "command" || !command.UpdatedAt.Equal(old) {
		t.Fatal("durable command lost", err)
	}
	wrong := nodes.Node{ID: "node", CredentialHash: "revoked", LastHeartbeatAt: &now}
	overview, err := view.Overview(ctx, now, time.Minute, []nodes.Node{wrong})
	if err != nil || overview.NodeCount != 1 || overview.GroupCount != 1 || overview.OnlineNodeCount != 0 {
		t.Fatal("wrong identity or deleted node affected overview", err)
	}
	sample := nodes.Node{ID: "node", CredentialHash: "current", LastHeartbeatAt: &now,
		AppliedGeneration: 2, LastApplyStatus: "succeeded", ControlCommandID: "forged"}
	overview, err = view.Overview(ctx, now, time.Minute, []nodes.Node{sample})
	if err != nil || overview.OnlineNodeCount != 1 || overview.SyncingNodeCount != 1 || overview.FailedApplyCount != 1 {
		t.Fatal("telemetry replaced durable deployment evidence", err)
	}
	command, err = view.ControlForNode(ctx, "node")
	if err != nil || command.CommandID != "command" {
		t.Fatal("telemetry changed command", err)
	}
	overview, err = view.Overview(ctx, now.Add(2*time.Minute), time.Minute, []nodes.Node{sample})
	if err != nil || overview.OnlineNodeCount != 0 {
		t.Fatal("overview cached online status beyond heartbeat expiry", err)
	}
	overview, err = view.Overview(ctx, now, time.Minute, nil)
	if err != nil || overview.OnlineNodeCount != 0 {
		t.Fatal("request telemetry contaminated immutable projection", err)
	}
}
