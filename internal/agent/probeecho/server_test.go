package probeecho

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptError) Temporary() bool { return true }
func (temporaryAcceptError) Timeout() bool   { return true }

type temporaryListener struct {
	closed  chan struct{}
	started chan struct{}
	once    sync.Once
}

func (l *temporaryListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.started) })
	select {
	case <-l.closed:
		return nil, errors.New("listener closed")
	default:
		return nil, temporaryAcceptError{}
	}
}

func (l *temporaryListener) Close() error {
	l.once.Do(func() { close(l.started) })
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *temporaryListener) Addr() net.Addr { return temporaryAddr("probe") }

type temporaryAddr string

func (temporaryAddr) Network() string  { return "temporary" }
func (a temporaryAddr) String() string { return string(a) }

func TestAcceptLoopBackoffCanBeInterruptedByClose(t *testing.T) {
	listener := &temporaryListener{closed: make(chan struct{}), started: make(chan struct{})}
	server := &Server{listener: listener, done: make(chan struct{}), stop: make(chan struct{})}
	server.wg.Add(1)
	go server.acceptLoop()
	select {
	case <-listener.started:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not start")
	}

	started := time.Now()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("Close() took %s during temporary accept backoff", elapsed)
	}
	select {
	case <-server.done:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not stop")
	}
}
