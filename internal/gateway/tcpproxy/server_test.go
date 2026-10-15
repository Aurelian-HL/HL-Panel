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
	mu             sync.Mutex
	backend        Backend
	backends       []Backend
	selects        int
	releases       int
	releasedIDs    []string
	err            error
	lastKey        string
	keyCalls       []string
	excludedCalls  []map[string]struct{}
	selectedIDs    []string
	ignoreExcludes bool
}

func (s *testSelector) Select() (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectKeyExcludingLocked("", nil)
}

func (s *testSelector) SelectKey(key string) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKey = key
	s.keyCalls = append(s.keyCalls, key)
	return s.selectKeyExcludingLocked(key, nil)
}

func (s *testSelector) Release(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	valid := id == s.backend.ID
	for _, backend := range s.backends {
		if id == backend.ID {
			valid = true
			break
		}
	}
	if !valid {
		return errors.New("unexpected backend release")
	}
	s.releases++
	s.releasedIDs = append(s.releasedIDs, id)
	return nil
}

func (s *testSelector) SelectExcluding(excluded map[string]struct{}) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectKeyExcludingLocked("", excluded)
}

func (s *testSelector) SelectKeyExcluding(key string, excluded map[string]struct{}) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKey = key
	s.keyCalls = append(s.keyCalls, key)
	return s.selectKeyExcludingLocked(key, excluded)
}

func (s *testSelector) selectKeyExcludingLocked(_ string, excluded map[string]struct{}) (Backend, error) {
	s.selects++
	copyExcluded := make(map[string]struct{}, len(excluded))
	for id := range excluded {
		copyExcluded[id] = struct{}{}
	}
	s.excludedCalls = append(s.excludedCalls, copyExcluded)
	if s.err != nil {
		return Backend{}, s.err
	}
	backends := s.backends
	if len(backends) == 0 {
		backends = []Backend{s.backend}
	}
	for _, backend := range backends {
		if !s.ignoreExcludes {
			if _, skip := excluded[backend.ID]; skip {
				continue
			}
		}
		s.selectedIDs = append(s.selectedIDs, backend.ID)
		return backend, nil
	}
	return Backend{}, errors.New("no backend after exclusions")
}

func (s *testSelector) snapshot() (selects, releases int, releasedIDs, selectedIDs []string, excludedCalls []map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selects, s.releases, append([]string(nil), s.releasedIDs...), append([]string(nil), s.selectedIDs...), append([]map[string]struct{}(nil), s.excludedCalls...)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
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
