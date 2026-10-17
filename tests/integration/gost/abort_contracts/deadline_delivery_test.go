package quiccleanupreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	dialer "github.com/go-gost/x/dialer/quic"
	xio "github.com/go-gost/x/internal/io"
	xnet "github.com/go-gost/x/internal/net"
	listener "github.com/go-gost/x/listener/quic"
	metrics "github.com/go-gost/x/metrics/wrapper"
)

type eofRW struct {
	mu      sync.Mutex
	data    []byte
	readErr error
	writes  bytes.Buffer
	short   bool
}

func (c *eofRW) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := copy(p, c.data)
	c.data = nil
	return n, c.readErr
}
func (c *eofRW) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.short && len(p) > 0 {
		return len(p) - 1, nil
	}
	return c.writes.Write(p)
}
func (c *eofRW) Close() error                    { return nil }
func (c *eofRW) CloseRead() error                { return nil }
func (c *eofRW) CloseWrite() error               { return nil }
func (c *eofRW) SetDeadline(time.Time) error     { return nil }
func (c *eofRW) SetReadDeadline(time.Time) error { return nil }
func TestActualFinalReadBytesAndShortWrite(t *testing.T) {
	for _, readErr := range []error{io.EOF, errors.New("after-final-bytes")} {
		a := &eofRW{data: []byte("last-payload"), readErr: readErr}
		b := &eofRW{readErr: io.EOF}
		err := xnet.Pipe(context.Background(), a, b)
		if readErr == io.EOF && err != nil {
			t.Fatal(err)
		}
		if readErr != io.EOF && !errors.Is(err, readErr) {
			t.Fatalf("want read error got %v", err)
		}
		if b.writes.String() != "last-payload" {
			t.Fatalf("last bytes lost: %q", b.writes.String())
		}
	}
	a := &eofRW{data: []byte("payload"), readErr: io.EOF}
	b := &eofRW{readErr: io.EOF, short: true}
	if err := xnet.Pipe(context.Background(), a, b); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("want short write got %v", err)
	}
}

type closeWriteFailure struct {
	*deadlineGate
	err error
}

func (c *closeWriteFailure) CloseWrite() error { return c.err }
func TestActualCloseWriteErrorStopsOtherHalf(t *testing.T) {
	sentinel := errors.New("half-close-real-failure")
	gate := &deadlineGate{abortObserved: make(chan struct{}), readBlocked: make(chan struct{})}
	b := &closeWriteFailure{deadlineGate: gate, err: sentinel}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { _ = gate.SetDeadline(time.Now()) })
	done := make(chan error, 1)
	go func() { done <- xnet.Pipe(ctx, &eofRW{readErr: io.EOF}, b) }()
	waitPipe(t, done, sentinel)
	select {
	case <-gate.abortObserved:
	default:
		t.Fatal("CloseWrite error did not abort blocked reverse reader")
	}
}

type abortFallbackConn struct {
	mu                              sync.Mutex
	abortErr, deadlineErr, closeErr error
	entered, unblock                chan struct{}
	enterOnce, unblockOnce          sync.Once
	calls                           []string
}

