package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func batchCreationPGRequest() forwarding.BatchCreateRequest {
	first := forwarding.Request{EntryGroupID: "group", EgressMode: forwarding.EgressDirect,
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
		VLESSFlow: "xtls-rprx-vision", VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5,
		VLESSSOCKS5Host: "192.0.2.10", VLESSSOCKS5Port: 1080, VLESSSOCKS5Username: "isolated", VLESSSOCKS5Password: "isolated",
		SelectionPolicy: forwarding.SelectionRoundRobin}
	second := first
	second.Name, second.VLESSSOCKS5Host = "批量客户", "192.0.2.11"
	return forwarding.BatchCreateRequest{Rules: []forwarding.Request{first, second}}
}

func batchCreationPGService(s *Store) *forwarding.Service {
	return forwarding.NewService(s, time.Now, forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "example.com", Destination: "example.com:443"}))
}

func TestBatchCreatePostgreSQLAtomicPersistenceAndReplay(t *testing.T) {
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	raw, _ := healthSnapshot(t, s)
	var snapshot struct {
		NodeConfigsByNode map[string]map[int64]generations.NodeConfigGeneration
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	before := snapshot.NodeConfigsByNode["node"][proof.NodeConfigGeneration]
	if before.Generation == 0 {
		t.Fatal("fixture has no applied configuration")
	}
	var oldBundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(before.Config, &oldBundle); err != nil {
		t.Fatal(err)
	}
	input := batchCreationPGRequest()
	result, replayed, err := batchCreationPGService(s).BatchCreateForAdministrator(ctx, "admin-one", input, "batch-persist")
	if err != nil || replayed || result.Created != 2 || result.Items[0].Name != "192.0.2.10" || result.Items[1].Name != "批量客户" {
		t.Fatal("batch was not saved", replayed, err)
	}
	config, _, err := s.DesiredNodeConfig(ctx, "node")
	if err != nil || config.Generation != before.Generation+1 {
		t.Fatal("batch generated more than one node revision", err)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil || len(bundle.Fragments) != len(oldBundle.Fragments)+2 {
		t.Fatal("persisted listeners are incomplete", err)
	}
	for _, original := range oldBundle.Fragments {
		found := false
		for _, fragment := range bundle.Fragments {
			if fragment.GroupID == original.GroupID {
				found = true
				if !reflect.DeepEqual(fragment, original) {
					t.Fatal("batch changed an existing listener")
				}
			}
		}
		if !found {
			t.Fatal("batch dropped an existing listener")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, replayed, err := batchCreationPGService(reopened).BatchCreateForAdministrator(ctx, "admin-one", input, "batch-persist")
	if err != nil || !replayed || replay.Items[0].ID != result.Items[0].ID || replay.Items[1].ID != result.Items[1].ID {
		t.Fatal("restart lost batch idempotency", replayed, err)
	}
	for _, rule := range result.Items {
		if _, err := reopened.CredentialForAdministrator(ctx, "admin-one", rule.ID); err != nil {
			t.Fatal("restart lost a batch identity", err)
		}
	}
	current, _, err := reopened.DesiredNodeConfig(ctx, "node")
	if err != nil || current.Generation != config.Generation || !bytes.Equal(current.Config, config.Config) {
		t.Fatal("replay changed node configuration", err)
	}
	events, err := reopened.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == "forwarding_rule.batch_create" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("batch audit was duplicated", count)
	}
	input.Rules[1].EntryGroupID = "missing-group"
	beforeSnapshot, revision := healthSnapshot(t, reopened)
	if _, _, err := batchCreationPGService(reopened).BatchCreateForAdministrator(ctx, "admin-one", input, "batch-invalid"); err == nil {
		t.Fatal("invalid batch committed")
	}
	after, next := healthSnapshot(t, reopened)
	if next != revision || !bytes.Equal(beforeSnapshot, after) {
		t.Fatal("invalid batch changed durable data")
	}
}

func TestBatchCreatePostgreSQLConcurrentPortReservation(t *testing.T) {
	s, _, _ := protocolHealthFixture(t)
	ctx := context.Background()
	input := batchCreationPGRequest()
	type outcome struct {
		result forwarding.BatchCreateResult
		err    error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	for _, key := range []string{"batch-concurrent-a", "batch-concurrent-b"} {
		go func(key string) {
			<-start
			result, _, err := batchCreationPGService(s).BatchCreateForAdministrator(ctx, "admin-one", input, key)
			done <- outcome{result, err}
		}(key)
	}
	close(start)
	ports := make(map[int]bool)
	for range 2 {
		out := <-done
		if out.err != nil || out.result.Created != 2 {
			t.Fatal("concurrent batch failed", out.err)
		}
		for _, rule := range out.result.Items {
			if ports[rule.ListenPort] || rule.ListenPort == 12000 {
				t.Fatal("batch reused a reserved port")
			}
			ports[rule.ListenPort] = true
		}
	}
}

func TestBatchCreatePostgreSQLCommitFailureDoesNotPublish(t *testing.T) {
	s, _, _ := protocolHealthFixture(t)
	ctx := context.Background()
	input := batchCreationPGRequest()
	before, revision := healthSnapshot(t, s)
	wake := s.DesiredConfigChanges("node")
	defer s.ReleaseDesiredConfigChanges("node", wake)
	if _, err := s.db.ExecContext(ctx, "CREATE FUNCTION reject_batch_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated batch commit rejection'; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER reject_batch_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_batch_commit()"); err != nil {
		t.Fatal(err)
	}
	_, _, err := batchCreationPGService(s).BatchCreateForAdministrator(ctx, "admin-one", input, "batch-commit-retry")
	var failure *snapshotTransactionFailure
	if !errors.As(err, &failure) || failure.stage != "commit" {
		t.Fatal("batch did not report its failed commit", err)
	}
	after, next := healthSnapshot(t, s)
	if next != revision || !bytes.Equal(before, after) {
		t.Fatal("failed batch commit changed durable data")
	}
	select {
	case <-wake:
		t.Fatal("failed batch notified a node")
	default:
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER reject_batch_commit ON nyvp_control_snapshots; DROP FUNCTION reject_batch_commit()"); err != nil {
		t.Fatal(err)
	}
	result, replayed, err := batchCreationPGService(s).BatchCreateForAdministrator(ctx, "admin-one", input, "batch-commit-retry")
	if err != nil || replayed || result.Created != 2 {
		t.Fatal("failed batch could not retry with the same key", replayed, err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed batch did not notify its node")
	}
}
