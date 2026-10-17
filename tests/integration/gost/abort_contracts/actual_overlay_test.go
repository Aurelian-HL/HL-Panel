package quiccleanupreview

import (
	"context"
	"errors"
	dialer "github.com/go-gost/x/dialer/quic"
	xio "github.com/go-gost/x/internal/io"
	xnet "github.com/go-gost/x/internal/net"
	listener "github.com/go-gost/x/listener/quic"
	metrics "github.com/go-gost/x/metrics/wrapper"
	"github.com/quic-go/quic-go"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type adapterFactory func(*quic.Stream, *quic.Conn) net.Conn

func actualPipe(ctx context.Context, a, b io.ReadWriteCloser, timeout time.Duration) error {
	return xnet.Pipe(ctx, a, b, xnet.WithReadTimeout(timeout))
}

// The tracker observes the actual adapter without changing its cancellation.
type checkedConn struct {
	net.Conn
	writes      atomic.Int32
	completed   atomic.Int32
	unsafeClose atomic.Bool
	started     chan struct{}
	startOnce   sync.Once
}

func checkedAdapter(c net.Conn) *checkedConn {
	return &checkedConn{Conn: c, started: make(chan struct{})}
}
func (c *checkedConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	defer c.writes.Add(-1)
	c.startOnce.Do(func() { close(c.started) })
	n, e := c.Conn.Write(p)
	c.completed.Add(1)
	return n, e
}
func (c *checkedConn) Close() error {
	if c.writes.Load() != 0 {
		c.unsafeClose.Store(true)
	}
	return c.Conn.Close()
}
func (c *checkedConn) CloseWrite() error {
	if c.writes.Load() != 0 {
		c.unsafeClose.Store(true)
	}
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}
func (c *checkedConn) CloseRead() error     { return c.Conn.(interface{ CloseRead() error }).CloseRead() }
func (c *checkedConn) Abort() error         { return xio.Abort(c.Conn) }
func (c *checkedConn) UnwrapConn() net.Conn { return c.Conn }

func TestActualOverlayContracts(t *testing.T) {
	for name, wrap := range map[string]adapterFactory{
		"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn,
		"metrics-listener": func(s *quic.Stream, c *quic.Conn) net.Conn {
			return metrics.WrapConn("review", listener.NewReviewConn(s, c))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("external-cancel-blocked-read-write", func(t *testing.T) { testActualExternalCancelBlockedReadAndWrite(t, wrap) })
			t.Run("first-error-stops-other-half", func(t *testing.T) { testActualFirstErrorStopsOtherHalf(t, wrap) })
			t.Run("target-fin-delayed-reverse", func(t *testing.T) { testActualTargetFINThenDelayedReverse(t, wrap) })
			t.Run("client-fin-delayed-reply", func(t *testing.T) { testActualClientFINThenDelayedReply(t, wrap) })
		})
	}
}

func TestActualAbortThroughKnownWrapperAndComposite(t *testing.T) {
	for name, wrap := range map[string]adapterFactory{"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn} {
		t.Run(name, func(t *testing.T) {
			client, server := pair(t)
			for i := 0; i < 24; i++ {
				_, s := streams(t, client, server)
				q := metrics.WrapConn("review", wrap(s, server))
				composite := xio.NewReadWriteCloser(q, q, q)
				if err := xio.Abort(composite); err != nil {
					t.Fatal(err)
				}
				if err := composite.Close(); err != nil {
					t.Fatal(err)
				}
			}
			sibling(t, client, server)
		})
	}
}

type opaqueConn struct{ net.Conn }

func TestActualOpaqueWrapperHidesAbort(t *testing.T) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	q := &opaqueConn{listener.NewReviewConn(s, server)}
	if err := xio.Abort(q); err != xio.ErrUnsupported {
		t.Fatalf("want unsupported got %v", err)
	}
	_ = q.Conn.(interface{ Abort() error }).Abort()
	sibling(t, client, server)
}

