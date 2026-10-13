package tcpproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type testSelector struct {
	mu       sync.Mutex
	backend  Backend
	selects  int
	releases int
	err      error
	lastKey  string
}

type failoverSelector struct {
	mu          sync.Mutex
	backends    []Backend
	next        int
	releases    map[string]int
	unreachable []string
}

type versionedReachabilitySelector struct {
	testSelector
	mu             sync.Mutex
	versionedCalls []reachabilityCall
	legacyCalls    []reachabilityCall
}

type reachabilityCall struct {
	id        string
	address   string
	revision  uint64
	reachable bool
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptError) Temporary() bool { return true }
func (temporaryAcceptError) Timeout() bool   { return true }

type temporaryListener struct {
	mu       sync.Mutex
	closed   chan struct{}
	accepted chan net.Conn
	attempts int
}

func newTemporaryListener() *temporaryListener {
	return &temporaryListener{closed: make(chan struct{}), accepted: make(chan net.Conn)}
}

func (l *temporaryListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.attempts == 0 {
		l.attempts++
		l.mu.Unlock()
		return nil, temporaryAcceptError{}
	}
	closed := l.closed
	accepted := l.accepted
	l.mu.Unlock()
	select {
	case <-closed:
		return nil, net.ErrClosed
	case connection := <-accepted:
		return connection, nil
	}
}

func (l *temporaryListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *temporaryListener) Addr() net.Addr { return temporaryAddr("temporary") }

type temporaryAddr string

func (a temporaryAddr) Network() string { return "temporary" }
func (a temporaryAddr) String() string  { return string(a) }

func (s *versionedReachabilitySelector) SetLocalReachability(id, address string, reachable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacyCalls = append(s.legacyCalls, reachabilityCall{id: id, address: address, reachable: reachable})
	return nil
}

func (s *versionedReachabilitySelector) SetLocalReachabilityAtRevision(id, address string, revision uint64, reachable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionedCalls = append(s.versionedCalls, reachabilityCall{id: id, address: address, revision: revision, reachable: reachable})
	return nil
}

func (s *failoverSelector) Select() (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.backends) {
		return Backend{}, errors.New("no backend left")
	}
	backend := s.backends[s.next]
	s.next++
	return backend, nil
}

func (s *failoverSelector) SelectKey(string) (Backend, error) { return s.Select() }

func (s *failoverSelector) Release(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.releases == nil {
		s.releases = make(map[string]int)
	}
	s.releases[id]++
	return nil
}

func (s *failoverSelector) SetLocalReachability(id, _ string, reachable bool) error {
	if reachable {
		return nil
	}
	s.mu.Lock()
	s.unreachable = append(s.unreachable, id)
	s.mu.Unlock()
	return nil
}

func (s *testSelector) Select() (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selects++
	if s.err != nil {
		return Backend{}, s.err
	}
	return s.backend, nil
}

func (s *testSelector) SelectKey(key string) (Backend, error) {
	s.mu.Lock()
	s.lastKey = key
	s.mu.Unlock()
	return s.Select()
}

func (s *testSelector) Release(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.backend.ID {
		return errors.New("unexpected backend release")
	}
	s.releases++
	return nil
}

