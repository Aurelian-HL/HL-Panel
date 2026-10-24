package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func nodeHistoryFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := isolatedDatabaseURL(t)
	s, err := Open(context.Background(), dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, now := context.Background(), time.Now().UTC()
	if err = s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "history-token", TokenHash: "history-token-hash", ExpiresAt: now.Add(time.Hour)}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "history-token-hash", CredentialHash: "history-credential", Node: nodes.Node{ID: "history-node", Hostname: "loopback", CreatedAt: now, UpdatedAt: now}}, now, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "history-group", Name: "isolated", Kind: groups.KindEdge, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.UpsertGroupMember(ctx, groups.Member{GroupID: "history-group", NodeID: "history-node", Weight: 1, CreatedAt: now, UpdatedAt: now}, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	return s, dsn
}

func TestArchivedHistoryLargerThanLegacyHotLimitRoundTripsMigration(t *testing.T) {
	source, _ := nodeHistoryFixture(t)
	ctx := context.Background()
	input := historyRevision(1)
	input.Config = json.RawMessage(`{"services":[],"padding":"` + strings.Repeat("x", 50<<10) + `"}`)
	created, err := source.CreateGroupRevision(ctx, input, audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	current := created.Assignments[0]
	current.Generation = 351
	current.ID = "history-current"
	// Seed the same shape as hundreds of rule edits without replaying those
	// edits or copying group-revision history into this storage-boundary test.
	tx, err := source.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := readState(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var records map[string]map[string]json.RawMessage
	if err = json.Unmarshal(fields["Nodes"], &records); err != nil {
		t.Fatal(err)
	}
	var node nodes.Node
	if err = json.Unmarshal(records[current.NodeID]["Node"], &node); err != nil {
		t.Fatal(err)
	}
	node.DesiredGeneration = current.Generation
	records[current.NodeID]["Node"], _ = json.Marshal(node)
	fields["Nodes"], _ = json.Marshal(records)
	fields["NodeConfigsByNode"], _ = json.Marshal(map[string]map[int64]generations.NodeConfigGeneration{current.NodeID: {current.Generation: current}})
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = memoryrepo.DecodeSnapshot(raw); err != nil {
		t.Fatal("invalid current-generation fixture", err)
	}
	if _, err = tx.Exec(`UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, raw); err != nil {
		t.Fatal(err)
	}
	history := make([]memoryrepo.NodeConfigHistory, 350)
	for i := range history {
		c := current
		c.Generation, c.ID = int64(i+1), fmt.Sprintf("history-archived-%d", i+1)
		history[i].Config = c
	}
	if err = persistNodeConfigHistory(ctx, tx, history); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	target, _ := nodeHistoryFixture(t)
	for _, s := range []*Store{source, target} {
		runner, e := usagemigration.New(s.db)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = runner.Apply(ctx); e != nil {
			t.Fatal(e)
		}
	}
	exported, err := source.ExportMigration(ctx)
	if err != nil || len(exported.Snapshot) <= 16<<20 {
		t.Fatal("large history export failed", len(exported.Snapshot), err)
	}
	runtime := migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{7}, 32)}
	newBackupService := func(s *Store) *migrationbackup.Service {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
		return migrationbackup.New(s, auth.NewService(s, audit.NewService(s), time.Now, time.Hour), audit.NewService(s), "v0.1.65", directory, func() migrationbackup.RuntimeSecrets { return runtime }, nil)
	}
	exporter := newBackupService(source)
	archive, err := exporter.Export(ctx, "admin-one", "isolated-test-password", "isolated-backup-pass", "https://source.example.com")
	if err != nil {
		t.Fatal(err)
	}
	importer := newBackupService(target)
	preview, err := importer.Preview(ctx, "admin-one", "isolated-test-password", "isolated-backup-pass", archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = importer.Restore(ctx, "admin-one", "isolated-test-password", "isolated-backup-pass", "large-history-restore", preview.Digest, "https://target.example.com", archive); err != nil {
		t.Fatal("large history restore failed", err)
	}
	var hotBytes, archivedCount int
	if err = target.db.QueryRow(`SELECT octet_length(payload) FROM nyvp_control_snapshots`).Scan(&hotBytes); err != nil {
		t.Fatal(err)
	}
	if err = target.db.QueryRow(`SELECT count(*) FROM hl_panel_node_config_history`).Scan(&archivedCount); err != nil || archivedCount != 350 || hotBytes >= maxHotSnapshotBytes {
		t.Fatal("restore did not retain compact hot state and all history", archivedCount, hotBytes, err)
	}
	for _, generation := range []int64{1, 175, 350, 351} {
		receipt, e := target.UsageConfigInput(ctx, current.NodeID, generation)
		if e != nil || receipt.Config.Generation != generation || receipt.Config.ConfigSHA256 != current.ConfigSHA256 {
			t.Fatal("restored generation unavailable", generation, e)
		}
	}
	t.Logf("complete control backup=%d bytes; restored hot snapshot=%d bytes; archived generations=%d", len(exported.Snapshot), hotBytes, archivedCount)
}

func historyRevision(number int) generations.CreateGroupRevisionInput {
	return generations.CreateGroupRevisionInput{ID: fmt.Sprintf("history-revision-%d", number), GroupID: "history-group", Engine: agentv1.EngineGOST,
		Config:       json.RawMessage(fmt.Sprintf(`{"services":[],"test":%d,"padding":"%s"}`, number, strings.Repeat("x", 4096))),
		ConfigSHA256: "fixture", RequestSHA256: fmt.Sprint(number), IdempotencyKey: fmt.Sprintf("history-key-%d", number), CreatedBy: "admin-one", CreatedAt: time.Now().UTC()}
}

func TestArchivedGenerationPreservesLateUsageReceiptsAndRestart(t *testing.T) {
	s, dsn := nodeHistoryFixture(t)
	ctx := context.Background()
	first, err := s.CreateGroupRevision(ctx, historyRevision(1), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	c := first.Assignments[0]
	commit := generations.ApplyResult{ID: "history-commit", NodeID: c.NodeID, Generation: c.Generation, ConfigSHA256: c.ConfigSHA256, Phase: "commit", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if _, err = s.RecordApplyResult(ctx, commit, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 4; i++ {
		if _, err = s.CreateGroupRevision(ctx, historyRevision(i), audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM hl_panel_node_config_history`).Scan(&count); err != nil || count != 3 {
		t.Fatal("history was not archived", count, err)
	}
	before, err := s.UsageConfigInput(ctx, c.NodeID, c.Generation)
	if err != nil || !reflect.DeepEqual(before.Config, c) || len(before.ApplyResults) != 1 {
		t.Fatal("late usage lost its receipt", err)
	}
	verify := commit
	verify.ID = "history-verify"
	verify.Phase = "verify"
	if _, err = s.RecordApplyResult(ctx, verify, audit.Event{}); err != nil {
		t.Fatal("late receipt rejected", err)
	}
	if _, err = s.RecordApplyResult(ctx, verify, audit.Event{}); err != nil {
		t.Fatal("receipt replay failed", err)
	}
	if _, err = s.CreateGroupRevision(ctx, historyRevision(1), audit.Event{}); err != nil {
		t.Fatal("revision replay failed", err)
	}
	runner, err := usagemigration.New(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	exported, err := s.ExportMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := memoryrepo.DecodeSnapshot(exported.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := decoded.UsageConfigInput(ctx, c.NodeID, c.Generation)
	if err != nil || len(joined.ApplyResults) != 2 {
		t.Fatal("export overwrote the late receipt with stale archived data", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, err := restarted.UsageConfigInput(ctx, c.NodeID, c.Generation)
	if err != nil || !reflect.DeepEqual(after, joined) {
		t.Fatal("restart lost history", err)
	}
	if _, err = restarted.UsageConfigInput(ctx, c.NodeID, 999); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("unknown generation accepted", err)
	}
	if _, err = restarted.db.Exec(`UPDATE hl_panel_node_config_history SET payload='{}'::bytea WHERE generation=2`); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.UsageConfigInput(ctx, c.NodeID, 2); err == nil {
		t.Fatal("corrupt history accepted")
	}
}

func TestHistoryArchiveFailureRollsBackConfigurationAndAudit(t *testing.T) {
	s, _ := nodeHistoryFixture(t)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		if _, err := s.CreateGroupRevision(ctx, historyRevision(i), audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	var before []byte
	var revision int64
	if err := s.db.QueryRow(`SELECT payload,revision FROM nyvp_control_snapshots`).Scan(&before, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE hl_panel_node_config_history ADD CONSTRAINT isolated_reject_history CHECK(generation < 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroupRevision(ctx, historyRevision(3), audit.Event{ID: "failed-history-audit"}); err == nil {
		t.Fatal("archive failure was ignored")
	}
	var after []byte
	var afterRevision int64
	if err := s.db.QueryRow(`SELECT payload,revision FROM nyvp_control_snapshots`).Scan(&after, &afterRevision); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || revision != afterRevision {
		t.Fatal("failed archive advanced snapshot")
	}
	events, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ID == "failed-history-audit" {
			t.Fatal("failed archive committed audit")
		}
	}
}

func TestLegacySnapshotHistoryMigrationIsAtomicAndPreservesAllGenerations(t *testing.T) {
	s, dsn := nodeHistoryFixture(t)
	ctx := context.Background()
	for i := 1; i <= 12; i++ {
		if _, err := s.CreateGroupRevision(ctx, historyRevision(i), audit.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := readState(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = joinNodeConfigHistory(ctx, tx, state); err != nil {
		t.Fatal(err)
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM hl_panel_node_config_history`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE nyvp_control_snapshots SET payload=$1`, raw); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var count int
	if err = other.db.QueryRow(`SELECT count(*) FROM hl_panel_node_config_history`).Scan(&count); err != nil || count != 11 {
		t.Fatal(count, err)
	}
	for i := int64(1); i <= 12; i++ {
		if _, err = other.UsageConfigInput(ctx, "history-node", i); err != nil {
			t.Fatal("migration lost a generation", i, err)
		}
	}
	// A second startup and another writer must observe the compacted row.
	if _, err = s.ListNodes(ctx); err != nil {
		t.Fatal(err)
	}
	var hot []byte
	if err = s.db.QueryRow(`SELECT payload FROM nyvp_control_snapshots`).Scan(&hot); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(hot, &fields); err != nil {
		t.Fatal(err)
	}
	var configs map[string]map[string]json.RawMessage
	if err = json.Unmarshal(fields["NodeConfigsByNode"], &configs); err != nil {
		t.Fatal(err)
	}
	if len(configs["history-node"]) != 1 {
		t.Fatal("hot snapshot retained history")
	}
}
