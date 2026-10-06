package usage

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type enforcementClientStub struct {
	command *agentv1.EnforcementCommand
	results []agentv1.EnforcementResultRequest
}

func (client *enforcementClientStub) DesiredEnforcement(context.Context, string) (*agentv1.EnforcementCommand, error) {
	return client.command, nil
}
func (client *enforcementClientStub) ReportEnforcementResult(_ context.Context, _ string, result agentv1.EnforcementResultRequest) error {
	client.results = append(client.results, result)
	return nil
}

type executorStub struct {
	customerID string
	enabled    bool
	command    agentv1.EnforcementCommand
	err        error
}

func (executor *executorStub) ApplyEnforcement(_ context.Context, command agentv1.EnforcementCommand) error {
	executor.command = command
	executor.customerID = command.CustomerID
	executor.enabled = command.Action == agentv1.EnforcementEnableCustomer
	return executor.err
}

func TestEnforcementReconcilerAppliesDisableAndEnableBeforeSuccess(t *testing.T) {
	for _, action := range []agentv1.EnforcementAction{agentv1.EnforcementDisableCustomer, agentv1.EnforcementEnableCustomer} {
		client := &enforcementClientStub{command: &agentv1.EnforcementCommand{DecisionID: "decision-one", CustomerID: "customer-one", RuleID: "rule-one", Protocol: "tcp", Action: action, Revision: 3}}
		executor := &executorStub{}
		reconciler, err := NewEnforcementReconciler(client, executor, "credential")
		if err != nil {
			t.Fatal(err)
		}
		applied, err := reconciler.ReconcileOnce(context.Background())
		if err != nil || !applied {
			t.Fatalf("ReconcileOnce() = (%v,%v)", applied, err)
		}
		if executor.customerID != "customer-one" || executor.enabled != (action == agentv1.EnforcementEnableCustomer) {
			t.Fatalf("executor = %#v for action %s", executor, action)
		}
		if len(client.results) != 1 || client.results[0].Status != agentv1.EnforcementResultSucceeded || client.results[0].Revision != 3 {
			t.Fatalf("success reported before/without execution: %#v", client.results)
		}
	}
}

func TestEnforcementReconcilerReportsFailureAndNeverClaimsSuccess(t *testing.T) {
	client := &enforcementClientStub{command: &agentv1.EnforcementCommand{DecisionID: "decision-one", CustomerID: "customer-one", RuleID: "rule-one", Protocol: "tcp", Action: agentv1.EnforcementDisableCustomer, Revision: 1}}
	executor := &executorStub{err: errors.New("engine adapter unavailable token=secret")}
	reconciler, err := NewEnforcementReconciler(client, executor, "credential")
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := reconciler.ReconcileOnce(context.Background()); err == nil || !applied {
		t.Fatalf("ReconcileOnce() = (%v,%v), want failed attempt", applied, err)
	}
	if len(client.results) != 1 || client.results[0].Status != agentv1.EnforcementResultFailed || client.results[0].Message == "" {
		t.Fatalf("failure result = %#v", client.results)
	}
	if _, err := NewEnforcementReconciler(client, nil, "credential"); !errors.Is(err, ErrEnforcementExecutorUnavailable) {
		t.Fatalf("nil executor error = %v", err)
	}
}
