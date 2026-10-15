package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestDeletedNodeCannotReceiveConfigurationOrApplyReceipt(t *testing.T) {
	s := New(auth.Administrator{ID: "admin", Username: "admin"})
	now := time.Now().UTC()
	s.nodes["old"] = nodes.Node{ID: "old", DeletedAt: &now, DesiredGeneration: 54, AppliedGeneration: 53}
	s.nodeConfigsByNode["old"] = map[int64]generations.NodeConfigGeneration{54: {NodeID: "old", Generation: 54, ConfigSHA256: "old-hash"}}
	ctx := context.Background()
	if _, _, err := s.DesiredNodeConfig(ctx, "old"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted desired config: %v", err)
	}
	if _, err := s.RecordApplyResult(ctx, generations.ApplyResult{NodeID: "old", Generation: 54, ConfigSHA256: "old-hash"}, audit.Event{}); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted receipt: %v", err)
	}
	if _, created, err := s.compileNodeConfigLocked("old", now); created || err != nil {
		t.Fatalf("deleted compilation: %v %v", created, err)
	}
	if s.nodes["old"].DesiredGeneration != 54 || len(s.nodeConfigsByNode["old"]) != 1 {
		t.Fatal("historical configuration changed")
	}
}
