package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func TestPreparedTransactionProfileSavesWhileRulePreparationIsPending(t *testing.T) {
	s, _, request, _, _ := ruleDeletionPGFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	customer, _, err := customers.NewService(s, time.Now).CreateCustomer(ctx, "admin-one", customers.CustomerInput{
		AccountKind: customers.AccountService, Username: "independent-profile", IdempotencyKey: "profile-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Operation = rulegroups.BatchPause
	input := rulegroups.BatchInput{Request: request, UpdatedAt: time.Now().UTC(), IdempotencyKey: "slow-rule", RequestSHA256: "slow-rule", CreatedBy: "admin-one"}
	started, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	defer release()
	done := make(chan error, 1)
	calls := 0
	go func() {
		_, err := transactPrepared(ctx, s, func(state *memoryrepo.Store) (rulegroups.BatchResult, error) {
			calls++
			result, _, err := state.BatchUpdateRules(ctx, input, audit.Event{ID: "slow-rule"})
			if err == nil && calls == 1 {
				close(started)
				select {
				case <-unblock:
				case <-ctx.Done():
					return result, ctx.Err()
				}
			}
			return result, err
		})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatal("rule preparation failed before synchronization", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	profileCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	edit := customers.SaveCustomerInput{Customer: customer, ExpectedRevision: customer.Revision, IdempotencyKey: "profile-edit", RequestSHA256: "profile-edit", CreatedBy: "admin-one"}
	edit.Customer.DisplayName = "saved while rule was preparing"
	edit.Customer.Revision++
	_, _, err = s.SaveCustomer(profileCtx, edit, audit.Event{ID: "profile-edit"})
	release()
	ruleErr := <-done
	if err != nil {
		t.Fatal("profile save waited behind private rule preparation", err)
	}
	if ruleErr != nil || calls != 2 {
		t.Fatal("stale rule preparation did not recompute exactly once", calls, ruleErr)
	}
	current, err := s.Customer(ctx, customer.ID)
	if err != nil || current.DisplayName != edit.Customer.DisplayName {
		t.Fatal("rule commit overwrote concurrently saved profile", err)
	}
	rule, err := s.ForwardingRule(ctx, request.RuleIDs[0])
	if err != nil || !rule.Paused {
		t.Fatal("profile save lost the rule change", err)
	}
	events, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.ID == "slow-rule" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("discarded rule preparation leaked an audit entry", count)
	}
}

func TestPreparedTransactionRejectsCrossStoreAndSameRevisionUpdates(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_revision=%t", repair), func(t *testing.T) {
			ctx := context.Background()
			dsn := isolatedDatabaseURL(t)
			admin := bootstrapAdministrator(t)
			s, err := Open(ctx, dsn, admin)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			operation := func(state *memoryrepo.Store) (struct{}, error) {
				return struct{}{}, state.AppendAudit(ctx, audit.Event{ID: "prepared-after-external"})
			}
			prepared, err := prepareTransaction(ctx, s, operation)
			if err != nil {
				t.Fatal(err)
			}
			other, err := Open(ctx, dsn, admin)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if err := other.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "external-edit"}); err != nil {
				t.Fatal(err)
			}
			if repair {
				if _, err := other.db.ExecContext(ctx, "UPDATE nyvp_control_snapshots SET revision=$1 WHERE singleton=true", prepared.base.revision); err != nil {
					t.Fatal(err)
				}
			}
			before, revision := healthSnapshot(t, s)
			if committed, err := commitPreparedTransaction(ctx, s, prepared); err != nil || committed {
				t.Fatal("stale candidate was not rejected before business writes", committed, err)
			}
			after, next := healthSnapshot(t, s)
			if !bytes.Equal(before, after) || next != revision {
				t.Fatal("stale candidate altered the database")
			}
			if _, err := transactPrepared(ctx, s, operation); err != nil {
				t.Fatal(err)
			}
			current, err := s.AdministratorByID(ctx, admin.ID)
			if err != nil || !current.MustChangePassword {
				t.Fatal("recomputed candidate overwrote external update", err)
			}
		})
	}
}

func TestPreparedTransactionPreservesConcurrentTrafficAndHealth(t *testing.T) {
	for _, mode := range []string{"traffic", "health"} {
		t.Run(mode, func(t *testing.T) {
			s, _, proof := protocolHealthFixture(t)
			ctx := context.Background()
			if err := s.PublishProtocolHealth(ctx, proof); err != nil {
				t.Fatal(err)
			}
			operation := func(state *memoryrepo.Store) (struct{}, error) {
				return struct{}{}, state.AppendAudit(ctx, audit.Event{ID: "fold-latest-projections"})
			}
			prepared, err := prepareTransaction(ctx, s, operation)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "traffic" {
				if err := s.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: proof.RuleID, ExpectedRevision: proof.RuleRevision, TotalBytes: 123456, At: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				_, revision := healthSnapshot(t, s)
				if revision != prepared.base.revision {
					t.Fatal("fixture did not exercise an overlay-only update")
				}
			} else {
				proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
				proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
				if err := s.PublishProtocolHealth(ctx, proof); err != nil {
					t.Fatal(err)
				}
			}
			if committed, err := commitPreparedTransaction(ctx, s, prepared); err != nil || committed {
				t.Fatal("candidate ignored a concurrent projection update", committed, err)
			}
			if _, err := transactPrepared(ctx, s, operation); err != nil {
				t.Fatal(err)
			}
			if mode == "traffic" {
				rule, err := s.ForwardingRule(ctx, proof.RuleID)
				if err != nil || rule.TrafficUsedBytes != 123456 {
					t.Fatal("rule counter was overwritten", err)
				}
			}
			assertHealth(t, s, proof)
		})
	}
}

