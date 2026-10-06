package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var ErrEnforcementExecutorUnavailable = errors.New("usage enforcement executor is not configured")

type EnforcementClient interface {
	DesiredEnforcement(context.Context, string) (*agentv1.EnforcementCommand, error)
	ReportEnforcementResult(context.Context, string, agentv1.EnforcementResultRequest) error
}

// AccessExecutor must be idempotent. A successful local change can be retried
// when the acknowledgement is lost; only a real engine adapter may implement it.
type AccessExecutor interface {
	ApplyEnforcement(context.Context, agentv1.EnforcementCommand) error
}

type EnforcementReconciler struct {
	client         EnforcementClient
	executor       AccessExecutor
	nodeCredential string
}

func NewEnforcementReconciler(client EnforcementClient, executor AccessExecutor, nodeCredential string) (*EnforcementReconciler, error) {
	if client == nil || strings.TrimSpace(nodeCredential) == "" {
		return nil, errors.New("enforcement client and node credential are required")
	}
	if executor == nil {
		return nil, ErrEnforcementExecutorUnavailable
	}
	return &EnforcementReconciler{client: client, executor: executor, nodeCredential: nodeCredential}, nil
}

func (reconciler *EnforcementReconciler) ReconcileOnce(ctx context.Context) (bool, error) {
	command, err := reconciler.client.DesiredEnforcement(ctx, reconciler.nodeCredential)
	if err != nil || command == nil {
		return false, err
	}
	if command.Action != agentv1.EnforcementDisableCustomer && command.Action != agentv1.EnforcementEnableCustomer {
		return true, errors.New("unsupported enforcement action")
	}
	if command.Protocol == "udp" && command.Limits != (agentv1.EffectiveLimits{}) {
		return true, errors.New("UDP enforcement command contains TCP-only limits")
	}
	applyErr := reconciler.executor.ApplyEnforcement(ctx, *command)
	result := agentv1.EnforcementResultRequest{
		DecisionID: command.DecisionID, Action: command.Action, Revision: command.Revision,
		Status: agentv1.EnforcementResultSucceeded,
	}
	if applyErr != nil {
		result.Status = agentv1.EnforcementResultFailed
		result.Message = sanitizeMessage(applyErr.Error())
	}
	if reportErr := reconciler.client.ReportEnforcementResult(context.WithoutCancel(ctx), reconciler.nodeCredential, result); reportErr != nil {
		if applyErr != nil {
			return true, fmt.Errorf("apply enforcement: %v; report result: %w", applyErr, reportErr)
		}
		return true, fmt.Errorf("report enforcement result: %w", reportErr)
	}
	return true, applyErr
}

func sanitizeMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) <= 512 {
		return message
	}
	message = message[:512]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
