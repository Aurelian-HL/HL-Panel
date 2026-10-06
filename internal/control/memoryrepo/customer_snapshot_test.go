package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func TestCustomerSnapshotPreservesPrivateHashesAuthorizationAndReplay(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	ctx := context.Background()
	input := customerSaveInput(item, "snapshot-customer")
	if _, _, err := s.SaveCustomer(ctx, input, audit.Event{Action: "customer.create"}); err != nil {
		t.Fatal(err)
	}
	s.groupNetworks["entry-1"] = groupconfig.GroupNetwork{GroupID: "entry-1", ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 21000, AllowedExitGroupIDs: []string{"exit-1"}, AllowDirect: true, TrafficMultiplier: 1, Revision: 1}
	s.forwardRules["rule-1"] = forwarding.Rule{ID: "rule-1", Name: "Snapshot forwarding rule", CustomerID: item.ID, EntryGroupID: "entry-1", Protocol: forwarding.ProtocolTCP, EgressMode: forwarding.EgressDirect, ListenPort: 20000, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 18080}}, SelectionPolicy: forwarding.SelectionRoundRobin, Paused: true, Revision: 1}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("customer-test-password")) {
		t.Fatal("snapshot stored plaintext password")
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restored.Customer(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persisted.PasswordHash, item.PasswordHash) {
		t.Fatal("snapshot lost private customer hash")
	}
	if restored.userGroups[item.UserGroupID].AllowedEntryGroupIDs[0] != "entry-1" || restored.groupNetworks["entry-1"].PortEnd != 21000 || !restored.forwardRules["rule-1"].Paused {
		t.Fatal("snapshot lost authorization, network or paused state")
	}
	if _, replayed, err := restored.SaveCustomer(ctx, input, audit.Event{Action: "must-not-append"}); err != nil || !replayed {
		t.Fatalf("snapshot lost replay record: %v", err)
	}
	if len(restored.AuditEvents()) != 1 {
		t.Fatal("replay duplicated audit")
	}
	public, err := customers.NewService(restored, nil).ListCustomers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, item.PasswordHash) || bytes.Contains(encoded, []byte("password")) || len(public[0].PasswordHash) != 0 {
		t.Fatal("restored customer API exposed password material")
	}
}

func TestVersionOneSnapshotMigratesIntoWritableBusinessMaps(t *testing.T) {
	s := snapshotFixture(t)
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var previous map[string]json.RawMessage
	if err := json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	previous["Version"] = json.RawMessage("1")
	for _, key := range []string{"Customers", "UserGroups", "GroupNetworks", "ForwardRules", "BusinessIdempotency"} {
		delete(previous, key)
	}
	oldRaw, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(oldRaw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.customers == nil || restored.userGroups == nil || restored.groupNetworks == nil || restored.forwardRules == nil || restored.businessIdempotency == nil {
		t.Fatal("legacy snapshot migration left nil business maps")
	}
	now := time.Now().UTC()
	group := customers.UserGroup{ID: "new-group", Name: "first group after migration", Revision: 1, CreatedAt: now, UpdatedAt: now}
	_, _, err = restored.SaveUserGroup(context.Background(), customers.SaveUserGroupInput{UserGroup: group, IdempotencyKey: "legacy-create", RequestSHA256: "legacy-hash", CreatedBy: "admin"}, audit.Event{})
	if err != nil {
		t.Fatalf("legacy database not writable after migration: %v", err)
	}
	next, err := restored.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var migrated snapshot
	if err := json.Unmarshal(next, &migrated); err != nil || migrated.Version != SnapshotVersion || len(migrated.UserGroups) != 1 {
		t.Fatal("migration was not persisted as current schema")
	}
	if len(migrated.Nodes) != len(s.nodes) || len(migrated.Administrators) != 1 {
		t.Fatal("migration damaged pre-existing state")
	}
}

func TestVersionTwoSnapshotMigratesLegacyDirectPolicy(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	s.customers[item.ID] = item
	s.groupNetworks["entry-1"] = groupconfig.GroupNetwork{GroupID: "entry-1", ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 21000, AllowedExitGroupIDs: []string{"exit-1"}, AllowDirect: true, TrafficMultiplier: 1, Revision: 1}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var previous map[string]json.RawMessage
	if err := json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	previous["Version"] = json.RawMessage("2")
	var networks map[string]map[string]json.RawMessage
	if err := json.Unmarshal(previous["GroupNetworks"], &networks); err != nil {
		t.Fatal(err)
	}
	delete(networks["entry-1"], "direct_policy")
	previous["GroupNetworks"], err = json.Marshal(networks)
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(legacyRaw)
	if err != nil {
		t.Fatal(err)
	}
	network, err := restored.GroupNetwork(context.Background(), "entry-1")
	if err != nil {
		t.Fatal(err)
	}
	if network.DirectPolicy != groupconfig.DirectPolicyOptional || !network.AllowDirect {
		t.Fatalf("legacy allow_direct was not migrated: %+v", network)
	}
	migratedRaw, err := restored.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var migrated snapshot
	if err := json.Unmarshal(migratedRaw, &migrated); err != nil || migrated.Version != SnapshotVersion || migrated.GroupNetworks["entry-1"].DirectPolicy != groupconfig.DirectPolicyOptional {
		t.Fatal("migrated snapshot did not persist the v3 direct policy")
	}
}
