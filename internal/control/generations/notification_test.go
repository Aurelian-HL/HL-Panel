package generations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type receiptRepository struct {
	Repository
	applied int64
	err     error
}

func (r receiptRepository) RecordApplyResult(context.Context, ApplyResult, audit.Event) (nodes.Node, error) {
	return nodes.Node{ID: "node", AppliedGeneration: r.applied}, r.err
}

func TestDeploymentNotificationRequiresDurableVerifiedGeneration(t *testing.T) {
	for _, test := range []struct {
		name    string
		phase   agentv1.ApplyPhase
		status  agentv1.ApplyStatus
		applied int64
		err     error
		notify  bool
	}{
		{"verified", agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded, 2, nil, true},
		{"commit", agentv1.ApplyPhaseCommit, agentv1.ApplyStatusSucceeded, 2, nil, false},
		{"failed", agentv1.ApplyPhaseVerify, agentv1.ApplyStatusFailed, 2, nil, false},
		{"unapplied", agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded, 1, nil, false},
		{"transaction-failed", agentv1.ApplyPhaseVerify, agentv1.ApplyStatusSucceeded, 2, errors.New("commit failed"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			s := NewService(receiptRepository{applied: test.applied, err: test.err}, nil, WithDeploymentNotification(func(id string) { called = id == "node" }))
			_, _ = s.RecordApplyResult(context.Background(), "node", 2, test.phase, test.status, strings.Repeat("a", 64), "", "", "")
			if called != test.notify {
				t.Fatalf("notified=%v want=%v", called, test.notify)
			}
		})
	}
}

func TestChangeSignalsCoalesceAndRegisterNextGeneration(t *testing.T) {
	var signals ChangeSignals
	a, b := signals.ForNode("one"), signals.ForNode("two")
	if signals.ForNode("one") != a {
		t.Fatal("subscribers did not share signal")
	}
	signals.Notify("one")
	signals.Notify("one")
	select {
	case <-a:
	default:
		t.Fatal("signal lost")
	}
	select {
	case <-b:
		t.Fatal("unrelated node woke")
	default:
	}
	next := signals.ForNode("one")
	select {
	case <-next:
		t.Fatal("next generation inherited old signal")
	default:
	}
	signals.Notify("one")
	select {
	case <-next:
	default:
		t.Fatal("next generation signal lost")
	}
}
