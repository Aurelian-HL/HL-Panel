package quic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coredialer "github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

// Observe a real peer-received STREAMS_BLOCKED frame. The channel is a causal
// barrier proving the tested request reached OpenStreamSync's quota wait.
type blockedTrace struct {
	once    sync.Once
	blocked chan struct{}
}

func (b *blockedTrace) AddProducer() qlogwriter.Recorder { return b }
func (b *blockedTrace) SupportsSchemas(string) bool      { return true }
func (b *blockedTrace) Close() error                     { return nil }
func (b *blockedTrace) RecordEvent(e qlogwriter.Event) {
	if p, ok := e.(qlog.PacketReceived); ok {
		for _, f := range p.Frames {
			if blocked, ok := f.Frame.(*qlog.StreamsBlockedFrame); ok && blocked.StreamLimit == 2 {
				b.once.Do(func() { close(b.blocked) })
			}
		}
	}
}

type udpDialer struct {
	t     *testing.T
	calls atomic.Int32
}

func (u *udpDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "udp" || addr != "" {
		u.t.Fatalf("unexpected socket request %q %q", network, addr)
	}
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err == nil {
		u.calls.Add(1)
		u.t.Cleanup(func() { c.Close() })
	}
	return c, err
}

type fixture struct {
	t       *testing.T
	ln      *quic.Listener
	d       *quicDialer
	udp     *udpDialer
	trace   *blockedTrace
	addr    string
	session *quicSession
	server  *quic.Conn
}

func fixtureQUIC(t *testing.T) *fixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	trace := &blockedTrace{blocked: make(chan struct{})}
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"cancel-review"}}, &quic.Config{MaxIncomingStreams: 2, Tracer: func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace { return trace }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	d := NewDialer(coredialer.LoggerOption(logger.LoggerGroup()), coredialer.TLSConfigOption(&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"cancel-review"}})).(*quicDialer)
	if err := d.Init(nil); err != nil {
		t.Fatal(err)
	}
	u := &udpDialer{t: t}
	f := &fixture{t: t, ln: ln, d: d, udp: u, addr: ln.Addr().String(), trace: trace}
	t.Cleanup(func() {
		d.sessionMutex.Lock()
		defer d.sessionMutex.Unlock()
		for _, s := range d.sessions {
			_ = s.Close()
		}
	})
	return f
}
func (f *fixture) dial(ctx context.Context) (net.Conn, error) {
	return f.d.Dial(ctx, f.addr, coredialer.NetDialerDialOption(f.udp))
}
func (f *fixture) cached() *quicSession {
	f.d.sessionMutex.Lock()
	defer f.d.sessionMutex.Unlock()
	return f.d.sessions[f.addr]
}
func (f *fixture) openFirst() (net.Conn, *quic.Stream) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := f.dial(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	server, err := f.ln.Accept(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	f.server = server
	f.session = f.cached()
	f.t.Cleanup(func() { server.CloseWithError(0, "fixture cleanup") })
	return c, f.acceptStream(ctx, server, c)
}
func (f *fixture) acceptStream(ctx context.Context, server *quic.Conn, c net.Conn) *quic.Stream {
	f.t.Helper()
	if _, err := c.Write([]byte{0x51}); err != nil {
		f.t.Fatal(err)
	}
	s, err := server.AcceptStream(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(s, b); err != nil || b[0] != 0x51 {
		f.t.Fatalf("stream marker %x %v", b, err)
	}
	return s
}
func (f *fixture) open() (net.Conn, *quic.Stream) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := f.dial(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return c, f.acceptStream(ctx, f.server, c)
}
func exchange(t *testing.T, c net.Conn, s *quic.Stream, label string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	c.SetDeadline(deadline)
	s.SetDeadline(deadline)
	for _, side := range []struct {
		w   io.Writer
		r   io.Reader
		msg string
	}{{c, s, label + " client-to-server"}, {s, c, label + " server-to-client"}} {
		if _, err := side.w.Write([]byte(side.msg)); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, len(side.msg))
		if _, err := io.ReadFull(side.r, b); err != nil || string(b) != side.msg {
			t.Fatalf("exchange %q got %q err %v", side.msg, b, err)
		}
	}
	c.SetDeadline(time.Time{})
	s.SetDeadline(time.Time{})
}
func release(t *testing.T, c net.Conn, s *quic.Stream) {
	t.Helper()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	s.SetDeadline(time.Now().Add(2 * time.Second))
	// The parent abort layer makes full Close cancel the receive side. Use the
	// adapter's graceful send FIN while the reverse direction remains readable.
	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(s); err != nil || len(b) != 0 {
		t.Fatalf("server FIN read %x %v", b, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(c); err != nil || len(b) != 0 {
		t.Fatalf("client FIN read %x %v", b, err)
	}
}
func waitDone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: " + what)
	}
}

type dialResult struct {
	c   net.Conn
	err error
}

func blockedDial(f *fixture, mode string) error {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "already-canceled" {
		cancel()
		_, err := f.dial(ctx)
		return err
	}
	var request context.Context = ctx
	var deadlineCancel context.CancelFunc
	if mode == "deadline" {
		request, deadlineCancel = context.WithTimeout(ctx, 500*time.Millisecond)
		defer deadlineCancel()
	}
	result := make(chan dialResult, 1)
	go func() { c, e := f.dial(request); result <- dialResult{c, e} }()
	waitDone(f.t, f.trace.blocked, "peer received STREAMS_BLOCKED")
	select {
	case r := <-result:
		f.t.Fatalf("Dial returned before cancellation: %v", r.err)
	default:
	}
	if mode != "deadline" {
		cancel()
	}
	select {
	case r := <-result:
		if r.c != nil {
			f.t.Fatal("failed Dial returned nonnil conn")
		}
		return r.err
	case <-time.After(3 * time.Second):
		f.t.Fatal("canceled Dial remained blocked")
		return nil
	}
}
func TestDialCancelBaselineClosesSibling(t *testing.T) {
	f := fixtureQUIC(t)
	c, s := f.openFirst()
	exchange(t, c, s, "before")
	_, _ = f.open()
	err := blockedDial(f, "cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context canceled got %v", err)
	}
	if !f.session.IsClosed() {
		t.Fatal("baseline did not close shared session")
	}
	if f.cached() != nil {
		t.Fatal("baseline left cache")
	}
	waitDone(t, f.server.Context().Done(), "peer sees shared connection close")
	s.SetReadDeadline(time.Now().Add(time.Second))
	_, err = s.Read(make([]byte, 1))
	var app *quic.ApplicationError
	if !errors.As(err, &app) {
		t.Fatalf("sibling want ApplicationError got %v", err)
	}
	t.Logf("baseline proof: one canceled Dial closed established sibling: %v; sockets=%d", err, f.udp.calls.Load())
}
func TestDialCancellationPreservesSharedSession(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "already-canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := fixtureQUIC(t)
			c, s := f.openFirst()
			exchange(t, c, s, "before")
			holder, held := f.open()
			err := blockedDial(f, mode)
			want := context.Canceled
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("want %v got %v", want, err)
			}
			if f.session.IsClosed() || f.cached() != f.session {
				t.Fatal("request error closed or evicted healthy shared session")
			}
			exchange(t, c, s, "after request failure")
			release(t, holder, held)
			next, peer := f.open()
			exchange(t, next, peer, "after credit restored")
			exchange(t, c, s, "original sibling still alive")
			if f.cached() != f.session || f.udp.calls.Load() != 1 {
				t.Fatalf("unexpected reconnect sockets=%d", f.udp.calls.Load())
			}
			release(t, next, peer)
			release(t, c, s)
		})
	}
}
func TestDialDeadSharedSessionReconnects(t *testing.T) {
	f := fixtureQUIC(t)
	c, s := f.openFirst()
	exchange(t, c, s, "before peer close")
	_, _ = f.open()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan dialResult, 1)
	go func() { c, e := f.dial(ctx); result <- dialResult{c, e} }()
	waitDone(t, f.trace.blocked, "blocked Dial before peer close")
	if err := f.server.CloseWithError(42, "test peer shutdown"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		var app *quic.ApplicationError
		if r.c != nil || !errors.As(r.err, &app) {
			t.Fatalf("bad session Dial result %v %v", r.c, r.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// OpenStreamSync may fail before quic-go cancels Conn.Context.
	// Verify eventual close and replacement by the next real Dial.
	waitDone(t, f.session.session.Context().Done(), "client session close completes")
	if !f.session.IsClosed() {
		t.Fatal("closed session not observable")
	}
	fresh, err := f.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server, err := f.ln.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.CloseWithError(0, "fixture cleanup") })
	stream := f.acceptStream(ctx, server, fresh)
	exchange(t, fresh, stream, "reconnected")
	if f.cached() == f.session || f.udp.calls.Load() != 2 {
		t.Fatalf("wanted replacement session sockets=%d", f.udp.calls.Load())
	}
	release(t, fresh, stream)
}

