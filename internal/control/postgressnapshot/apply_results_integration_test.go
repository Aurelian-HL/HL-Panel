package postgressnapshot

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestApplyReceiptBatchTransactionCost(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "two-single-transactions", true: "one-batch-transaction"}[batch], func(t *testing.T) {
			s, _, proof := protocolHealthFixture(t)
			ctx := context.Background()
			svc := generations.NewService(s, time.Now)
			const iterations = 20
			durations := make([]time.Duration, 0, iterations)
			_, revision := healthSnapshot(t, s)
			for i := 0; i < iterations; i++ {
				commit := agentv1.ApplyResultRequest{Generation: agentv1.NodeConfigGeneration(proof.NodeConfigGeneration),
					AttemptID: fmt.Sprintf("%032x", i+1), ConfigSHA256: proof.ConfigSHA256,
					Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray"}
				verify := commit
				verify.Phase = agentv1.ApplyPhaseVerify
				started := time.Now()
				if batch {
					if _, err := svc.RecordApplyResults(ctx, proof.NodeID, []agentv1.ApplyResultRequest{commit, verify}); err != nil {
						t.Fatal(err)
					}
				} else {
					for _, request := range []agentv1.ApplyResultRequest{commit, verify} {
						if _, err := svc.RecordApplyResult(ctx, proof.NodeID, int64(request.Generation), request.Phase,
							request.Status, request.ConfigSHA256, request.EngineMode, request.Message, request.AttemptID); err != nil {
							t.Fatal(err)
						}
					}
				}
				durations = append(durations, time.Since(started))
			}
			_, next := healthSnapshot(t, s)
			writes := int64(iterations * 2)
			if batch {
				writes = iterations
			}
			if next != revision+writes {
				t.Fatalf("snapshot writes=%d want=%d", next-revision, writes)
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			t.Logf("isolated PostgreSQL iterations=%d snapshot_writes=%d median=%s min=%s max=%s", iterations,
				writes, durations[len(durations)/2], durations[0], durations[len(durations)-1])
		})
	}
}

func TestApplyReceiptBatchSingleTransactionAndDurableEvidence(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable-success", true: "commit-failure"}[failure], func(t *testing.T) {
			s, dsn, proof := protocolHealthFixture(t)
			ctx := context.Background()
			commit := agentv1.ApplyResultRequest{Generation: agentv1.NodeConfigGeneration(proof.NodeConfigGeneration), AttemptID: strings.Repeat("f", 32), ConfigSHA256: proof.ConfigSHA256, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray"}
			verify := commit
			verify.Phase = agentv1.ApplyPhaseVerify
			before, revision := healthSnapshot(t, s)
			audits, err := s.AuditEvents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			notified := 0
			svc := generations.NewService(s, time.Now, generations.WithDeploymentNotification(func(string) { notified++ }))
			if failure {
				_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_apply_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated apply commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_apply_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_apply_commit()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = svc.RecordApplyResults(ctx, proof.NodeID, []agentv1.ApplyResultRequest{commit, verify})
			after, next := healthSnapshot(t, s)
			if failure {
				if err == nil || notified != 0 || !bytes.Equal(before, after) || next != revision {
					t.Fatal("failed commit changed snapshot or notified", err)
				}
			} else if err != nil || notified != 1 || next != revision+1 {
				t.Fatal("pair was not committed once", err)
			}
			reopened, err := Open(ctx, dsn, bootstrapAdministrator(t))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			persisted, err := reopened.AuditEvents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := len(audits)
			if !failure {
				want += 2
			}
			if len(persisted) != want {
				t.Fatal("audit transaction mismatch")
			}
			input, err := reopened.RuleNodeDeploymentInput(ctx, proof.RuleID, proof.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			wantAttempt := "0123456789abcdef0123456789abcdef"
			if !failure {
				wantAttempt = commit.AttemptID
			}
			for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
				if input.ApplyResults[phase].AttemptID != wantAttempt {
					t.Fatal("cold reopen lost evidence or exposed failed commit")
				}
			}
		})
	}
}