func (c *abortFallbackConn) Read([]byte) (int, error) {
	c.enterOnce.Do(func() { close(c.entered) })
	<-c.unblock
	return 0, net.ErrClosed
}
func (c *abortFallbackConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *abortFallbackConn) Abort() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "abort")
	return c.abortErr
}
func (c *abortFallbackConn) SetDeadline(time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "deadline")
	if c.deadlineErr == nil {
		c.unblockOnce.Do(func() { close(c.unblock) })
	}
	return c.deadlineErr
}
func (c *abortFallbackConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "close")
	c.unblockOnce.Do(func() { close(c.unblock) })
	return c.closeErr
}
func TestActualAbortFailureFallbacks(t *testing.T) {
	abortErr := errors.New("abort-real-failure")
	deadlineErr := errors.New("deadline-real-failure")
	closeErr := errors.New("close-real-failure")
	cases := []struct {
		name                            string
		abortErr, deadlineErr, closeErr error
		wantCalls                       string
		wantErrors                      []error
	}{
		{"abort-error-deadline-ok", abortErr, nil, nil, "abort,deadline,close", []error{abortErr}},
		{"wrapped-unsupported-deadline-ok", fmt.Errorf("wrapper: %w", xio.ErrUnsupported), nil, nil, "abort,deadline,close", nil},
		{"deadline-error-close", xio.ErrUnsupported, deadlineErr, nil, "abort,deadline,close,close", []error{deadlineErr}},
		{"abort-and-deadline-errors-close", abortErr, deadlineErr, nil, "abort,deadline,close,close", []error{abortErr, deadlineErr}},
		{"close-error-retained", abortErr, deadlineErr, closeErr, "abort,deadline,close,close", []error{abortErr, deadlineErr, closeErr}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := &abortFallbackConn{abortErr: tc.abortErr, deadlineErr: tc.deadlineErr, closeErr: tc.closeErr, entered: make(chan struct{}), unblock: make(chan struct{})}
			local, peer := tcpPair(t)
			defer peer.Close()
			t.Cleanup(func() { gate.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- xnet.Pipe(ctx, gate, local) }()
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("reader did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				for _, want := range tc.wantErrors {
					if !errors.Is(err, want) {
						t.Fatalf("cleanup error %v lost: %v", want, err)
					}
				}
				if errors.Is(err, xio.ErrUnsupported) {
					t.Fatalf("unsupported marker surfaced despite successful fallback: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("failed cleanup skipped fallback; Pipe did not converge")
			}
			gate.mu.Lock()
			calls := strings.Join(gate.calls, ",")
			gate.mu.Unlock()
			if calls != tc.wantCalls {
				t.Fatalf("want cleanup order %s got %s", tc.wantCalls, calls)
			}
		})
	}
}

type closeErrorTracker struct {
	net.Conn
	mu    sync.Mutex
	first error
}

func (c *closeErrorTracker) CloseWrite() error {
	err := c.Conn.(interface{ CloseWrite() error }).CloseWrite()
	c.mu.Lock()
	if c.first == nil {
		c.first = err
	}
	c.mu.Unlock()
	return err
}
func (c *closeErrorTracker) CloseRead() error {
	return c.Conn.(interface{ CloseRead() error }).CloseRead()
}
func (c *closeErrorTracker) UnwrapConn() net.Conn { return c.Conn }
func TestActualQUICCloseWriteErrorPropagates(t *testing.T) {
	for name, wrap := range map[string]adapterFactory{"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn} {
		t.Run(name, func(t *testing.T) {
			client, server := pair(t)
			c, s := streams(t, client, server)
			c.SetDeadline(time.Now().Add(time.Second))
			s.SetDeadline(time.Now().Add(time.Second))
			c.CancelRead(0x100)
			select {
			case <-s.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("STOP_SENDING not observed")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(s); err != nil {
				t.Fatal(err)
			}
			q := &closeErrorTracker{Conn: wrap(s, server)}
			err := xnet.Pipe(context.Background(), q, &eofRW{readErr: io.EOF})
			q.mu.Lock()
			closeErr := q.first
			q.mu.Unlock()
			if closeErr == nil || !errors.Is(err, closeErr) {
				t.Fatalf("Pipe must retain actual QUIC half-close error: Pipe=%v CloseWrite=%v", err, closeErr)
			}
			sibling(t, client, server)
		})
	}
}

type deadlineGate struct {
	mu               sync.Mutex
	closeEntered     chan struct{}
	releaseClose     chan struct{}
	abortObserved    chan struct{}
	readBlocked      chan struct{}
	readOnce         sync.Once
	abortOnce        sync.Once
	futureAfterAbort bool
	aborted          bool
}

func (c *deadlineGate) Read([]byte) (int, error)    { <-c.readBlocked; return 0, os.ErrDeadlineExceeded }
func (c *deadlineGate) Write(p []byte) (int, error) { return len(p), nil }
func (c *deadlineGate) Close() error                { return nil }
func (c *deadlineGate) CloseRead() error            { return nil }
func (c *deadlineGate) CloseWrite() error           { close(c.closeEntered); <-c.releaseClose; return nil }
func (c *deadlineGate) SetReadDeadline(at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.aborted && at.After(time.Now().Add(time.Second)) {
		c.futureAfterAbort = true
	}
	return nil
}
func (c *deadlineGate) SetDeadline(at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.aborted = true
	c.readOnce.Do(func() { close(c.readBlocked) })
	c.abortOnce.Do(func() { close(c.abortObserved) })
	return nil
}
func TestActualAbortDeadlineCannotBeOverwrittenByHalfCloseGrace(t *testing.T) {
	a := &eofRW{readErr: io.EOF}
	b := &deadlineGate{closeEntered: make(chan struct{}), releaseClose: make(chan struct{}), abortObserved: make(chan struct{}), readBlocked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- xnet.Pipe(ctx, a, b, xnet.WithReadTimeout(time.Hour)) }()
	select {
	case <-b.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("normal EOF did not reach half-close")
	}
	cancel()
	select {
	case <-b.abortObserved:
	case <-time.After(time.Second):
		t.Fatal("abort deadline not reached")
	}
	close(b.releaseClose)
	waitPipe(t, done, context.Canceled)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.futureAfterAbort {
		t.Fatal("10-second half-close or idle deadline overwrote abort")
	}
}

func TestActualPipeCancelReleasesStreamCredit(t *testing.T) {
	for name, wrap := range map[string]adapterFactory{"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn} {
		t.Run(name, func(t *testing.T) {
			client, server := pair(t)
			for i := 0; i < 24; i++ {
				_, s := streams(t, client, server)
				q := metrics.WrapConn("review", wrap(s, server))
				a, b := tcpPair(t)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- xnet.Pipe(ctx, q, a) }()
				cancel()
				waitPipe(t, done, context.Canceled)
				_ = b.Close()
			}
			sibling(t, client, server)
		})
	}
}
