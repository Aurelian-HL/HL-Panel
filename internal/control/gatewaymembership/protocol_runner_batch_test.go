package gatewaymembership

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type batchInitializationRepository struct {
	*initializationRepository
	calls   int
	configs map[string]ProtocolProbeConfig
}

func (r *batchInitializationRepository) ConfigureProtocolProbes(_ context.Context, configs map[string]ProtocolProbeConfig) error {
	r.calls++
	r.configs = configs
	return r.err
}
func TestBatchInitializationPreservesReadyOnFailure(t *testing.T) {
	for _, failure := range []error{nil, context.DeadlineExceeded} {
		a, b := schedulingInput("a", 1), schedulingInput("b", 1)
		a.Config, b.Config = ProtocolProbeConfig{}, ProtocolProbeConfig{}
		repo := &batchInitializationRepository{initializationRepository: &initializationRepository{schedulingRepository: &schedulingRepository{states: []ProbeState{a, schedulingInput("ready", 1), b}}, err: failure}}
		runner := NewProtocolRunner(repo, "unused", 39000, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ready, err := runner.loadRefresh(context.Background())
		if err != nil || len(ready) != 1 || ready[0].State.Rule.ID != "ready" || repo.calls != 1 || len(repo.configs) != 2 || len(repo.configured) != 0 {
			t.Fatalf("batch lost ready work or performed single writes: %v", err)
		}
		if repo.configs["a"].UUID == "" || repo.configs["a"].UUID == repo.configs["b"].UUID {
			t.Fatal("batch identities are not independent")
		}
	}
}