func TestPreparedTransactionCommitFailureDoesNotReplayOrNotify(t *testing.T) {
	s, _, request, _, _ := ruleDeletionPGFixture(t)
	ctx := context.Background()
	request.Operation = rulegroups.BatchPause
	input := rulegroups.BatchInput{Request: request, UpdatedAt: time.Now().UTC(), IdempotencyKey: "failed-prepared-pause", RequestSHA256: "failed-prepared-pause", CreatedBy: "admin-one"}
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	beforeAudit, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wake := s.DesiredConfigChanges("node")
	defer s.ReleaseDesiredConfigChanges("node", wake)
	if _, err := s.db.ExecContext(ctx, "CREATE FUNCTION reject_prepared_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated prepared commit rejection'; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER reject_prepared_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_prepared_commit()"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = transactPrepared(ctx, s, func(state *memoryrepo.Store) (rulegroups.BatchResult, error) {
		calls++
		result, _, err := state.BatchUpdateRules(ctx, input, audit.Event{ID: "failed-prepared-pause"})
		return result, err
	})
	var failure *snapshotTransactionFailure
	if calls != 1 || !errors.As(err, &failure) || failure.stage != "commit" || failure.outcome != "unconfirmed" {
		t.Fatal("failed COMMIT was replayed or lost its uncertain outcome", calls, err)
	}
	after, next := healthSnapshot(t, s)
	if revision != next || !bytes.Equal(before, after) || s.readCache.revision != cached.revision || s.readCache.forwarding != cached.forwarding {
		t.Fatal("failed commit published private snapshot or cache")
	}
	afterAudit, err := s.AuditEvents(ctx)
	if err != nil || len(beforeAudit) != len(afterAudit) {
		t.Fatal("failed commit published an audit", err)
	}
	select {
	case <-wake:
		t.Fatal("failed commit notified a node")
	default:
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER reject_prepared_commit ON nyvp_control_snapshots; DROP FUNCTION reject_prepared_commit()"); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := s.BatchUpdateRules(ctx, input, audit.Event{ID: "failed-prepared-pause"}); err != nil || replayed {
		t.Fatal("failed commit prevented retry with the same key", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("successful retry did not notify the node")
	}
	if _, replayed, err := s.BatchUpdateRules(ctx, input, audit.Event{ID: "failed-prepared-pause"}); err != nil || !replayed {
		t.Fatal("committed retry did not replay idempotently", err)
	}
}

func TestPreparedTransactionRejectsInvalidSnapshotAndReleasesSingleConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t), PoolConfig{MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, revision := healthSnapshot(t, s)
	now := time.Now().UTC()
	_, err = transactPrepared(ctx, s, func(state *memoryrepo.Store) (struct{}, error) {
		return struct{}{}, state.CreateSession(ctx, auth.Session{ID: "invalid-private-session", AdminID: "missing-admin", TokenHash: fmt.Sprintf("%064x", 123), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "invalid-private-session"})
	})
	var failure *snapshotTransactionFailure
	if !errors.As(err, &failure) || failure.stage != "snapshot_validate" {
		t.Fatal("invalid private state reached commit", err)
	}
	after, next := healthSnapshot(t, s)
	if revision != next || !bytes.Equal(before, after) {
		t.Fatal("invalid snapshot changed durable state")
	}
	if _, _, err := customers.NewService(s, time.Now).CreateCustomer(ctx, "admin-one", customers.CustomerInput{AccountKind: customers.AccountService, Username: "single-connection", IdempotencyKey: "single-connection"}); err != nil {
		t.Fatal("prepared commit deadlocked or poisoned a single-connection pool", err)
	}
}

func TestPreparedTransactionBoundsRecomputationBeforeSerializedFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := 0
	_, err = transactPrepared(ctx, s, func(state *memoryrepo.Store) (struct{}, error) {
		calls++
		// Deliberately inject a competitor between reading and committing. This
		// test-only external write is not a permitted production callback effect.
		if calls <= 2 {
			if err := s.AppendAudit(ctx, audit.Event{ID: fmt.Sprintf("competitor-%d", calls)}); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, state.AppendAudit(ctx, audit.Event{ID: "bounded-preparation"})
	})
	if err != nil || calls != 3 {
		t.Fatal("recomputation was unbounded or fallback failed", calls, err)
	}
	events, err := s.AuditEvents(ctx)
	if err != nil || len(events) != 3 {
		t.Fatal("discarded candidates leaked or lost writes", len(events), err)
	}
}

func TestPreparedTransactionReservesFallbackBudgetDuringTelemetry(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	if err := s.PublishProtocolHealth(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	stop, ready, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var lastTotal int64
	lastProof := proof
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		samples := 0
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-ticker.C:
				total := lastTotal + 1
				if err := s.ApplyRuleTraffic(context.Background(), forwarding.RuleTrafficProjection{RuleID: proof.RuleID, ExpectedRevision: proof.RuleRevision, TotalBytes: total, At: time.Now().UTC()}); err != nil {
					done <- err
					return
				}
				lastTotal = total
				next := lastProof
				next.VerifiedAt = next.VerifiedAt.Add(time.Millisecond)
				next.LeaseExpiresAt = next.LeaseExpiresAt.Add(time.Millisecond)
				if err := s.PublishProtocolHealth(context.Background(), next); err != nil {
					done <- err
					return
				}
				lastProof = next
				samples++
				if samples == 1 {
					close(ready)
				}
			}
		}
	}()
	joined := false
	defer func() {
		if !joined {
			close(stop)
			<-done
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		joined = true
		t.Fatal("telemetry failed before preparation", err)
	case <-time.After(5 * time.Second):
		t.Fatal("telemetry did not start")
	}
	// Model a slower CPU with a sizeable private computation, not a held DB
	// lock. Three computations cannot fit this budget; two comfortably can.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := transactPrepared(ctx, s, func(state *memoryrepo.Store) (struct{}, error) {
		calls++
		timer := time.NewTimer(550 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		}
		return struct{}{}, state.AppendAudit(ctx, audit.Event{ID: "telemetry-budget"})
	})
	// Stop and join the sampler before inspecting its last accepted values.
	close(stop)
	sampleErr := <-done
	joined = true
	if sampleErr != nil {
		t.Fatal("telemetry failed", sampleErr)
	}
	if err != nil || calls != 2 {
		t.Fatal("speculative recomputation exhausted the commit budget", calls, err)
	}
	rule, readErr := s.ForwardingRule(context.Background(), proof.RuleID)
	if readErr != nil || lastTotal < 2 || rule.TrafficUsedBytes != lastTotal {
		t.Fatal("successful fallback lost concurrent traffic", readErr)
	}
	assertHealth(t, s, lastProof)
	events, readErr := s.AuditEvents(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	count := 0
	for _, event := range events {
		if event.ID == "telemetry-budget" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("discarded preparation leaked an audit entry", count)
	}
}

