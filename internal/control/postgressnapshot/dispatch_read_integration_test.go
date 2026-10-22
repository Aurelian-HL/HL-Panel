package postgressnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestDispatchConfigurationReadsDoNotLoadRuntimeOverlays(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	want, err := s.ProtocolProbeConfig(ctx, proof.RuleID)
	if err != nil || want.UUID == "" {
		t.Fatal("missing fixture probe identity", err)
	}
	_, applied, desiredErr := s.DesiredNodeConfig(ctx, proof.NodeID)
	if desiredErr != nil && !errors.Is(desiredErr, faults.ErrNotFound) {
		t.Fatal(desiredErr)
	}
	// Fault injection stays inside the unique schema created by the fixture.
	// Forwarding readiness must still require overlays, while internal config
	// and identity reads do not depend on counters or protocol health tables.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE hl_panel_projection_revisions RENAME TO dispatch_test_unavailable`); err != nil {
		t.Fatal(err)
	}
	if _, actualApplied, err := s.DesiredNodeConfig(ctx, proof.NodeID); !errors.Is(err, desiredErr) || actualApplied != applied {
		t.Fatal("desired configuration depends on runtime overlays", err)
	}
	if got, err := s.ProtocolProbeConfig(ctx, proof.RuleID); err != nil || got != want {
		t.Fatal("probe identity depends on runtime overlays", err)
	}
	if _, err := s.ControlForNode(ctx, proof.NodeID); err != nil {
		t.Fatal("control command depends on runtime overlays", err)
	}
	if _, err := s.ListNezhaBindings(ctx); err != nil {
		t.Fatal("node bindings depend on runtime overlays", err)
	}
	if _, err := s.Overview(ctx, time.Now(), time.Minute); err != nil {
		t.Fatal("overview depends on unrelated runtime overlays", err)
	}
	if _, err := s.RuleNodeDeploymentInput(ctx, proof.RuleID, proof.NodeID); err == nil {
		t.Fatal("deployment input silently skipped required overlays")
	}
	if _, err := s.ForwardingRule(ctx, proof.RuleID); err == nil {
		t.Fatal("public readiness silently skipped required overlays")
	}
}

func TestDispatchProbeReadTracksRepairDeletionAndDatabaseFailure(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	before, err := s.ProtocolProbeConfig(ctx, proof.RuleID)
	if err != nil || before.UUID == "" {
		t.Fatal("missing fixture probe identity", err)
	}
	raw, _ := healthSnapshot(t, s)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var probes map[string]gatewaymembership.ProtocolProbeConfig
	if err := json.Unmarshal(doc["ProtocolProbes"], &probes); err != nil {
		t.Fatal(err)
	}
	next := before
	next.UUID = "aaaaaaaa-0000-4000-8000-000000000042"
	probes[proof.RuleID] = next
	replace := func() {
		t.Helper()
		encoded, err := json.Marshal(probes)
		if err != nil {
			t.Fatal(err)
		}
		doc["ProtocolProbes"] = encoded
		payload, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		// No revision bump: xmin must still invalidate confidential cached data.
		if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, payload); err != nil {
			t.Fatal(err)
		}
	}
	replace()
	if got, err := s.ProtocolProbeConfig(ctx, proof.RuleID); err != nil || got != next {
		t.Fatal("same-revision probe identity repair not observed", err)
	}
	delete(probes, proof.RuleID)
	replace()
	if _, err := s.ProtocolProbeConfig(ctx, proof.RuleID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted probe secret served from cache", err)
	}
	probes[proof.RuleID] = next
	replace()
	if _, err := s.ProtocolProbeConfig(ctx, proof.RuleID); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ProtocolProbeConfig(canceled, proof.RuleID); err == nil {
		t.Fatal("canceled request served cached probe secret")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, []byte(`{"Version":24}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProtocolProbeConfig(ctx, proof.RuleID); err == nil || errors.Is(err, faults.ErrNotFound) {
		t.Fatal("corrupt authoritative snapshot served cached probe secret", err)
	}
	s.Close()
	if _, err := s.ProtocolProbeConfig(ctx, proof.RuleID); err == nil {
		t.Fatal("unavailable database served cached probe secret")
	}
}

func TestDispatchConfigurationReadIncrementalCost(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	// Warm both paths. The comparison isolates this batch's overlay removal;
	// both versions already validate revision/xmin and reuse a detached view.
	read := func(overlays bool) error {
		_, err := readForwardingView(ctx, s, overlays, func(v *memoryrepo.ForwardingReadView) (gatewaymembership.ProtocolProbeConfig, error) {
			return v.ProtocolProbeConfig(ctx, proof.RuleID)
		})
		return err
	}
	for _, overlays := range []bool{true, false} {
		if err := read(overlays); err != nil {
			t.Fatal(err)
		}
	}
	for _, overlays := range []bool{true, false} {
		started := time.Now()
		for i := 0; i < 100; i++ {
			if err := read(overlays); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("overlays=%v iterations=100 mean=%s", overlays, time.Since(started)/100)
	}
}
