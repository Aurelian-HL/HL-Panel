package gatewaymembership

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
)

type scanRepository struct {
	protocolRunnerRepository
	scanned chan struct{}
}

func (s scanRepository) ListEndpointPools(context.Context) ([]endpoints.EndpointPool, error) {
	s.scanned <- struct{}{}
	return nil, nil
}

func TestVerificationReceiptWakesProtocolRunnerWithoutWaitingInterval(t *testing.T) {
	repo := scanRepository{scanned: make(chan struct{}, 4)}
	runner := NewProtocolRunner(repo, "unused", 39000, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-repo.scanned:
	case <-time.After(time.Second):
		t.Fatal("initial scan missing")
	}
	runner.NotifyDeployment("node")
	select {
	case <-repo.scanned:
	case <-time.After(time.Second):
		t.Fatal("verification waited for periodic scan")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}
