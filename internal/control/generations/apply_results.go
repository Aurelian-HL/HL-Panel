package generations

import (
	"context"
	"errors"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var ErrApplyBatchUnsupported = errors.New("atomic apply receipt batches are unsupported")

func (s *Service) RecordApplyResults(ctx context.Context, nodeID string, requests []agentv1.ApplyResultRequest) (nodes.Node, error) {
	if !agentv1.IsSuccessfulApplyPair(requests) {
		return nodes.Node{}, fmt.Errorf("%w: expected commit and verify success for one attempt", faults.ErrValidation)
	}
	repository, ok := s.repository.(ApplyResultBatchRepository)
	if !ok {
		return nodes.Node{}, ErrApplyBatchUnsupported
	}
	results := make([]ApplyResult, 2)
	events := make([]audit.Event, 2)
	for i, request := range requests {
		var err error
		results[i], events[i], err = s.prepareApplyResult(ctx, nodeID, int64(request.Generation), request.Phase,
			request.Status, request.ConfigSHA256, request.EngineMode, request.Message, request.AttemptID)
		if err != nil {
			return nodes.Node{}, err
		}
	}
	node, err := repository.RecordApplyResults(ctx, results, events)
	// Publication must only follow durable persistence of both phases.
	if err == nil && node.AppliedGeneration >= results[1].Generation && s.onDeployment != nil {
		s.onDeployment(nodeID)
	}
	return node, err
}