func TestServerForwardsOneConnectionToSelectedBackend(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()
	backendDone := make(chan error, 1)
	go func() {
		connection, acceptErr := backendListener.Accept()
		if acceptErr != nil {
			backendDone <- acceptErr
			return
		}
		defer connection.Close()
		data, readErr := io.ReadAll(io.LimitReader(connection, 64))
		if readErr != nil {
			backendDone <- readErr
			return
		}
		_, writeErr := connection.Write([]byte("backend:" + string(data)))
		backendDone <- writeErr
	}()

	selector := &testSelector{backend: Backend{ID: "node-a", Address: backendListener.Addr().String()}}
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Listener: frontListener, Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if tcpClient, ok := client.(*net.TCPConn); ok {
		if err := tcpClient.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "backend:hello" {
		t.Fatalf("response = %q, want backend echo", response)
	}
	_ = client.Close()
	select {
	case err := <-backendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend did not receive forwarded connection")
	}
	_ = server.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.selects != 1 || selector.releases != 1 {
		t.Fatalf("selector calls = %d selects, %d releases; want one each", selector.selects, selector.releases)
	}
	if selector.lastKey != "127.0.0.1" {
		t.Fatalf("selector affinity key = %q, want client IP", selector.lastKey)
	}
}

func TestServeCloseInterruptsTemporaryAcceptRetry(t *testing.T) {
	listener := newTemporaryListener()
	server, err := New(Config{Listener: listener, Selector: &testSelector{err: errors.New("no backend")}})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	// Let Serve enter the temporary-error wait before closing it.
	select {
	case <-time.After(10 * time.Millisecond):
	}
	started := time.Now()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error after close = %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 25*time.Millisecond {
			t.Fatalf("Serve() took %s to stop after close, want immediate interruption", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after close")
	}
}

func TestServerContextCancellationClosesForwardedConnections(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()
	backendAccepted := make(chan net.Conn, 1)
	backendDone := make(chan struct{})
	go func() {
		connection, acceptErr := backendListener.Accept()
		if acceptErr != nil {
			close(backendDone)
			return
		}
		backendAccepted <- connection
		_, _ = io.Copy(io.Discard, connection)
		_ = connection.Close()
		close(backendDone)
	}()

	selector := &testSelector{backend: Backend{ID: "node-a", Address: backendListener.Addr().String()}}
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Listener: frontListener, Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-backendAccepted:
	case <-time.After(time.Second):
		t.Fatal("backend connection was not established")
	}
	cancel()

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, readErr := client.Read(make([]byte, 1))
	if readErr == nil {
		t.Fatal("client connection remained open after context cancellation")
	}
	select {
	case <-backendDone:
	case <-time.After(time.Second):
		t.Fatal("backend connection was not closed after context cancellation")
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.releases != 1 {
		t.Fatalf("selector releases = %d, want one release", selector.releases)
	}
}

func TestServerClosesClientWhenNoBackendIsAvailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &testSelector{err: errors.New("no healthy endpoint")}
	server, err := New(Config{Listener: listener, Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, readErr := client.Read(make([]byte, 1))
	_ = client.Close()
	if readErr == nil {
		t.Fatal("client unexpectedly remained open")
	}
	_ = server.Close()
	<-serveDone
}

func TestServerRetriesDialWithAnotherBackendAndMarksFailedBackend(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()
	backendDone := make(chan error, 1)
	go func() {
		connection, acceptErr := backendListener.Accept()
		if acceptErr != nil {
			backendDone <- acceptErr
			return
		}
		defer connection.Close()
		data, readErr := io.ReadAll(io.LimitReader(connection, 64))
		if readErr != nil {
			backendDone <- readErr
			return
		}
		_, writeErr := connection.Write([]byte("backend:" + string(data)))
		backendDone <- writeErr
	}()

	selector := &failoverSelector{backends: []Backend{
		{ID: "node-a", Address: "unreachable.example:443"},
		{ID: "node-b", Address: backendListener.Addr().String()},
	}}
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Listener: frontListener, Selector: selector, MaxDialAttempts: 2,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "unreachable.example:443" {
				return nil, errors.New("simulated backend outage")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if tcpClient, ok := client.(*net.TCPConn); ok {
		if err := tcpClient.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	response, err := io.ReadAll(client)
	_ = client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "backend:hello" {
		t.Fatalf("response = %q, want backend echo", response)
	}
	select {
	case err := <-backendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend did not receive forwarded connection")
	}
	_ = server.Close()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.releases["node-a"] != 1 || selector.releases["node-b"] != 1 {
		t.Fatalf("releases = %#v, want one reservation release per attempt", selector.releases)
	}
	if len(selector.unreachable) != 1 || selector.unreachable[0] != "node-a" {
		t.Fatalf("unreachable reports = %#v, want node-a", selector.unreachable)
	}
}

func TestServerClosesConnectionReturnedAlongsideDialError(t *testing.T) {
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &testSelector{backend: Backend{ID: "node-a", Address: "127.0.0.1:443"}}
	partialGateway, partialPeer := net.Pipe()
	dialStarted := make(chan struct{})
	server, err := New(Config{
		Listener: frontListener, Selector: selector, MaxDialAttempts: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			close(dialStarted)
			return partialGateway, errors.New("partial dial failure")
		},
	})
	if err != nil {
		partialPeer.Close()
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		partialPeer.Close()
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("backend dial did not start")
	}
	_ = partialPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := partialPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("partially returned dial connection remained open")
	}
	_ = partialPeer.Close()
	_ = server.Close()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerBoundsFailoverByOneDialBudget(t *testing.T) {
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &failoverSelector{backends: []Backend{
		{ID: "node-a", Address: "node-a.example:443"},
		{ID: "node-b", Address: "node-b.example:443"},
		{ID: "node-c", Address: "node-c.example:443"},
	}}
	dialStarted := make(chan struct{}, 3)
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		dialStarted <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server, err := New(Config{
		Listener: frontListener, Selector: selector, Dial: dial,
		DialTimeout: 150 * time.Millisecond, MaxDialAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	started := time.Now()
	_, readErr := client.Read(make([]byte, 1))
	elapsed := time.Since(started)
	if readErr == nil {
		t.Fatal("client unexpectedly received data")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("failover exceeded one dial budget: elapsed=%s", elapsed)
	}
	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("backend dial did not start")
	}
	_ = server.Close()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerContextCancellationDoesNotMarkBackendUnreachable(t *testing.T) {
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &failoverSelector{backends: []Backend{{ID: "node-a", Address: "node-a.example:443"}}}
	dialStarted := make(chan struct{})
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server, err := New(Config{Listener: frontListener, Selector: selector, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("backend dial did not start")
	}
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}

	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.releases["node-a"] != 1 {
		t.Fatalf("releases = %#v, want one reservation release", selector.releases)
	}
	if len(selector.unreachable) != 0 {
		t.Fatalf("unreachable reports = %#v, want none for context cancellation", selector.unreachable)
	}
}

func TestServerContextCancellationAfterDialDoesNotMarkBackendReachable(t *testing.T) {
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &versionedReachabilitySelector{testSelector: testSelector{backend: Backend{ID: "node-a", Address: "127.0.0.1:443", MembershipRevision: 7}}}
	gatewaySide, backendSide := net.Pipe()
	defer backendSide.Close()
	ctx, cancel := context.WithCancel(context.Background())
	server, err := New(Config{
		Listener: frontListener,
		Selector: selector,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			// Simulate a successful dial racing with gateway shutdown.
			cancel()
			return gatewaySide, nil
		},
	})
	if err != nil {
		gatewaySide.Close()
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		server.Close()
		<-serveDone
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if len(selector.versionedCalls) != 0 || len(selector.legacyCalls) != 0 {
		t.Fatalf("reachability calls = versioned %#v legacy %#v, want none", selector.versionedCalls, selector.legacyCalls)
	}
}

func TestListenerFailureClosesConnectionsReleasesReservationAndWaits(t *testing.T) {
	frontListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selector := &testSelector{backend: Backend{ID: "node-a", Address: "127.0.0.1:443"}}
	backendPeers := make(chan net.Conn, 1)
	dial := func(context.Context, string, string) (net.Conn, error) {
		gatewaySide, backendSide := net.Pipe()
		backendPeers <- backendSide
		return gatewaySide, nil
	}
	server, err := New(Config{Listener: frontListener, Selector: selector, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	client, err := net.DialTimeout("tcp", frontListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var backendPeer net.Conn
	select {
	case backendPeer = <-backendPeers:
		defer backendPeer.Close()
	case <-time.After(time.Second):
		t.Fatal("backend was not dialed")
	}
	if err := server.Serve(context.Background()); !errors.Is(err, ErrAlreadyServing) {
		t.Fatalf("second Serve() error = %v, want ErrAlreadyServing", err)
	}
	if err := frontListener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err == nil || !strings.Contains(err.Error(), "accept client connection") {
			t.Fatalf("Serve() error = %v, want listener failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not finish after listener failure")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client remained open after listener failure")
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.releases != 1 {
		t.Fatalf("selector releases = %d, want one before Serve returns", selector.releases)
	}
}

func TestValidateBackendRejectsCredentialsAndControlCharacters(t *testing.T) {
	for _, address := range []string{"user:pass@host:443", "host:443?x=1", "host\r:443", "host\n:443", "host\t:443", "host"} {
		if err := validateBackend(Backend{ID: "node", Address: address}); err == nil {
			t.Fatalf("validateBackend(%q) accepted unsafe address", address)
		}
	}
	if err := validateBackend(Backend{ID: "node", Address: "127.0.0.1:443"}); err != nil {
		t.Fatal(err)
	}
	if err := validateBackend(Backend{ID: "node", Address: "entry.example.net:443"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sanitizeError(errors.New("line\nwith spaces")), "line with spaces") {
		t.Fatal("sanitizeError did not normalize message")
	}
}

func TestVersionedReachabilityIsPreferredWhenBackendHasRevision(t *testing.T) {
	selector := &versionedReachabilitySelector{}
	server := &Server{selector: selector}
	backend := Backend{ID: "node-a", Address: "127.0.0.1:443", MembershipRevision: 42}
	if err := server.setLocalReachability(backend, false); err != nil {
		t.Fatal(err)
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if len(selector.versionedCalls) != 1 || selector.versionedCalls[0].revision != 42 || selector.versionedCalls[0].reachable {
		t.Fatalf("versioned reachability calls = %#v, want one revision 42 failure", selector.versionedCalls)
	}
	if len(selector.legacyCalls) != 0 {
		t.Fatalf("legacy reachability calls = %#v, want none", selector.legacyCalls)
	}
}

func TestLegacyReachabilityRemainsCompatibleWithoutBackendRevision(t *testing.T) {
	selector := &versionedReachabilitySelector{}
	server := &Server{selector: selector}
	if err := server.setLocalReachability(Backend{ID: "node-a", Address: "127.0.0.1:443"}, true); err != nil {
		t.Fatal(err)
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if len(selector.versionedCalls) != 0 || len(selector.legacyCalls) != 1 || !selector.legacyCalls[0].reachable {
		t.Fatalf("reachability calls = versioned %#v legacy %#v, want one legacy recovery", selector.versionedCalls, selector.legacyCalls)
	}
}
