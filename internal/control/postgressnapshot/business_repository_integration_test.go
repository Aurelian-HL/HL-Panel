package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestPostgreSQLBusinessRestartReplayAndAuthorizationRollback(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	event := audit.Event{ID: "business-event", Action: "business-test", ActorID: admin.ID, CreatedAt: now}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "entry-business", Name: "Business entry", Kind: groups.KindEntry}, event))
	networkInput := groupconfig.UpdateInput{Network: groupconfig.GroupNetwork{GroupID: "entry-business", ConnectHost: "entry.example.test", PortStart: 22000, PortEnd: 22010, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}, IdempotencyKey: "network-configure", RequestSHA256: "network-request", CreatedBy: admin.ID}
	_, _, err = s.UpdateGroupNetwork(ctx, networkInput, event)
	require(err)
	group := customers.UserGroup{ID: "business-user-group", Name: "Business users", AllowedEntryGroupIDs: []string{"entry-business"}, AllowedExitGroupIDs: []string{}, AllowDirect: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	groupInput := customers.SaveUserGroupInput{UserGroup: group, IdempotencyKey: "create-group", RequestSHA256: "group-request", CreatedBy: admin.ID}
	_, _, err = s.SaveUserGroup(ctx, groupInput, event)
	require(err)
	customerService := customers.NewService(s, nil)
	customerInput := customers.CustomerInput{Username: "business-customer", Password: "isolated-customer-password", UserGroupID: group.ID, MaxRules: 2, IdempotencyKey: "create-customer"}
	customer, _, err := customerService.CreateCustomer(ctx, admin.ID, customerInput)
	require(err)
	privateBefore, err := s.Customer(ctx, customer.ID)
	require(err)
	ruleInput := forwarding.CreateInput{Rule: forwarding.Rule{ID: "business-rule", Name: "First forwarding rule", CustomerID: customer.ID, EntryGroupID: "entry-business", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 18080}}, SelectionPolicy: forwarding.SelectionRoundRobin, Revision: 1, CreatedAt: now, UpdatedAt: now}, IdempotencyKey: "create-rule", RequestSHA256: "rule-request", CreatedBy: admin.ID}
	rule, _, err := s.CreateForwardingRule(ctx, ruleInput, event)
	require(err)
	if rule.ListenPort != 22000 || rule.Deployed || rule.Status != forwarding.StatusPendingActivation {
		t.Fatal("wrong allocation or false activation status")
	}
	// Revoking an authorization used by an existing rule must roll back the
	// complete operation, including its audit event and idempotency record.
	beforeAudit, err := s.AuditEvents(ctx)
	require(err)
	revoked := groupInput
	revoked.UserGroup.Revision = 2
	revoked.UserGroup.AllowDirect = false
	revoked.ExpectedRevision = 1
	revoked.IdempotencyKey = "revoke-direct"
	revoked.RequestSHA256 = "revoke-request"
	if _, _, err = s.SaveUserGroup(ctx, revoked, event); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("revoke did not fail: %v", err)
	}
	afterAudit, err := s.AuditEvents(ctx)
	require(err)
	if len(beforeAudit) != len(afterAudit) {
		t.Fatal("failed mutation appended audit")
	}
	require(s.Close())
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	require(err)
	defer reopened.Close()
	privateAfter, err := reopened.Customer(ctx, customer.ID)
	require(err)
	if !bytes.Equal(privateBefore.PasswordHash, privateAfter.PasswordHash) {
		t.Fatal("customer hash lost across restart")
	}
	if _, replayed, err := customers.NewService(reopened, nil).CreateCustomer(ctx, admin.ID, customerInput); err != nil || !replayed {
		t.Fatalf("customer replay lost: %v", err)
	}
	if _, replayed, err := reopened.UpdateGroupNetwork(ctx, networkInput, event); err != nil || !replayed {
		t.Fatalf("network replay lost: %v", err)
	}
	if _, replayed, err := reopened.SaveUserGroup(ctx, groupInput, event); err != nil || !replayed {
		t.Fatalf("user-group replay lost: %v", err)
	}
	restored, replayed, err := reopened.CreateForwardingRule(ctx, ruleInput, event)
	require(err)
	if !replayed || restored.ID != rule.ID || restored.ListenPort != rule.ListenPort {
		t.Fatal("rule replay lost port reservation")
	}
	groupAfter, err := reopened.UserGroup(ctx, group.ID)
	require(err)
	if !groupAfter.AllowDirect || groupAfter.Revision != 1 {
		t.Fatal("rejected authorization mutation persisted")
	}
	finalAudit, err := reopened.AuditEvents(ctx)
	require(err)
	if len(finalAudit) != len(beforeAudit) {
		t.Fatal("replay or restart duplicated audit")
	}
	var snapshotRaw []byte
	require(reopened.db.QueryRowContext(ctx, `SELECT payload FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&snapshotRaw))
	if bytes.Contains(snapshotRaw, []byte(customerInput.Password)) {
		t.Fatal("database snapshot leaked plaintext customer password")
	}
}

func TestPostgreSQLVersionOneBusinessMigration(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	oldRaw, err := memoryrepo.New(admin).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(oldRaw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["Version"] = json.RawMessage("1")
	for _, key := range []string{"Customers", "UserGroups", "GroupNetworks", "ForwardRules", "BusinessIdempotency"} {
		delete(legacy, key)
	}
	oldRaw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET format_version=1,payload=$1 WHERE singleton=true`, oldRaw); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, _, err = customers.NewService(reopened, nil).CreateUserGroup(ctx, admin.ID, customers.UserGroupInput{Name: "Migrated business users", IdempotencyKey: "migrate-first-group"})
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var payload []byte
	if err := reopened.db.QueryRowContext(ctx, `SELECT format_version,payload FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&version, &payload); err != nil {
		t.Fatal(err)
	}
	if version != memoryrepo.SnapshotVersion {
		t.Fatalf("database remained at schema %d", version)
	}
	restored, err := memoryrepo.DecodeSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	userGroups, err := restored.ListUserGroups(ctx)
	if err != nil || len(userGroups) != 1 {
		t.Fatal("migration lost newly saved group")
	}
	storedAdmin, err := restored.AdministratorByUsername(ctx, admin.Username)
	if err != nil || !bytes.Equal(storedAdmin.PasswordHash, admin.PasswordHash) {
		t.Fatal("migration changed existing administrator")
	}
}
