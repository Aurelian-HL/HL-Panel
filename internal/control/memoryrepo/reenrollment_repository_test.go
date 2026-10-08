package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestReenrollmentAtomicallyReplacesBundleAndEndpointProjections(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	for _, id := range []string{"group-1", "group-2"} {
		g := groups.DeviceGroup{ID: id, Name: id, Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateDeviceGroup(ctx, g, projectionAudit("create-"+id, now)); err != nil {
			t.Fatal(err)
		}
		store.revisionsByGroup[id] = []generations.GroupRevision{{ID: "revision-" + id, GroupID: id, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`), CreatedAt: now}}
	}
	service := enrollment.NewService(store, func() time.Time { return now }, time.Hour)
	first, err := service.IssueForGroup(ctx, "admin", "node", "group-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.Enroll(ctx, enrollment.EnrollInput{RawToken: first.Token, Hostname: "node.test", DialHost: "192.0.2.10", Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.42"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.NodeByCredentialHash(ctx, securetoken.Hash(identity.NodeCredential))
	if err != nil {
		t.Fatal(err)
	}
	oldPool := createProjectedPool(t, store, "pool-1", "entry-1.test", "group-1", "pool-1", now)
	newPool := createProjectedPool(t, store, "pool-2", "entry-2.test", "group-2", "pool-2", now)
	next, err := service.IssueForGroup(ctx, "admin", "renamed", "group-2", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input := enrollment.EnrollInput{RawToken: next.Token, Hostname: "node.test", DialHost: "192.0.2.11", Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.42"}
	// Inject a corrupted persisted revision to verify complete transaction rollback.
	store.revisionsByGroup["group-2"][0].Config = json.RawMessage(`{broken`)
	membersBefore := store.membersByGroup["group-1"][node.ID]
	auditsBefore := len(store.AuditEvents())
	if _, err := service.Reenroll(ctx, node, input); err == nil {
		t.Fatal("invalid bundle was accepted")
	}
	if store.membersByGroup["group-1"][node.ID] != membersBefore || len(store.membersByGroup["group-2"]) != 0 || store.tokensByHash[securetoken.Hash(next.Token)].UsedAt != nil || len(store.AuditEvents()) != auditsBefore {
		t.Fatal("failed compile changed membership, token or audit")
	}
	persisted, _ := store.NodeByCredentialHash(ctx, node.CredentialHash)
	if persisted.DesiredGeneration != node.DesiredGeneration || persisted.Name != node.Name {
		t.Fatal("failed compile changed node")
	}
	oldCandidates, _ := store.EndpointPoolMembers(ctx, oldPool.ID)
	newCandidates, _ := store.EndpointPoolMembers(ctx, newPool.ID)
	if len(oldCandidates) != 1 || len(newCandidates) != 0 {
		t.Fatal("failed compile changed projection")
	}
	store.revisionsByGroup["group-2"][0].Config = json.RawMessage(`{"services":[]}`)
	// Recheck credentials inside the mutation lock, not just during HTTP auth.
	stale := node
	stale.CredentialHash = "rotated-away"
	if _, err := service.Reenroll(ctx, stale, input); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("stale private identity was accepted")
	}
	// Preserve a heartbeat which arrived after request authentication.
	latest := store.nodes[node.ID]
	latest.Resources = map[string]any{"sentinel": "current-heartbeat"}
	store.nodes[node.ID] = latest
	moved, err := service.Reenroll(ctx, node, input)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != node.ID || moved.CredentialHash != node.CredentialHash || moved.Resources["sentinel"] != "current-heartbeat" || moved.Name != "renamed" {
		t.Fatal("identity or current telemetry was lost")
	}
	oldCandidates, _ = store.EndpointPoolMembers(ctx, oldPool.ID)
	newCandidates, _ = store.EndpointPoolMembers(ctx, newPool.ID)
	if len(oldCandidates) != 0 || len(newCandidates) != 1 {
		t.Fatal("latest membership not projected")
	}
	configuration, _, err := store.DesiredNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(configuration.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Fragments) != 1 || bundle.Fragments[0].GroupID != "group-2" {
		t.Fatal("old group remained in desired bundle")
	}
	before, _ := store.EncodeSnapshot()
	if _, err := service.Reenroll(ctx, moved, input); err != nil {
		t.Fatal(err)
	}
	after, _ := store.EncodeSnapshot()
	if !bytes.Equal(before, after) {
		t.Fatal("replay changed durable state")
	}
}
