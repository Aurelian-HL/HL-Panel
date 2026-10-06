package endpointselector_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/gateway/endpointselector"
	"github.com/hongle/hl-panel/internal/gateway/healthprobe"
	"github.com/hongle/hl-panel/internal/gateway/tcpproxy"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

type echoBackend struct {
	id       string
	listener net.Listener
	wg       sync.WaitGroup
}

type reachabilityObservation struct {
	verdict healthprobe.Verdict
	err     error
}

func startEchoBackend(t *testing.T, id string) *echoBackend {
	t.Helper()
	return startEchoBackendAt(t, id, "127.0.0.1:0")
}

func startEchoBackendAt(t *testing.T, id, address string) *echoBackend {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var listener net.Listener
	var err error
	for {
		listener, err = net.Listen("tcp", address)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listen on %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	backend := &echoBackend{id: id, listener: listener}
	backend.wg.Add(1)
	go func() {
		defer backend.wg.Done()
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			backend.wg.Add(1)
			go func() {
				defer backend.wg.Done()
				defer connection.Close()
				_, _ = io.ReadAll(io.LimitReader(connection, 1024))
				_, _ = io.WriteString(connection, backend.id)
			}()
		}
	}()
	return backend
}

func TestTCPReachabilityAutomaticallyVetoesAndRestoresOneOfTenBackends(t *testing.T) {
	backends := make([]*echoBackend, 0, 10)
	members := make([]endpointrouter.Endpoint, 0, 10)
	lease := time.Now().Add(time.Hour)
	for index := 1; index <= 10; index++ {
		id := fmt.Sprintf("node-%02d", index)
		backend := startEchoBackend(t, id)
		backends = append(backends, backend)
		member := endpointrouter.Endpoint{
			ID: id, Address: backend.listener.Addr().String(), Weight: 1,
			Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: lease,
		}
		members = append(members, member)
	}
	defer func() {
		for _, backend := range backends {
			backend.close()
		}
	}()

	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	verdicts := make(chan reachabilityObservation, 64)
	controller, err := healthprobe.NewController(router, healthprobe.Config{
		Interval: 200 * time.Millisecond, Timeout: 150 * time.Millisecond,
		FailureThreshold: 2, SuccessThreshold: 2, MaxConcurrency: 10,
	}, func(verdict healthprobe.Verdict, err error) {
		verdicts <- reachabilityObservation{verdict: verdict, err: err}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReplaceVersionedMembership(1, members); err != nil {
		t.Fatal(err)
	}
	probeContext, stopProbes := context.WithCancel(context.Background())
	probeDone := make(chan struct{})
	go func() {
		controller.Run(probeContext)
		close(probeDone)
	}()
	defer func() {
		stopProbes()
		select {
		case <-probeDone:
		case <-time.After(time.Second):
			t.Error("reachability monitor did not stop")
		}
	}()

	selector, err := endpointselector.NewRouterSelector(router)
	if err != nil {
		t.Fatal(err)
	}
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := tcpproxy.New(tcpproxy.Config{Listener: frontListener, Selector: selector, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, stopServer := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveContext) }()
	defer func() {
		stopServer()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("gateway Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("gateway did not stop")
		}
	}()

	failedAddress := backends[9].listener.Addr().String()
	backends[9].close()
	waitReachabilityVerdict(t, verdicts, "node-10", false, 2)
	if blocked, err := router.LocallyBlocked("node-10"); err != nil || !blocked {
		t.Fatalf("failed node local veto = %v, error = %v", blocked, err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 18, map[string]int{
		"node-01": 2, "node-02": 2, "node-03": 2, "node-04": 2, "node-05": 2,
		"node-06": 2, "node-07": 2, "node-08": 2, "node-09": 2,
	})

	backends[9] = startEchoBackendAt(t, "node-10", failedAddress)
	waitReachabilityVerdict(t, verdicts, "node-10", true, 2)
	if blocked, err := router.LocallyBlocked("node-10"); err != nil || blocked {
		t.Fatalf("recovered node local veto = %v, error = %v", blocked, err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 20, map[string]int{
		"node-01": 2, "node-02": 2, "node-03": 2, "node-04": 2, "node-05": 2,
		"node-06": 2, "node-07": 2, "node-08": 2, "node-09": 2, "node-10": 2,
	})
}

func waitReachabilityVerdict(
	t *testing.T,
	verdicts <-chan reachabilityObservation,
	id string,
	reachable bool,
	consecutive int,
) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case observed := <-verdicts:
			if observed.err != nil {
				t.Fatalf("apply reachability verdict: %v", observed.err)
			}
			if observed.verdict.ID == id && observed.verdict.Reachable == reachable {
				if observed.verdict.Consecutive != consecutive {
					t.Fatalf("reachability verdict = %#v, want consecutive %d", observed.verdict, consecutive)
				}
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for endpoint %s reachable=%v", id, reachable)
		}
	}
}

func (b *echoBackend) close() {
	_ = b.listener.Close()
	b.wg.Wait()
}

func TestSingleEntryDistributesAcrossTenBackendsAndTracksLifecycle(t *testing.T) {
	backends := make([]*echoBackend, 0, 10)
	members := make([]endpointrouter.Endpoint, 0, 10)
	lease := time.Now().Add(time.Hour)
	for index := 1; index <= 10; index++ {
		id := fmt.Sprintf("node-%02d", index)
		backend := startEchoBackend(t, id)
		backends = append(backends, backend)
		members = append(members, endpointrouter.Endpoint{
			ID: id, Address: backend.listener.Addr().String(), Weight: index,
			Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: lease,
		})
	}
	defer func() {
		for _, backend := range backends {
			backend.close()
		}
	}()

	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceMembership(members); err != nil {
		t.Fatal(err)
	}
	selector, err := endpointselector.NewRouterSelector(router)
	if err != nil {
		t.Fatal(err)
	}
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := tcpproxy.New(tcpproxy.Config{Listener: frontListener, Selector: selector, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("gateway Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("gateway did not stop")
		}
	}()

	assertWeightedCycle(t, frontListener.Addr().String(), 55, map[string]int{
		"node-01": 1, "node-02": 2, "node-03": 3, "node-04": 4, "node-05": 5,
		"node-06": 6, "node-07": 7, "node-08": 8, "node-09": 9, "node-10": 10,
	})

	members[9].Status = endpointrouter.EndpointOffline
	if err := router.ReplaceMembership(members); err != nil {
		t.Fatal(err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 45, map[string]int{
		"node-01": 1, "node-02": 2, "node-03": 3, "node-04": 4, "node-05": 5,
		"node-06": 6, "node-07": 7, "node-08": 8, "node-09": 9,
	})

	members[8].Status = endpointrouter.EndpointDraining
	if err := router.ReplaceMembership(members); err != nil {
		t.Fatal(err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 36, map[string]int{
		"node-01": 1, "node-02": 2, "node-03": 3, "node-04": 4,
		"node-05": 5, "node-06": 6, "node-07": 7, "node-08": 8,
	})

	members[7].HealthLeaseExpiresAt = time.Now().Add(-time.Second)
	if err := router.ReplaceMembership(members); err != nil {
		t.Fatal(err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 28, map[string]int{
		"node-01": 1, "node-02": 2, "node-03": 3, "node-04": 4,
		"node-05": 5, "node-06": 6, "node-07": 7,
	})

	members[9].Status = endpointrouter.EndpointReady
	members[9].HealthLeaseExpiresAt = time.Now().Add(time.Hour)
	if err := router.ReplaceMembership(members); err != nil {
		t.Fatal(err)
	}
	assertWeightedCycle(t, frontListener.Addr().String(), 38, map[string]int{
		"node-01": 1, "node-02": 2, "node-03": 3, "node-04": 4,
		"node-05": 5, "node-06": 6, "node-07": 7, "node-10": 10,
	})

	deadline := time.Now().Add(time.Second)
	for _, member := range members {
		for {
			active, activeErr := router.ActiveConnections(member.ID)
			if activeErr != nil {
				t.Fatal(activeErr)
			}
			if active == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("endpoint %s retained %d active connections", member.ID, active)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func assertWeightedCycle(t *testing.T, address string, connections int, want map[string]int) {
	t.Helper()
	actual := make(map[string]int)
	for index := 0; index < connections; index++ {
		id := requestBackendID(t, address)
		actual[id]++
	}
	if len(actual) != len(want) {
		t.Fatalf("selected backend set = %#v, want %#v", actual, want)
	}
	for id, count := range want {
		if actual[id] != count {
			t.Fatalf("backend %s selected %d times, want %d; all = %#v", id, actual[id], count, actual)
		}
	}
}

func requestBackendID(t *testing.T, address string) string {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "probe"); err != nil {
		t.Fatal(err)
	}
	if tcpConnection, ok := connection.(*net.TCPConn); ok {
		if err := tcpConnection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(response))
}