func TestPreparedTransactionImportOwnsAuditMetadata(t *testing.T) {
	s, _, _ := protocolHealthFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rule := forwarding.Rule{
		ID: "metadata-import-rule", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin-one",
		CustomerID: forwarding.AdministratorSubjectID("admin-one"), Name: "metadata import",
		EntryGroupID: "group", EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressTCP,
		Protocol: forwarding.ProtocolTCP, ListenPort: 12001, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 19001}},
		SelectionPolicy: forwarding.SelectionRoundRobin, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	input := forwarding.ImportInput{Candidates: []forwarding.ImportCandidate{{Line: 1, Operation: forwarding.ImportCreate, Rule: rule}}, IdempotencyKey: "import-private-audit", RequestSHA256: "import-private-audit", CreatedBy: "admin-one"}
	metadata := map[string]any{"caller": "unchanged"}
	if _, _, err := s.ImportForwardingRules(ctx, input, audit.Event{ID: "import-private-audit", ResourceID: "import-private-audit", Metadata: metadata, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 || metadata["caller"] != "unchanged" {
		t.Fatal("import modified caller metadata")
	}
	events, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ID == "import-private-audit" {
			if event.Metadata["created"] != float64(1) || event.Metadata["updated"] != float64(0) || event.Metadata["caller"] != "unchanged" {
				t.Fatal("import audit lost its independent metadata")
			}
			return
		}
	}
	t.Fatal("import audit was not persisted")
}

func TestPreparationBudgetCancellationDoesNotBorrowConnection(t *testing.T) {
	s, err := Open(context.Background(), isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 2 {
		release, err := s.prepareBudget.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	calls := 0
	_, err = prepareTransaction(ctx, s, func(*memoryrepo.Store) (struct{}, error) {
		calls++
		return struct{}{}, nil
	})
	var failure *snapshotTransactionFailure
	if calls != 0 || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &failure) || failure.stage != "prepare_queue" || s.db.Stats().InUse != 0 {
		t.Fatal("waiting preparation borrowed a connection or ignored cancellation", err)
	}
}
