package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

type sequenceSource struct {
	mu        sync.Mutex
	snapshots []membership.Snapshot
	index     int
}

func (s *sequenceSource) Load(context.Context) (membership.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 {
		return membership.Snapshot{}, errors.New("no snapshot")
	}
	index := s.index
	if index < len(s.snapshots)-1 {
		s.index++
	}
	return s.snapshots[index], nil
}

type recordingRouter struct {
	mu        sync.Mutex
	applied   [][]endpointrouter.Endpoint
	revisions []uint64
	appliedC  chan struct{}
}

func (r *recordingRouter) ReplaceVersionedMembership(revision uint64, endpoints []endpointrouter.Endpoint) error {
	r.mu.Lock()
	r.applied = append(r.applied, append([]endpointrouter.Endpoint(nil), endpoints...))
	r.revisions = append(r.revisions, revision)
	r.mu.Unlock()
	select {
	case r.appliedC <- struct{}{}:
	default:
	}
	return nil
}

type contextServer struct {
	started chan struct{}
}

type recordingMonitor struct {
	started chan struct{}
}

func (m *recordingMonitor) Run(ctx context.Context) {
	close(m.started)
	<-ctx.Done()
}

func (s *contextServer) Serve(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

func TestRuntimeAppliesNewerSnapshotAndStopsWithContext(t *testing.T) {
	future := time.Now().Add(time.Minute)
	source := &sequenceSource{snapshots: []membership.Snapshot{
		{Revision: 1, SHA256: "one", Endpoints: []endpointrouter.Endpoint{{ID: "a", Address: "127.0.0.1:10001", Weight: 1, Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: future}}},
		{Revision: 2, SHA256: "two", Endpoints: []endpointrouter.Endpoint{{ID: "b", Address: "127.0.0.1:10002", Weight: 1, Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: future}}},
	}}
	router := &recordingRouter{appliedC: make(chan struct{}, 4)}
	server := &contextServer{started: make(chan struct{})}
	monitor := &recordingMonitor{started: make(chan struct{})}
	runtime, err := New(Config{Source: source, Router: router, Server: server, ReachabilityMonitor: monitor, RefreshInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-server.started:
	case <-time.After(time.Second):
		t.Fatal("gateway server did not start")
	}
	select {
	case <-monitor.started:
	case <-time.After(time.Second):
		t.Fatal("reachability monitor did not start")
	}
	for count := 0; count < 2; count++ {
		select {
		case <-router.appliedC:
		case <-time.After(time.Second):
			t.Fatal("membership snapshot was not applied")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if len(router.applied) != 2 || router.applied[0][0].ID != "a" || router.applied[1][0].ID != "b" {
		t.Fatalf("applied snapshots = %#v", router.applied)
	}
	if len(router.revisions) != 2 || router.revisions[0] != 1 || router.revisions[1] != 2 {
		t.Fatalf("applied revisions = %#v", router.revisions)
	}
}

func TestApplyRejectsRollbackAndSameRevisionMutation(t *testing.T) {
	router := &recordingRouter{appliedC: make(chan struct{}, 1)}
	runtime := &Runtime{router: router}
	base := membership.Snapshot{Revision: 4, SHA256: "base", Endpoints: []endpointrouter.Endpoint{}}
	state, err := runtime.apply(base, appliedSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.apply(membership.Snapshot{Revision: 3, SHA256: "old", Endpoints: []endpointrouter.Endpoint{}}, state); err == nil {
		t.Fatal("apply() accepted a revision rollback")
	}
	if _, err := runtime.apply(membership.Snapshot{Revision: 4, SHA256: "changed", Endpoints: []endpointrouter.Endpoint{}}, state); err == nil {
		t.Fatal("apply() accepted changed content at the same revision")
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if len(router.applied) != 1 {
		t.Fatalf("router apply count = %d, want one", len(router.applied))
	}
}
