package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func applyPairFixture() (*Store, []agentv1.ApplyResultRequest) {
	s := New(auth.Administrator{})
	s.nodes["node"] = nodes.Node{ID: "node", DesiredGeneration: 2}
	hash := strings.Repeat("a", 64)
	s.nodeConfigsByNode["node"] = map[int64]generations.NodeConfigGeneration{
		2: {NodeID: "node", Generation: 2, ConfigSHA256: hash, Config: []byte(`{"schema_version":1,"fragments":[]}`)},
	}
	commit := agentv1.ApplyResultRequest{Generation: 2, AttemptID: strings.Repeat("b", 32),
		ConfigSHA256: hash, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray"}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	return s, []agentv1.ApplyResultRequest{commit, verify}
}

func TestApplyReceiptBatchUDPCompileFailureRollsBackAndRetries(t *testing.T) {
	s, _, req, _ := reliableUDPFixture(t, forwarding.TunnelKCP)
	node := s.nodes["exit-node"]
	config := s.nodeConfigsByNode[node.ID][node.DesiredGeneration]
	commit := agentv1.ApplyResultRequest{Generation: agentv1.NodeConfigGeneration(config.Generation),
		ConfigSHA256: config.ConfigSHA256, AttemptID: applyAttemptA, Phase: agentv1.ApplyPhaseCommit,
		Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost"}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	pair := []agentv1.ApplyResultRequest{commit, verify}
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	previous, existed := s.revisionsByGroup[req.EntryGroupID]
	s.revisionsByGroup[req.EntryGroupID] = []generations.GroupRevision{{GroupID: req.EntryGroupID,
		Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	notified := 0
	svc := generations.NewService(s, time.Now, generations.WithDeploymentNotification(func(string) { notified++ }))
	if _, err := svc.RecordApplyResults(context.Background(), node.ID, pair); err == nil {
		t.Fatal("invalid dependent configuration accepted")
	}
	if existed {
		s.revisionsByGroup[req.EntryGroupID] = previous
	} else {
		delete(s.revisionsByGroup, req.EntryGroupID)
	}
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	assertUDPSnapshotUnchanged(t, before, after)
	if notified != 0 {
		t.Fatal("failed batch notified deployment")
	}
	if _, err := svc.RecordApplyResults(context.Background(), node.ID, pair); err != nil {
		t.Fatal("batch retry failed", err)
	}
	if s.nodes["entry-node"].DesiredGeneration == 0 || notified != 1 {
		t.Fatal("retry did not publish dependent UDP ingress")
	}
}

func TestApplyReceiptBatchPersistsBothPhasesAndReplays(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "commit-already-recorded"}[partial], func(t *testing.T) {
			s, pair := applyPairFixture()
			notified := 0
			svc := generations.NewService(s, time.Now, generations.WithDeploymentNotification(func(id string) {
				if id != "node" || s.nodes[id].AppliedGeneration != 2 {
					t.Fatal("notification before both phases")
				}
				notified++
			}))
			if partial {
				r := pair[0]
				if _, err := svc.RecordApplyResult(context.Background(), "node", int64(r.Generation), r.Phase, r.Status, r.ConfigSHA256, r.EngineMode, r.Message, r.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				node, err := svc.RecordApplyResults(context.Background(), "node", pair)
				if err != nil || node.AppliedGeneration != 2 {
					t.Fatalf("apply: %+v %v", node, err)
				}
			}
			if len(s.applyResultsByNode["node"][2]) != 2 || len(s.auditEvents) != 2 || notified != 2 {
				t.Fatal("lost evidence or duplicate audit")
			}
		})
	}
}

func TestApplyReceiptBatchFailureRestoresAllState(t *testing.T) {
	for _, name := range []string{"conflicting-verify", "stale-attempt", "hash", "deleted-node", "canceled", "malformed", "invalid-hash", "invalid-attempt", "invalid-mode"} {
		t.Run(name, func(t *testing.T) {
			s, pair := applyPairFixture()
			ctx := context.Background()
			want := faults.ErrConflict
			switch name {
			case "conflicting-verify":
				s.applyAttemptsByNode["node"] = map[int64]generations.ApplyAttemptState{2: {CurrentID: pair[0].AttemptID, SeenIDs: map[string]bool{pair[0].AttemptID: true}}}
				s.applyResultsByNode["node"] = map[int64]map[string]generations.ApplyResult{2: {"verify": {NodeID: "node", Generation: 2, AttemptID: pair[0].AttemptID, Phase: agentv1.ApplyPhaseVerify, Status: agentv1.ApplyStatusSucceeded, ConfigSHA256: pair[0].ConfigSHA256, EngineMode: "xray", Message: "conflict"}}}
			case "stale-attempt":
				s.applyAttemptsByNode["node"] = map[int64]generations.ApplyAttemptState{2: {CurrentID: strings.Repeat("c", 32), SeenIDs: map[string]bool{pair[0].AttemptID: true}}}
			case "hash":
				pair[0].ConfigSHA256 = strings.Repeat("c", 64)
				pair[1].ConfigSHA256 = pair[0].ConfigSHA256
			case "deleted-node":
				n := s.nodes["node"]
				now := time.Now()
				n.DeletedAt = &now
				s.nodes["node"] = n
				want = faults.ErrNotFound
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "malformed":
				pair[1].AttemptID = strings.Repeat("c", 32)
				want = faults.ErrValidation
			case "invalid-hash":
				pair[0].ConfigSHA256 = "bad"
				pair[1].ConfigSHA256 = "bad"
				want = faults.ErrValidation
			case "invalid-attempt":
				pair[0].AttemptID = "bad"
				pair[1].AttemptID = "bad"
				want = faults.ErrValidation
			case "invalid-mode":
				pair[0].EngineMode = "bad"
				pair[1].EngineMode = "bad"
				want = faults.ErrValidation
			}
			before, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			notified := false
			_, err = generations.NewService(s, time.Now, generations.WithDeploymentNotification(func(string) { notified = true })).RecordApplyResults(ctx, "node", pair)
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want %v", err, want)
			}
			after, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || notified {
				t.Fatal("failed batch changed state or notified deployment")
			}
		})
	}
}
