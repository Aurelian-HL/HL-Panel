package controlclient

import (
	"context"
	"errors"
	"net/http"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (c *Client) ReportApplyResults(ctx context.Context, credential string, results []agentv1.ApplyResultRequest) error {
	if !agentv1.IsSuccessfulApplyPair(results) {
		return errors.New("invalid success receipt pair")
	}
	if !c.applyBatchUnsupported.Load() {
		body, status, err := c.do(ctx, http.MethodPost, "/agent/apply-results/batch", credential,
			agentv1.ApplyResultsRequest{Results: results}, maxOrdinaryResponseBody)
		if err != nil {
			return err
		}
		switch status {
		case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
			return nil
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			c.applyBatchUnsupported.Store(true)
		default:
			return httpError(status, body)
		}
	}
	// A partially delivered legacy pair remains in the durable queue. Replaying
	// its identical first phase is idempotent; only both successes acknowledge it.
	for _, result := range results {
		if err := c.ReportApplyResult(ctx, credential, result); err != nil {
			return err
		}
	}
	return nil
}
