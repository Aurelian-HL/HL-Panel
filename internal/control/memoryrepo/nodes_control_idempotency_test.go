package memoryrepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestRequestControlRejectsReusedKeyWithDifferentCommand(t *testing.T) {
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	store := New(auth.Administrator{ID: "admin", Username: "admin", CreatedAt: now})
	store.nodes["node-1"] = nodes.Node{ID: "node-1", CreatedAt: now, UpdatedAt: now}
	ctx := context.Background()
	first := nodes.ControlCommandInput{
		NodeID: "node-1", Command: "status", AdministratorID: "admin", IdempotencyKey: "same-key",
		RequestSHA256: "hash-status", CommandID: "control-1", UpdatedAt: now,
	}
	if _, replayed, err := store.RequestControl(ctx, first, audit.Event{ID: "first"}); err != nil || replayed {
		t.Fatalf("first request failed: replayed=%v err=%v", replayed, err)
	}
	second := first
	second.Command = "restart"
	second.RequestSHA256 = "hash-restart"
	second.CommandID = "control-2"
	second.UpdatedAt = now.Add(time.Second)
	if _, replayed, err := store.RequestControl(ctx, second, audit.Event{ID: "second"}); !errors.Is(err, faults.ErrIdempotencyConflict) || replayed {
		t.Fatalf("expected idempotency conflict, replayed=%v err=%v", replayed, err)
	}
	result, err := store.ControlForNode(ctx, "node-1")
	if err != nil || result.Command != "status" || result.CommandID != "control-1" {
		t.Fatalf("original command was replaced: result=%+v err=%v", result, err)
	}
	if got := len(store.AuditEvents()); got != 1 {
		t.Fatalf("conflicting request appended an audit event: got %d", got)
	}
	if _, replayed, err := store.RequestControl(ctx, first, audit.Event{ID: "replay"}); err != nil || !replayed {
		t.Fatalf("same request was not replayed: replayed=%v err=%v", replayed, err)
	}
}
