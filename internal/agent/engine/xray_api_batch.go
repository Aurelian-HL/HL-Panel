package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Bound each CLI/RPC transaction. One large change must not put thousands of
// handlers in a single RPC with an unknowable partially applied outcome.
const xrayHandlerBatchSize = 64

// Carry only diagnostics constructed by our runner. Other runners may return
// arbitrary stderr/configuration in err.Error(); never expose that text.
type xrayCommandError struct {
	operation, reason string
	cause             error
}

func (e *xrayCommandError) Error() string {
	return e.operation + " " + e.reason + ": " + e.cause.Error()
}
func (e *xrayCommandError) Unwrap() error { return e.cause }
func safeXrayAPIError(err error) error {
	var known *xrayCommandError
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, errXrayAPIUncertain) {
		return errXrayAPIUncertain
	}
	return ErrXrayLiveUpdate
}

func xrayHandlerBatches(ctx context.Context, api xrayAPIRunner, address, operation, key string, entries []json.RawMessage) error {
	for start := 0; start < len(entries); start += xrayHandlerBatchSize {
		end := min(start+xrayHandlerBatchSize, len(entries))
		if _, err := api.API(ctx, address, operation, xrayAPIDocument(key, entries[start:end])); err != nil {
			// Only stage and counts are exposed; document contents and tags can
			// contain customer credentials or accounting identifiers.
			return fmt.Errorf("%s handlers %d-%d/%d: %w", operation, start+1, end, len(entries), safeXrayAPIError(err))
		}
	}
	return nil
}

func xrayRoutingCall(ctx context.Context, api xrayAPIRunner, address string, routing json.RawMessage) error {
	_, err := api.API(ctx, address, "adrules", xrayAPIDocument("routing", routing))
	if err != nil {
		return fmt.Errorf("adrules: %w", safeXrayAPIError(err))
	}
	return nil
}
