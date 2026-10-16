package gatewaymembership

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/protocolprobe"
)

func TestObserverRequiresFlowMatchingDeployedRule(t *testing.T) {
	input := schedulingInput("empty-flow", 1)
	input.State.Rule.VLESSFlow, input.State.Rule.VLESSFlowExplicit = "", true
	member := input.State.Members[0]
	spec := protocolprobe.Spec{
		UUID: input.Config.UUID, EchoHost: "127.0.0.1", EchoPort: input.Config.EchoPort,
		DialHost: member.DialHost, DialPort: input.State.Rule.ListenPort,
	}
	observer := NewProtocolObserver(nil, time.Now)
	calls := 0
	observer.check = func(_ context.Context, s protocolprobe.Spec) (time.Time, error) {
		calls++
		if !s.FlowExplicit || s.EffectiveFlow() != "" {
			t.Fatal("wrong flow reached challenge")
		}
		return time.Now().UTC(), nil
	}
	if _, err := observer.observeState(context.Background(), input.State, input.Config, member.NodeID, spec); !errors.Is(err, ErrProtocolObservationTarget) || calls != 0 {
		t.Fatal("Vision probe could authorize empty-flow rule")
	}
	spec.FlowExplicit = true
	proof, err := observer.observeState(context.Background(), input.State, input.Config, member.NodeID, spec)
	if err != nil || proof.RuleID != input.State.Rule.ID || calls != 1 {
		t.Fatalf("matching empty flow rejected: %v", err)
	}
}
