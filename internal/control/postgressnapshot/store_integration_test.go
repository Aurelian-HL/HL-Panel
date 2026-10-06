package postgressnapshot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
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
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("NYVP_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set NYVP_TEST_POSTGRES_URL for an explicitly isolated PostgreSQL test database")
	}
	if os.Getenv("NYVP_TEST_POSTGRES_ALLOW_MUTATION") != "true" {
		t.Fatal("PostgreSQL test requires explicit NYVP_TEST_POSTGRES_ALLOW_MUTATION=true")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("test connection must be a PostgreSQL URL")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open isolated test database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	var name string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal("connect to isolated test database failed")
	}
	if !strings.HasPrefix(name, "nyvp_") || !strings.HasSuffix(name, "_test") {
		t.Fatal("refusing to mutate database: name must match nyvp_*_test")
	}
	schema := fmt.Sprintf("snapshot_test_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal("create isolated test schema failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error("clean up this test's isolated schema failed")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func bootstrapAdministrator(t *testing.T) auth.Administrator {
	t.Helper()
	hash, err := auth.HashPassword("isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	return auth.Administrator{ID: "admin-one", Username: "admin", PasswordHash: hash, CreatedAt: time.Now().UTC()}
}

func TestPostgreSQLRestartPreservesIdentityRoutingAuditAndIdempotency(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	now := time.Now().UTC()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	event := audit.Event{ID: "event-one", ActorType: "administrator", ActorID: admin.ID, Action: "test", Outcome: "succeeded", CreatedAt: now, Metadata: map[string]any{}}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.CreateSession(ctx, auth.Session{ID: "session-one", AdminID: admin.ID, TokenHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, event))
	require(s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "enroll-one", Name: "node-one", TokenHash: "enrollment-hash", ExpiresAt: now.Add(time.Hour)}, event))
	consume := enrollment.ConsumeInput{TokenHash: "enrollment-hash", CredentialHash: "credential-hash", Node: nodes.Node{ID: "node-one", Hostname: "isolated.example.test", CreatedAt: now, UpdatedAt: now}}
	_, err = s.ConsumeEnrollmentToken(ctx, consume, now, event)
	require(err)
	_, err = s.UpdateHeartbeat(ctx, "node-one", nodes.Heartbeat{Hostname: "isolated.example.test", Resources: map[string]any{"memory_bytes": 1024.0}}, now, event)
	require(err)
	require(s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "group-one", Name: "isolated-group", Kind: groups.KindEdge, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now}, event))
	_, _, err = s.UpsertGroupMember(ctx, groups.Member{GroupID: "group-one", NodeID: "node-one", Weight: 3, CreatedAt: now, UpdatedAt: now}, event)
	require(err)
	poolInput := endpoints.CreatePoolInput{ID: "pool-one", Name: "isolated", GroupID: "group-one", Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "edge.example.test", Port: 443, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, IdempotencyKey: "pool-key", RequestSHA256: "pool-request-hash", CreatedBy: admin.ID, CreatedAt: now}
	_, _, err = s.CreateEndpointPool(ctx, poolInput, event)
	require(err)
	memberInput := endpoints.AddMemberInput{PoolID: "pool-one", GroupID: "group-one", NodeID: "node-one", Weight: 3, IdempotencyKey: "member-key", RequestSHA256: "member-request-hash", CreatedBy: admin.ID, CreatedAt: now}
	_, _, err = s.AddEndpointPoolMember(ctx, memberInput, event)
	require(err)
	revisionInput := generations.CreateGroupRevisionInput{ID: "revision-one", GroupID: "group-one", Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[],"outbounds":[]}`), ConfigSHA256: "revision-hash", RequestSHA256: "revision-request-hash", IdempotencyKey: "revision-key", CreatedBy: admin.ID, CreatedAt: now}
	revision, err := s.CreateGroupRevision(ctx, revisionInput, event)
	require(err)
	if len(revision.Assignments) != 1 {
		t.Fatal("missing generated node assignment")
	}
	configuration := revision.Assignments[0]
	_, err = s.RecordApplyResult(ctx, generations.ApplyResult{ID: "apply-one", NodeID: "node-one", Generation: configuration.Generation, Phase: "commit", Status: "succeeded", ConfigSHA256: configuration.ConfigSHA256, CreatedAt: now}, event)
	require(err)
	beforeAudit, err := s.AuditEvents(ctx)
	require(err)
	require(s.Close())
	// A different bootstrap identity must not replace a populated database.
	reopened, err := Open(ctx, dsn, auth.Administrator{ID: "wrong", Username: "replacement"})
	require(err)
	defer reopened.Close()
	persistedAdmin, err := reopened.AdministratorByUsername(ctx, "admin")
	require(err)
	if persistedAdmin.ID != admin.ID || !bytes.Equal(persistedAdmin.PasswordHash, admin.PasswordHash) {
		t.Fatal("bootstrap overwrote persisted administrator")
	}
	if _, err := reopened.AdministratorByUsername(ctx, "replacement"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("replacement bootstrap was applied")
	}
	_, err = reopened.SessionByTokenHash(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now)
	require(err)
	node, err := reopened.NodeByCredentialHash(ctx, "credential-hash")
	require(err)
	if node.LastHeartbeatAt == nil || node.DesiredGeneration != 1 || node.LastApplyStatus != "succeeded" {
		t.Fatal("node state was not preserved")
	}
	if _, err := reopened.ConsumeEnrollmentToken(ctx, consume, now, event); !errors.Is(err, faults.ErrAlreadyUsed) {
		t.Fatal("consumed enrollment token became reusable")
	}
	pool, replayed, err := reopened.CreateEndpointPool(ctx, poolInput, event)
	require(err)
	if !replayed || pool.ID != "pool-one" || pool.MemberCount != 1 {
		t.Fatal("endpoint projection/idempotency was not preserved")
	}
	_, replayed, err = reopened.AddEndpointPoolMember(ctx, memberInput, event)
	require(err)
	if !replayed {
		t.Fatal("member confirmation idempotency was not preserved")
	}
	repeated, err := reopened.CreateGroupRevision(ctx, revisionInput, event)
	require(err)
	if !repeated.Replayed || repeated.Generation.ID != revision.Generation.ID {
		t.Fatal("revision idempotency was not preserved")
	}
	conflicting := revisionInput
	conflicting.RequestSHA256 = "different-request"
	if _, err := reopened.CreateGroupRevision(ctx, conflicting, event); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("request hash was not preserved")
	}
	desired, _, err := reopened.DesiredNodeConfig(ctx, "node-one")
	require(err)
	if desired.ConfigSHA256 != configuration.ConfigSHA256 {
		t.Fatal("node config was not preserved")
	}
	historical, err := reopened.UsageConfigInput(ctx, "node-one", configuration.Generation)
	require(err)
	if !bytes.Equal(historical.Config.Config, configuration.Config) || historical.ApplyResults[agentv1.ApplyPhaseCommit].ID != "apply-one" {
		t.Fatal("historical accounting configuration or receipt lost across restart")
	}
	afterAudit, err := reopened.AuditEvents(ctx)
	require(err)
	if len(afterAudit) != len(beforeAudit) {
		t.Fatal("restart or replay changed persisted audit count")
	}
}

func TestPostgreSQLConcurrentWritesRollbackAndCorruption(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	s, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const writers = 12
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.AppendAudit(ctx, audit.Event{ID: fmt.Sprintf("concurrent-%d", i), Action: "concurrent"})
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	audits, err := s.AuditEvents(ctx)
	if err != nil || len(audits) != writers {
		t.Fatal("concurrent updates were lost")
	}
	// Simulate a domain failure after an in-memory write: none may be committed.
	err = mutate(ctx, s, func(state *memoryrepo.Store) error {
		_ = state.AppendAudit(ctx, audit.Event{ID: "must-rollback"})
		return faults.ErrConflict
	})
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal("domain error changed")
	}
	audits, err = s.AuditEvents(ctx)
	if err != nil || len(audits) != writers {
		t.Fatal("failed transaction leaked state")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET format_version=999`); err != nil {
		t.Fatal("set isolated corruption fixture failed")
	}
	if _, err := s.ListNodes(ctx); err == nil {
		t.Fatal("unknown snapshot version accepted")
	}
	if reopened, err := Open(ctx, dsn, bootstrapAdministrator(t)); err == nil {
		reopened.Close()
		t.Fatal("corrupt store was reinitialized")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET format_version=1, payload=$1`, []byte(`{"Version":1}`)); err != nil {
		t.Fatal("set incomplete fixture failed")
	}
	if _, err := s.ListNodes(ctx); err == nil {
		t.Fatal("incomplete snapshot accepted")
	}
}