func TestActualAdapterGracefulCloseDoesNotResetAcceptedSend(t *testing.T) {
	for name, wrap := range map[string]adapterFactory{"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn} {
		t.Run(name, func(t *testing.T) {
			client, server := pair(t)
			c, s := streams(t, client, server)
			q := wrap(s, server)
			payload := make([]byte, 128*1024)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			sent := make(chan error, 1)
			go func() {
				_, err := q.Write(payload)
				if err == nil {
					err = q.Close()
				}
				sent <- err
			}()
			time.Sleep(30 * time.Millisecond)
			c.SetReadDeadline(time.Now().Add(time.Second))
			got, err := io.ReadAll(c)
			if err != nil || len(got) != len(payload) {
				t.Fatalf("delivery bytes=%d want=%d err=%v", len(got), len(payload), err)
			}
			for i := range payload {
				if got[i] != payload[i] {
					t.Fatalf("byte mismatch at %d", i)
				}
			}
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			sibling(t, client, server)
		})
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.DialTCP("tcp4", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ln.AcceptTCP()
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}
func waitPipe(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if want == nil {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, want) {
			t.Fatalf("want %v got %v", want, err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pipe did not converge within 1s")
	}
}

func testActualExternalCancelBlockedReadAndWrite(t *testing.T, wrap adapterFactory) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	q := checkedAdapter(wrap(s, server))
	a, b := tcpPair(t)
	ctx, cancel := context.WithCancel(server.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- actualPipe(ctx, q, a, 0) }()
	producer := make(chan error, 1)
	go func() { _, err := b.Write(make([]byte, 8*1024*1024)); producer <- err }()
	select {
	case <-q.started:
	case <-time.After(time.Second):
		t.Fatal("QUIC write not reached")
	}
	blocked := false
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		before := q.completed.Load()
		if q.writes.Load() != 0 {
			time.Sleep(50 * time.Millisecond)
			if q.writes.Load() != 0 && q.completed.Load() == before {
				blocked = true
				break
			}
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	if !blocked {
		t.Fatal("no persistent flow-control blocked QUIC Write observed")
	}
	cancel()
	waitPipe(t, done, context.Canceled)
	if q.unsafeClose.Load() {
		t.Fatal("Close ran concurrently with QUIC Write")
	}
	_ = b.Close()
	select {
	case <-producer:
	case <-time.After(time.Second):
		t.Fatal("TCP producer leaked")
	}
	sibling(t, client, server)
}

type triggeredReadError struct {
	net.Conn
	trigger <-chan struct{}
	err     error
}

func (c *triggeredReadError) Read([]byte) (int, error) { <-c.trigger; return 0, c.err }
func testActualFirstErrorStopsOtherHalf(t *testing.T, wrap adapterFactory) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	q := checkedAdapter(wrap(s, server))
	a, _ := tcpPair(t)
	trigger := make(chan struct{})
	sentinel := errors.New("injected target read failure")
	wrapped := &triggeredReadError{Conn: a, trigger: trigger, err: sentinel}
	done := make(chan error, 1)
	go func() { done <- actualPipe(server.Context(), q, wrapped, 0) }()
	time.Sleep(20 * time.Millisecond)
	close(trigger)
	waitPipe(t, done, sentinel)
	if q.unsafeClose.Load() {
		t.Fatal("Close concurrent with Write")
	}
	sibling(t, client, server)
}

func testActualTargetFINThenDelayedReverse(t *testing.T, wrap adapterFactory) {
	client, server := pair(t)
	c, s := streams(t, client, server)
	q := checkedAdapter(wrap(s, server))
	a, b := tcpPair(t)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	b.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() { done <- actualPipe(server.Context(), q, a, 0) }()
	if _, err := b.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if err := b.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(c)
	if err != nil || string(response) != "response" {
		t.Fatalf("response=%q err=%v", response, err)
	}
	time.Sleep(75 * time.Millisecond)
	if _, err = c.Write([]byte("delayed-ack")); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	ack, err := io.ReadAll(b)
	if err != nil || string(ack) != "delayed-ack" {
		t.Fatalf("ack=%q err=%v", ack, err)
	}
	waitPipe(t, done, nil)
	if q.unsafeClose.Load() {
		t.Fatal("Close concurrent with Write")
	}
	sibling(t, client, server)
}

func testActualClientFINThenDelayedReply(t *testing.T, wrap adapterFactory) {
	client, server := pair(t)
	c, s := streams(t, client, server)
	q := checkedAdapter(wrap(s, server))
	a, b := tcpPair(t)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	b.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() { done <- actualPipe(server.Context(), q, a, 0) }()
	if _, err := c.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := io.ReadAll(b)
	if err != nil || string(request) != "request" {
		t.Fatalf("request=%q err=%v", request, err)
	}
	time.Sleep(75 * time.Millisecond)
	if _, err = b.Write([]byte("delayed-response")); err != nil {
		t.Fatal(err)
	}
	if err = b.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(c)
	if err != nil || string(response) != "delayed-response" {
		t.Fatalf("response=%q err=%v", response, err)
	}
	waitPipe(t, done, nil)
	if q.unsafeClose.Load() {
		t.Fatal("Close concurrent with Write")
	}
	sibling(t, client, server)
}
