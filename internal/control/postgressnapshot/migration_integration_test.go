package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestPostgreSQLMigrationFullRestoreRecoveryRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	source, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	require(err)
	defer source.Close()
	targetDSN := isolatedDatabaseURL(t)
	targetAdmin := bootstrapAdministrator(t)
	targetAdmin.ID = "target-admin"
	targetAdmin.Username = "target"
	target, err := Open(ctx, targetDSN, targetAdmin)
	require(err)
	defer target.Close()
	for _, s := range []*Store{source, target} {
		runner, e := usagemigration.New(s.db)
		require(e)
		_, e = runner.Apply(ctx)
		require(e)
	}
	event := audit.Event{ID: "migration-fixture", ActorType: "administrator", ActorID: "admin-one", Action: "test", Outcome: "succeeded", CreatedAt: now}
	credentialHash := securetoken.Hash("isolated-node-credential")
	require(source.CreateEnrollmentToken(ctx, enrollment.Token{ID: "enroll-one", Name: "migration-node", TokenHash: securetoken.Hash("isolated-enrollment"), ExpiresAt: now.Add(time.Hour)}, event))
	_, err = source.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: securetoken.Hash("isolated-enrollment"), CredentialHash: credentialHash, Node: nodes.Node{ID: "node-one", Hostname: "loopback", CreatedAt: now, UpdatedAt: now}}, now, event)
	require(err)
	_, err = source.UpdateHeartbeat(ctx, "node-one", nodes.Heartbeat{Resources: map[string]any{"memory_bytes": 1024.0}}, now, event)
	require(err)
	require(source.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "group-one", Name: "migration-group", Kind: groups.KindEdge, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now}, event))
	_, _, err = source.UpsertGroupMember(ctx, groups.Member{GroupID: "group-one", NodeID: "node-one", Weight: 1, CreatedAt: now, UpdatedAt: now}, event)
	require(err)
	rule, _, err := forwarding.NewService(source, nil).CreateForAdministrator(ctx, "admin-one", forwarding.Request{Name: "迁移测试规则", EntryGroupID: "group-one", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: 12000, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 19001}}, SelectionPolicy: forwarding.SelectionRoundRobin, TrafficLimitBytes: 1000000}, "migration-rule-fixture")
	require(err)
	_, err = source.CreateGroupRevision(ctx, generations.CreateGroupRevisionInput{ID: "revision-one", GroupID: "group-one", Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`), ConfigSHA256: "fixture-config", RequestSHA256: "fixture-request", IdempotencyKey: "fixture-generation", CreatedBy: "admin-one", CreatedAt: now}, event)
	require(err)
	customer, _, err := customers.NewService(source, nil).CreateCustomer(ctx, "admin-one", customers.CustomerInput{Username: "migration-customer", Password: "isolated-customer", IdempotencyKey: "migration-customer"})
	require(err)
	subService := subscriptions.NewService(source, nil, nil)
	item, _, err := subService.Mutate(ctx, "admin-one", "", "create", subscriptions.Request{Name: "中文规则", CustomerID: customer.ID, Lines: []subscriptions.Line{{Name: "中文规则", URI: "socks5://user:isolated@127.0.0.1:1080"}}}, "migration-sub-create")
	require(err)
	item, _, err = subService.Mutate(ctx, "admin-one", item.ID, "publish", subscriptions.Request{Revision: item.Revision}, "migration-sub-publish")
	require(err)
	subscription, err := subService.Detail(ctx, "admin-one", item.ID)
	require(err)
	oldSession, err := auth.NewService(source, audit.NewService(source), time.Now, time.Hour).Login(ctx, "admin", "isolated-test-password")
	require(err)
	for _, query := range []string{
		`INSERT INTO usage_ingest_cursors VALUES('node-one','boot-one',1,now())`,
		`INSERT INTO usage_events VALUES('node-one','boot-one',1,repeat('a',64),'customer-fixture','rule-fixture','group-one','','tcp',now(),now()-interval '1 second',now(),123,123,246,2000000,1000000,now())`,
		`INSERT INTO usage_customer_totals VALUES('customer-fixture',123,246,now(),now())`,
		`INSERT INTO usage_enforcement_decisions(id,customer_id,rule_id,protocol,reason,action,status,trigger_node_id,trigger_boot_id,trigger_sequence,customer_charged_bytes,traffic_limit_bytes,created_at,updated_at) VALUES('decision-one','customer-fixture','rule-fixture','tcp','quota_exhausted','disable_customer_access','applied','node-one','boot-one',1,246,200,now(),now())`,
		`INSERT INTO usage_enforcement_results VALUES('decision-one',1,'node-one','disable_customer_access','succeeded',repeat('b',64),'isolated',now())`,
	} {
		_, err = source.db.ExecContext(ctx, query)
		require(err)
	}
	runtime := migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{7}, 32), GatewayPoolTokens: map[string]string{"fixture-pool": strings.Repeat("g", 32)}}
	backupPass := "isolated-backup-password"
	service := func(s *Store) *migrationbackup.Service {
		directory := t.TempDir()
		require(os.Chmod(directory, 0700))
		return migrationbackup.New(s, auth.NewService(s, audit.NewService(s), time.Now, time.Hour), audit.NewService(s), "v0.1.47", directory, func() migrationbackup.RuntimeSecrets { return runtime }, nil)
	}
	exporter := service(source)
	importer := service(target)
	raw, err := exporter.Export(ctx, "admin-one", "isolated-test-password", backupPass, "https://source.example.com")
	require(err)
	before, err := target.ExportMigration(ctx)
	require(err)
	preview, err := importer.Preview(ctx, "target-admin", "isolated-test-password", backupPass, raw)
	require(err)
	if preview.Counts["nodes"] != 1 || preview.Counts["rules"] != 1 || preview.Counts["subscriptions"] != 1 || preview.Counts["usage_events"] != 1 {
		t.Fatal("preview incomplete")
	}
	afterPreview, err := target.ExportMigration(ctx)
	require(err)
	if !reflect.DeepEqual(before, afterPreview) {
		t.Fatal("preview mutated target")
	}
	result, err := importer.Restore(ctx, "target-admin", "isolated-test-password", backupPass, "restore-fixture", preview.Digest, "https://target.example.com", raw)
	require(err)
	if result.Replayed || result.RecoveryID == "" {
		t.Fatal("missing restore receipt")
	}
	restored, err := target.ExportMigration(ctx)
	require(err)
	sourceState, err := source.ExportMigration(ctx)
	require(err)
	if !reflect.DeepEqual(sourceState.Usage, restored.Usage) {
		t.Fatal("ledger changed")
	}
	var a, b map[string]json.RawMessage
	require(json.Unmarshal(sourceState.Snapshot, &a))
	require(json.Unmarshal(restored.Snapshot, &b))
	for key := range a {
		if key != "Sessions" && key != "CustomerSessions" && key != "Nodes" && key != "ProtocolHealth" && key != "AuditEvents" && !bytes.Equal(a[key], b[key]) {
			t.Fatalf("business field changed: %s", key)
		}
	}
	node, err := target.NodeByCredentialHash(ctx, credentialHash)
	require(err)
	if node.ID != "node-one" || node.LastHeartbeatAt != nil || len(node.Resources) != 0 || node.DesiredGeneration < 1 {
		t.Fatal("node identity/health restoration incorrect")
	}
	desired, _, err := target.DesiredNodeConfig(ctx, "node-one")
	require(err)
	if desired.Generation != node.DesiredGeneration {
		t.Fatal("desired generation missing")
	}
	migratedRule, err := target.ForwardingRule(ctx, rule.ID)
	require(err)
	if migratedRule.Name != rule.Name || migratedRule.ListenPort != rule.ListenPort || migratedRule.TrafficLimitBytes != rule.TrafficLimitBytes || !reflect.DeepEqual(migratedRule.Targets, rule.Targets) {
		t.Fatal("forwarding rule changed after restore")
	}
	if _, err = target.SessionByTokenHash(ctx, securetoken.Hash(oldSession.AccessToken), now); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("old session was retained")
	}
	migratedSub, err := subscriptions.NewService(target, nil, nil).Detail(ctx, "admin-one", item.ID)
	require(err)
	if migratedSub.Token != subscription.Token {
		t.Fatal("subscription address changed")
	}
	_, lines, err := subscriptions.NewService(target, nil, nil).Public(ctx, subscription.Token)
	require(err)
	if len(lines) != 1 || lines[0].Name != "中文规则" {
		t.Fatal("restored feed unavailable")
	}
	replay, err := importer.Restore(ctx, "admin-one", "isolated-test-password", backupPass, "restore-fixture", preview.Digest, "https://target.example.com", raw)
	require(err)
	if !replay.Replayed || replay.RecoveryID != result.RecoveryID {
		t.Fatal("restore is not idempotent")
	}
	persisted, exists, err := target.MigrationRuntime(ctx)
	require(err)
	if !exists || !reflect.DeepEqual(persisted, runtime) {
		t.Fatal("runtime secrets missing")
	}
	// A valid encrypted archive with a DB constraint violation must roll back all stores.
	broken, err := migrationbackup.Open(raw, backupPass, "v0.1.47")
	require(err)
	broken.State.Usage["usage_customer_totals"] = bytes.ReplaceAll(broken.State.Usage["usage_customer_totals"], []byte(`"actual_bytes":123`), []byte(`"actual_bytes":-1`))
	bad, err := migrationbackup.Seal(broken, backupPass)
	require(err)
	if _, err = importer.Restore(ctx, "admin-one", "isolated-test-password", backupPass, "broken-fixture", migrationbackup.Digest(bad), "https://target.example.com", bad); err == nil {
		t.Fatal("invalid ledger accepted")
	}
	afterFailure, err := target.ExportMigration(ctx)
	require(err)
	if !reflect.DeepEqual(restored, afterFailure) {
		t.Fatal("failed restore changed data")
	}
	if _, err = importer.Restore(ctx, "admin-one", "isolated-test-password", backupPass, "restore-fixture", migrationbackup.Digest(bad), "https://target.example.com", bad); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("key conflict accepted")
	}
	recoveryRaw, err := importer.Recovery(result.RecoveryID)
	require(err)
	recovery, err := migrationbackup.Open(recoveryRaw, backupPass, "v0.1.47")
	require(err)
	if !reflect.DeepEqual(recovery.State, before) {
		t.Fatal("recovery backup is not target pre-import state")
	}
	require(target.Close())
	target, err = Open(ctx, targetDSN, auth.Administrator{})
	require(err)
	defer target.Close()
	_, exists, err = target.MigrationRuntime(ctx)
	require(err)
	if !exists {
		t.Fatal("restart lost runtime")
	}
	_, err = target.NodeByCredentialHash(ctx, credentialHash)
	require(err)
	_, err = service(target).Restore(ctx, "admin-one", "isolated-test-password", backupPass, "rollback-fixture", migrationbackup.Digest(recoveryRaw), "https://target.example.com", recoveryRaw)
	require(err)
	_, err = target.AdministratorByUsername(ctx, "target")
	require(err)
	recovered, err := target.ExportMigration(ctx)
	require(err)
	if !reflect.DeepEqual(recovered.Usage, before.Usage) {
		t.Fatal("recovery ledger not restored")
	}
}
