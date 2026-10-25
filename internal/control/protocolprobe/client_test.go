package protocolprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture tests process ownership and cancellation over real sockets.
// Real Xray routing and Reality identity isolation live in integration/xray.
type sharedFixture struct {
	starts  atomic.Int64
	checks  atomic.Int64
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
	fail    atomic.Bool
}

func newSharedFixture(t *testing.T) (*Client, *sharedFixture, Spec) {
	t.Helper()
	f := &sharedFixture{entered: make(chan struct{}, 100), release: make(chan struct{})}
	validate, start := validateXray, startXray
	t.Cleanup(func() { validateXray, startXray = validate, start })
	validateXray = func(context.Context, string, string) error {
		if f.fail.Load() {
			return ErrUnhealthy
		}
		return nil
	}
	startXray = f.start
	binary := filepath.Join(t.TempDir(), "xray")
	if err := os.WriteFile(binary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewClient(binary)
	t.Cleanup(client.Close)
	spec := validSpec()
	spec.XrayBinary = binary
	return client, f, spec
}

func (f *sharedFixture) start(ctx context.Context, _ string, path string) (probeProcess, error) {
	var cfg struct {
		Inbounds []struct {
			Port     int `json:"port"`
			Settings struct {
				Accounts []struct{ User, Pass string } `json:"accounts"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return probeProcess{}, err
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return probeProcess{}, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Inbounds[0].Port)))
	if err != nil {
		return probeProcess{}, err
	}
	accounts := map[string]string{}
	for _, account := range cfg.Inbounds[0].Settings.Accounts {
		accounts[account.User] = account.Pass
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	stopCtx, cancel := context.WithCancel(ctx)
	f.starts.Add(1)
	go func() { <-stopCtx.Done(); _ = listener.Close() }()
	go func() {
		defer close(done)
		defer wg.Wait()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				stop := context.AfterFunc(stopCtx, func() { _ = conn.Close() })
				defer stop()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				var hello [3]byte
				if _, err := io.ReadFull(conn, hello[:]); err != nil || hello != [3]byte{5, 1, 2} {
					return
				}
				_, _ = conn.Write([]byte{5, 2})
				var header [2]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 1 {
					return
				}
				user := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, user); err != nil {
					return
				}
				var n [1]byte
				if _, err := io.ReadFull(conn, n[:]); err != nil {
					return
				}
				password := make([]byte, int(n[0]))
				if _, err := io.ReadFull(conn, password); err != nil || accounts[string(user)] != string(password) {
					return
				}
				_, _ = conn.Write([]byte{1, 0})
				var request [5]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil {
					return
				} // readiness stops here
				if request != [5]byte{5, 1, 0, 3, byte(len("echo.example.com"))} {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(request[4])+2); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
				var challenge [32]byte
				if _, err := io.ReadFull(conn, challenge[:]); err != nil {
					return
				}
				f.checks.Add(1)
				if f.block.Load() {
					f.entered <- struct{}{}
					select {
					case <-f.release:
					case <-stopCtx.Done():
						return
					}
				}
				_, _ = conn.Write(challenge[:])
			}()
		}
	}()
	return probeProcess{done: done, stop: cancel}, nil
}

func TestSharedClientReuseFreshChallengeAndCrashRecovery(t *testing.T) {
	c, f, spec := newSharedFixture(t)
	other := spec
	other.DialHost = "second.example.com"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := c.Prepare(ctx, []Spec{spec, other}); err != nil {
		t.Fatal(err)
	}
	cancel()
	first := c.current
	invalid := spec
	invalid.ShortID = "abc"
	if err := c.Prepare(context.Background(), []Spec{other, spec, spec, invalid}); err != nil {
		t.Fatal(err)
	}
	if c.current != first || f.starts.Load() != 1 {
		t.Fatal("same target set restarted client")
	}
	var previous time.Time
	for i := 0; i < 3; i++ {
		spec.EvidenceKey = strconv.Itoa(i)
		at, err := c.Check(context.Background(), spec)
		if err != nil || at.IsZero() || at.Before(previous) {
			t.Fatal("fresh challenge failed", err)
		}
		previous = at
	}
	if f.checks.Load() != 3 {
		t.Fatal("cached success renewed health")
	}
	first.process.stop()
	select {
	case <-first.process.done:
	case <-time.After(time.Second):
		t.Fatal("process did not stop")
	}
	if err := c.Prepare(context.Background(), []Spec{spec, other}); err != nil {
		t.Fatal(err)
	}
	if f.starts.Load() != 2 || c.current == first {
		t.Fatal("crashed client not replaced")
	}
	if _, err := os.Stat(first.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired credentials not removed")
	}
	if _, err := c.Check(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestSharedClientFailedReplacementPreservesOldAndRetiresAfterCheck(t *testing.T) {
	c, f, spec := newSharedFixture(t)
	if err := c.Prepare(context.Background(), []Spec{spec}); err != nil {
		t.Fatal(err)
	}
	old := c.current
	other := spec
	other.DialHost = "second.example.com"
	f.fail.Store(true)
	if err := c.Prepare(context.Background(), []Spec{other}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if c.current != old {
		t.Fatal("failed preparation destroyed old client")
	}
	if _, err := c.Check(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f.fail.Store(false)
	f.block.Store(true)
	result := make(chan error, 1)
	go func() { _, err := c.Check(context.Background(), spec); result <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("challenge did not start")
	}
	if err := c.Prepare(context.Background(), []Spec{other}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.process.done:
		t.Fatal("replacement interrupted in-flight challenge")
	default:
	}
	close(f.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("challenge did not finish")
	}
	if _, err := os.Stat(old.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old credentials not cleaned")
	}
	if at, err := c.Check(context.Background(), spec); err == nil || !at.IsZero() {
		t.Fatal("removed target accepted")
	}
	c.Close()
	if err := c.Prepare(context.Background(), []Spec{spec}); !errors.Is(err, context.Canceled) {
		t.Fatal("closed client restarted")
	}
}

func TestSharedClientCancellationAndCloseDoNotLeakCredentials(t *testing.T) {
	c, f, spec := newSharedFixture(t)
	if err := c.Prepare(context.Background(), []Spec{spec}); err != nil {
		t.Fatal(err)
	}
	instance := c.current
	f.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.Check(ctx, spec); result <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("challenge did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled check passed")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt read")
	}
	c.Close()
	if _, err := os.Stat(instance.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed credentials not removed")
	}
}

func TestSharedClientBoundsConcurrentHandshakes(t *testing.T) {
	c, f, spec := newSharedFixture(t)
	if err := c.Prepare(context.Background(), []Spec{spec}); err != nil {
		t.Fatal(err)
	}
	f.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	var passed atomic.Int64
	for i := 0; i < NetworkChecks*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Check(ctx, spec); err == nil {
				passed.Add(1)
			}
		}()
	}
	for i := 0; i < NetworkChecks; i++ {
		select {
		case <-f.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded workers did not start")
		}
	}
	// Once the first wave is held at echo, excess callers must stay outside
	// the socket handshake rather than creating more process/network work.
	select {
	case <-f.entered:
		t.Fatal("network concurrency bound exceeded")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	wg.Wait()
	if passed.Load() != 0 {
		t.Fatal("cancelled challenges granted health")
	}
}
