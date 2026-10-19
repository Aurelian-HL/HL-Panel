package controlclient

import (
	"context"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (c *Client) ClaimNodeUpdate(ctx context.Context, credential string) (*agentv1.NodeUpdateTask, error) {
	body, status, err := c.do(ctx, "POST", "/agent/update-task/claim", credential, nil, maxOrdinaryResponseBody)
	if err != nil {
		return nil, err
	}
	if status == 204 {
		return nil, nil
	}
	if status != 200 {
		return nil, httpError(status, body)
	}
	var task agentv1.NodeUpdateTask
	if err := decodeStrict(body, &task); err != nil {
		return nil, err
	}
	return &task, nil
}
func (c *Client) ReportNodeUpdate(ctx context.Context, credential string, result agentv1.NodeUpdateResult) error {
	body, status, err := c.do(ctx, "POST", "/agent/update-task/result", credential, result, maxOrdinaryResponseBody)
	if err != nil {
		return err
	}
	if status != 204 {
		return httpError(status, body)
	}
	return nil
}
