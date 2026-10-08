package memoryrepo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestNodeDeletionRetiresCandidateProjectionsAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	passwordHash, err := auth.HashPassword("isolated-test")
	if err != nil {
		t.Fatal(err)
	}
	s := New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: passwordHash, CreatedAt: now})
	node := nodes.Node{ID: "node-1", CredentialHash: "private-hash", CreatedAt: now, UpdatedAt: now}
	s.nodes[node.ID], s.nodesByCredential[node.CredentialHash] = node, node.ID
	group := groups.DeviceGroup{ID: "group-1", Name: "test", Kind: groups.KindEdge, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDeviceGroup(ctx, group, projectionAudit("create", now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: group.ID, NodeID: node.ID, Weight: 100, CreatedAt: now, UpdatedAt: now}, projectionAudit("join", now)); err != nil {
		t.Fatal(err)
	}
	pool := createProjectedPool(t, s, "pool-1", "edge.example.test", group.ID, "pool-key", now)
	previousConfigs := len(s.nodeConfigsByNode[node.ID])
	result, replay, err := s.DeleteOffline(ctx, nodes.DeleteInput{NodeID: node.ID, DeletedBy: "admin-1", IdempotencyKey: "delete", RequestSHA256: "delete-hash", DeletedAt: now, OnlineFor: time.Minute}, projectionAudit("delete", now))
	if err != nil || replay || result.NodeID != node.ID {
		t.Fatalf("delete failed: %v", err)
	}
	candidates, err := s.EndpointPoolMembers(ctx, pool.ID)
	if err != nil || len(candidates) != 0 || len(s.protocolHealth[pool.ID]) != 0 {
		t.Fatal("candidate projections survived deletion")
	}
	if len(s.nodeConfigsByNode[node.ID]) != previousConfigs {
		t.Fatal("historical configuration lost")
	}
	if _, _, err := s.UpsertGroupMember(ctx, groups.Member{GroupID: group.ID, NodeID: node.ID, Weight: 100, CreatedAt: now, UpdatedAt: now}, projectionAudit("rejoin", now)); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted node rejoined without enrollment")
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.nodes[node.ID].DeletedAt == nil || len(restored.nodeConfigsByNode[node.ID]) != previousConfigs {
		t.Fatal("tombstone/history not durable")
	}
}

func TestConcurrentHeartbeatAndDeleteCannotRemoveOnlineNodeOrReviveDeletion(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		s := New(auth.Administrator{ID: "admin", Username: "admin"})
		s.nodes["node"] = nodes.Node{ID: "node", CreatedAt: now, UpdatedAt: now}
		var deleteErr, heartbeatErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, deleteErr = s.DeleteOffline(ctx, nodes.DeleteInput{NodeID: "node", DeletedBy: "admin", IdempotencyKey: "delete", RequestSHA256: "hash", DeletedAt: now, OnlineFor: time.Minute}, audit.Event{})
		}()
		go func() {
			defer wg.Done()
			_, heartbeatErr = s.UpdateHeartbeat(ctx, "node", nodes.Heartbeat{}, now, audit.Event{})
		}()
		wg.Wait()
		node := s.nodes["node"]
		if node.DeletedAt != nil {
			if deleteErr != nil || !errors.Is(heartbeatErr, faults.ErrUnauthorized) {
				t.Fatal("heartbeat revived deleted node")
			}
		} else if heartbeatErr != nil || !errors.Is(deleteErr, faults.ErrConflict) || node.LastHeartbeatAt == nil {
			t.Fatal("online node was deleted in race")
		}
	}
}
