package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type partialBatchRunner struct {
	updateAPIRunner
	addCalls int
	failed   bool
}

func (r *partialBatchRunner) API(ctx context.Context, address, operation string, raw []byte) ([]byte, error) {
	if operation != "lsi" && operation != "lso" && operation != "adrules" {
		var doc map[string][]json.RawMessage
		_ = json.Unmarshal(raw, &doc)
		for _, entries := range doc {
			if len(entries) > xrayHandlerBatchSize {
				return nil, errors.New("unbounded batch")
			}
		}
	}
	result, err := r.updateAPIRunner.API(ctx, address, operation, raw)
	if operation == "ado" {
		r.addCalls++
		if r.addCalls == 2 && !r.failed {
			r.failed = true
			return nil, ErrXrayLiveUpdate
		}
	}
	return result, err
}

func TestXrayBatchedPartialAdditionRestoresOnlyInstalledHandlers(t *testing.T) {
	r := &partialBatchRunner{updateAPIRunner: updateAPIRunner{in: map[string]json.RawMessage{}, out: map[string]json.RawMessage{}}}
	a := newTestXrayAdapter(t, r, true)
	entries := make([]json.RawMessage, 130)
	for j := range entries {
		entries[j] = json.RawMessage(fmt.Sprintf(`{"tag":"new-%d","protocol":"freedom","settings":{}}`, j))
	}
	delta := xrayLiveChanges{outbounds: xrayHandlerChanges{added: entries}}
	var progress xrayMutationProgress
	err := a.applyXrayLiveChanges(context.Background(), r, delta, &progress)
	if !errors.Is(err, ErrXrayLiveUpdate) || !strings.Contains(err.Error(), "ado handlers 65-128/130") {
		t.Fatalf("lost failed batch: %v", err)
	}
	if len(r.out) != 128 || progress.routingAttempted {
		t.Fatal("partial batch boundary not exercised")
	}
	if err := a.undoXrayLiveChanges(context.Background(), r, delta, progress); err != nil {
		t.Fatal(err)
	}
	if len(r.out) != 0 {
		t.Fatal("partial handlers remain after compensation")
	}
	for _, op := range r.calls {
		if op == "adrules" || op == "rmi" || op == "adi" {
			t.Fatal("untouched routing or listeners mutated")
		}
	}
}
