package memoryrepo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestProbeBatchIsAtomicAndRetryDoesNotRotate(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	s := f.store
	delete(s.protocolProbes, f.rule.ID)
	second := s.forwardRules[f.rule.ID]
	second.ID = "second-rule"
	second.ListenPort++
	s.forwardRules[second.ID] = second
	a := gatewaymembership.ProtocolProbeConfig{UUID: "aaaaaaaa-0000-4000-8000-000000000001", EchoPort: 39000}
	b := gatewaymembership.ProtocolProbeConfig{UUID: "aaaaaaaa-0000-4000-8000-000000000002", EchoPort: 39000}
	before := s.nodes[f.nodeID].DesiredGeneration
	if err := s.ConfigureProtocolProbes(context.Background(), map[string]gatewaymembership.ProtocolProbeConfig{f.rule.ID: a, second.ID: a}); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("duplicate accepted", err)
	}
	if len(s.protocolProbes) != 0 || s.nodes[f.nodeID].DesiredGeneration != before {
		t.Fatal("rejected batch changed state")
	}
	if err := s.ConfigureProtocolProbes(context.Background(), map[string]gatewaymembership.ProtocolProbeConfig{f.rule.ID: a, second.ID: b}); err != nil {
		t.Fatal(err)
	}
	if s.nodes[f.nodeID].DesiredGeneration != before+1 {
		t.Fatal("batch compiled node more than once")
	}
	after, events := s.nodes[f.nodeID].DesiredGeneration, len(s.auditEvents)
	if err := s.ConfigureProtocolProbes(context.Background(), map[string]gatewaymembership.ProtocolProbeConfig{f.rule.ID: b, second.ID: a}); err != nil {
		t.Fatal(err)
	}
	if s.protocolProbes[f.rule.ID] != a || s.protocolProbes[second.ID] != b || s.nodes[f.nodeID].DesiredGeneration != after || len(s.auditEvents) != events {
		t.Fatal("retry rotated identity or redeployed")
	}
}

func TestProbeBatchRollsBackCompileFailure(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	s := f.store
	delete(s.protocolProbes, f.rule.ID)
	s.revisionsByGroup[f.rule.EntryGroupID] = []generations.GroupRevision{{GroupID: f.rule.EntryGroupID, Revision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{broken`)}}
	before, _ := s.EncodeSnapshot()
	err := s.ConfigureProtocolProbes(context.Background(), map[string]gatewaymembership.ProtocolProbeConfig{f.rule.ID: {UUID: "aaaaaaaa-0000-4000-8000-000000000001", EchoPort: 39000}})
	if err == nil {
		t.Fatal("invalid material compiled")
	}
	after, _ := s.EncodeSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("compile failure changed persistent state")
	}
}
