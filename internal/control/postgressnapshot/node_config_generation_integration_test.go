package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestNodeConfigGenerationSurvivesRestoredCounter(t *testing.T) {
	for _, retainedHot := range []bool{true, false} {
		name := "archive-only"
		if retainedHot {
			name = "retained-hot"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := nodeHistoryFixture(t)
			ctx := context.Background()
			firstResult, err := s.CreateGroupRevision(ctx, historyRevision(1), audit.Event{})
			if err != nil {
				t.Fatal(err)
			}
			first := firstResult.Assignments[0]
			receipt := generations.ApplyResult{ID: "first-commit", NodeID: first.NodeID, Generation: first.Generation, ConfigSHA256: first.ConfigSHA256, Phase: "commit", Status: "succeeded", CreatedAt: time.Now().UTC()}
			if _, err = s.RecordApplyResult(ctx, receipt, audit.Event{}); err != nil {
				t.Fatal(err)
			}
			receipt.ID, receipt.Phase = "first-verify", "verify"
			if _, err = s.RecordApplyResult(ctx, receipt, audit.Event{}); err != nil {
				t.Fatal(err)
			}
			secondResult, err := s.CreateGroupRevision(ctx, historyRevision(2), audit.Event{})
			if err != nil {
				t.Fatal(err)
			}
			second := secondResult.Assignments[0]

			// Reproduce the old recovery boundary: restore the desired counter,
			// retaining the later configuration either hot or already archived.
			// Only this test's synthetic schema is edited.
			tx, err := s.db.BeginTx(ctx, nil)
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
			var records map[string]map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(fields["Nodes"], &records); err != nil {
				t.Fatal(err)
			}
			var node nodes.Node
			if err = json.Unmarshal(records[first.NodeID]["Node"], &node); err != nil {
				t.Fatal(err)
			}
			node.DesiredGeneration = first.Generation
			records[first.NodeID]["Node"], _ = json.Marshal(node)
			fields["Nodes"], _ = json.Marshal(records)
			configs := map[int64]generations.NodeConfigGeneration{first.Generation: first}
			if retainedHot {
				configs[second.Generation] = second
			} else if err = persistNodeConfigHistory(ctx, tx, []memoryrepo.NodeConfigHistory{{Config: second}}); err != nil {
				t.Fatal(err)
			}
			fields["NodeConfigsByNode"], _ = json.Marshal(map[string]map[int64]generations.NodeConfigGeneration{first.NodeID: configs})
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = memoryrepo.DecodeSnapshot(raw); err != nil {
				t.Fatal("invalid recovery fixture", err)
			}
			if _, err = tx.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1 WHERE singleton=true`, raw); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}

			thirdResult, err := s.CreateGroupRevision(ctx, historyRevision(3), audit.Event{})
			if err != nil || len(thirdResult.Assignments) != 1 {
				t.Fatal("compilation after recovery failed", err)
			}
			third := thirdResult.Assignments[0]
			if third.Generation != second.Generation+1 || third.ConfigSHA256 == second.ConfigSHA256 {
				t.Fatal("restored counter reused a configuration generation", third.Generation)
			}
			old, err := s.UsageConfigInput(ctx, second.NodeID, second.Generation)
			if err != nil || !reflect.DeepEqual(old.Config, second) {
				t.Fatal("later write overwrote retained history", err)
			}
			previous, err := s.UsageConfigInput(ctx, first.NodeID, first.Generation)
			if err != nil || !reflect.DeepEqual(previous.Config, first) || len(previous.ApplyResults) != 2 {
				t.Fatal("later write lost the applied generation or receipt", err)
			}
			replay, err := s.CreateGroupRevision(ctx, historyRevision(3), audit.Event{})
			if err != nil || !replay.Replayed || thirdResult.Replayed ||
				!reflect.DeepEqual(replay.Generation, thirdResult.Generation) ||
				!reflect.DeepEqual(replay.Assignments, thirdResult.Assignments) {
				t.Fatal("idempotent replay changed assignments", err)
			}
			tx, err = s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			joined, err := readState(ctx, tx, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = joinNodeConfigHistory(ctx, tx, joined); err != nil {
				t.Fatal("history export now conflicts with current generation", err)
			}
			t.Logf("desired counter restored to %d; retained generation %d; next generation %d; previous receipt preserved", first.Generation, second.Generation, third.Generation)
		})
	}
}

func TestNodeConfigGenerationFloorFailureRollsBack(t *testing.T) {
	s, _ := nodeHistoryFixture(t)
	ctx := context.Background()
	if _, err := s.CreateGroupRevision(ctx, historyRevision(1), audit.Event{}); err != nil {
		t.Fatal(err)
	}
	var before, after []byte
	var revisionBefore, revisionAfter int64
	if err := s.db.QueryRowContext(ctx, `SELECT payload,revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&before, &revisionBefore); err != nil {
		t.Fatal(err)
	}
	want := errors.New("isolated generation floor read failure")
	_, err := transact(ctx, s, true, func(state *memoryrepo.Store) (generations.CreateGroupRevisionResult, error) {
		state.SetNodeConfigGenerationFloorReader(func(string) (int64, error) { return 0, want })
		return state.CreateGroupRevision(ctx, historyRevision(2), audit.Event{})
	})
	if !errors.Is(err, want) {
		t.Fatal("generation floor error did not reject write", err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT payload,revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&after, &revisionAfter); err != nil {
		t.Fatal(err)
	}
	if revisionBefore != revisionAfter || !bytes.Equal(before, after) {
		t.Fatal("failed generation allocation committed state")
	}
	if _, err = s.CreateGroupRevision(ctx, historyRevision(2), audit.Event{}); err != nil {
		t.Fatal("retry after floor failure did not recover", err)
	}
}
