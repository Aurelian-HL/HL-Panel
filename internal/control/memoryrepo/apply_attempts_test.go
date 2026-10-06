package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	applyAttemptA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	applyAttemptB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	applyAttemptC = "cccccccccccccccccccccccccccccccc"
)

func TestApplyAttemptReplacesOldEvidenceAndRejectsDelayedResults(t *testing.T) {
	s := New(auth.Administrator{})
	s.nodes["node-one"] = nodes.Node{ID: "node-one", DesiredGeneration: 1}
	s.nodeConfigsByNode["node-one"] = map[int64]generations.NodeConfigGeneration{
		1: {NodeID: "node-one", Generation: 1, ConfigSHA256: "hash-one"},
	}
	now := time.Now().UTC()
	record := func(id string, phase agentv1.ApplyPhase, status agentv1.ApplyStatus) error {
		_, err := s.RecordApplyResult(context.Background(), generations.ApplyResult{
			NodeID: "node-one", Generation: 1, AttemptID: id, Phase: phase, Status: status,
			ConfigSHA256: "hash-one", EngineMode: "xray", CreatedAt: now,
		}, audit.Event{ID: "event-one", CreatedAt: now})
		return err
	}
	if err := record(applyAttemptA, agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := record(applyAttemptA, agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if s.nodes["node-one"].AppliedGeneration != 1 {
		t.Fatal("first attempt did not advance applied generation")
	}
	if err := record(applyAttemptB, agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if len(s.applyResultsByNode["node-one"][1]) != 1 || s.applyResultsByNode["node-one"][1]["commit"].AttemptID != "" {
		t.Fatal("new attempt retained old commit")
	}
	if err := record(applyAttemptA, agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("delayed old attempt = %v", err)
	}
	if err := record(applyAttemptB, agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded); err != nil {
		t.Fatalf("same-attempt out-of-order commit = %v", err)
	}
	if err := record(applyAttemptB, agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded); err != nil {
		t.Fatalf("idempotent replay = %v", err)
	}
	if err := record(applyAttemptB, agentv1.ApplyPhaseRollback, agentv1.ApplyStatusRolledBack); err != nil {
		t.Fatalf("rollback after verify = %v", err)
	}
	if !s.applyAttemptsByNode["node-one"][1].Invalidated {
		t.Fatal("rollback did not invalidate current receipt")
	}
	if err := record(applyAttemptC, agentv1.ApplyPhasePrepare, agentv1.ApplyStatusFailed); err != nil {
		t.Fatal(err)
	}
	if len(s.applyResultsByNode["node-one"][1]) != 1 || s.applyAttemptsByNode["node-one"][1].CurrentID != applyAttemptC {
		t.Fatal("failed new attempt retained old success")
	}
	if err := record(applyAttemptB, agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("superseded attempt = %v", err)
	}
}

func TestApplyAttemptSnapshotRoundTripAndLegacyUpgrade(t *testing.T) {
	s := snapshotFixture(t)
	s.applyResultsByNode["node-1"][1]["commit"] = generations.ApplyResult{
		NodeID: "node-1", Generation: 1, AttemptID: applyAttemptB, Phase: agentv1.ApplyPhaseCommit,
	}
	s.applyAttemptsByNode["node-1"] = map[int64]generations.ApplyAttemptState{
		1: {CurrentID: applyAttemptB, SeenIDs: map[string]bool{applyAttemptA: true, applyAttemptB: true}},
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	attempt := restored.applyAttemptsByNode["node-1"][1]
	if attempt.CurrentID != applyAttemptB || !attempt.SeenIDs[applyAttemptA] || !attempt.SeenIDs[applyAttemptB] {
		t.Fatalf("attempt ledger not restored: %#v", attempt)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["Version"] = json.RawMessage("8")
	delete(legacy, "ApplyAttemptsByNode")
	delete(legacy, "ApplyResultsByNode")
	// Version 8 requires the old result index, but not the new attempt index.
	legacy["ApplyResultsByNode"] = json.RawMessage(`{}`)
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := DecodeSnapshot(legacyRaw)
	if err != nil || len(upgraded.applyAttemptsByNode) != 0 {
		t.Fatalf("legacy snapshot upgrade = %v, %#v", err, upgraded)
	}
}

func TestSnapshotRejectsBrokenApplyAttemptLedger(t *testing.T) {
	s := snapshotFixture(t)
	s.applyAttemptsByNode["node-1"] = map[int64]generations.ApplyAttemptState{
		1: {CurrentID: applyAttemptA, SeenIDs: map[string]bool{applyAttemptA: true}},
	}
	s.applyResultsByNode["node-1"][1]["commit"] = generations.ApplyResult{
		NodeID: "node-1", Generation: 1, AttemptID: applyAttemptA, Phase: agentv1.ApplyPhaseCommit,
	}
	check := func(name string, change func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			change()
			raw, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(raw); err == nil {
				t.Fatal("broken apply attempt ledger was accepted")
			}
		})
	}
	check("missing current ID", func() {
		s.applyAttemptsByNode["node-1"][1] = generations.ApplyAttemptState{CurrentID: applyAttemptB,
			SeenIDs: map[string]bool{applyAttemptA: true}}
	})
	check("mismatched result", func() {
		s.applyAttemptsByNode["node-1"][1] = generations.ApplyAttemptState{CurrentID: applyAttemptB,
			SeenIDs: map[string]bool{applyAttemptA: true, applyAttemptB: true}}
	})
	check("invalid current ID", func() {
		s.applyAttemptsByNode["node-1"][1] = generations.ApplyAttemptState{CurrentID: "not-an-id",
			SeenIDs: map[string]bool{"not-an-id": true}}
	})
}