func TestDialFailedOldSessionCannotEvictReplacement(t *testing.T) {
	f := fixtureQUIC(t)
	_, _ = f.openFirst()
	_, _ = f.open()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan dialResult, 1)
	go func() { c, e := f.dial(ctx); result <- dialResult{c, e} }()
	waitDone(t, f.trace.blocked, "old Dial quota wait")
	// Hold the actual cache lock while the peer kills the old session. Build a
	// real replacement before letting the old failed Dial perform its cleanup.
	f.d.sessionMutex.Lock()
	locked := true
	defer func() {
		if locked {
			f.d.sessionMutex.Unlock()
		}
	}()
	if err := f.server.CloseWithError(43, "old session shutdown"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, f.session.session.Context().Done(), "old client session shutdown")
	pc, err := f.udp.Dial(ctx, "udp", "")
	if err != nil {
		t.Fatal(err)
	}
	addr, err := net.ResolveUDPAddr("udp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := f.d.initSession(ctx, addr, pc.(net.PacketConn))
	if err != nil {
		t.Fatal(err)
	}
	f.d.sessions[f.addr] = replacement
	f.d.sessionMutex.Unlock()
	locked = false
	server, err := f.ln.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.CloseWithError(0, "fixture cleanup") })
	select {
	case r := <-result:
		if r.c != nil || r.err == nil {
			t.Fatalf("old Dial result %v %v", r.c, r.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.cached() != replacement || replacement.IsClosed() {
		t.Fatal("old failure removed or closed replacement")
	}
	c, err := f.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := f.acceptStream(ctx, server, c)
	exchange(t, c, s, "cache replacement survives")
	release(t, c, s)
	if f.udp.calls.Load() != 2 {
		t.Fatalf("wanted 2 sockets got %d", f.udp.calls.Load())
	}
}
