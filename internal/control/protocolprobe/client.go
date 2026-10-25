package protocolprobe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetworkChecks bounds real handshakes, independently of process creation.
// Thousands of rule/node targets share one local client rather than forking
// an Xray process per challenge. This does not change the health lease TTL.
const NetworkChecks = 64

var sharedCheckSlots = make(chan struct{}, NetworkChecks)

// Client owns only a loopback probe process, never the node forwarding engine.
// Prepare replaces its configuration when the authorized target set changes;
// in-flight challenges finish on their original process before it is retired.
type Client struct {
	binary    string
	root      context.Context
	cancel    context.CancelFunc
	prepareMu sync.Mutex
	mu        sync.Mutex
	current   *clientInstance
	closed    bool
}

type clientInstance struct {
	key      [sha256.Size]byte
	info     os.FileInfo
	targets  map[[sha256.Size]byte]string
	password string
	port     int
	process  probeProcess
	cancel   context.CancelFunc
	dir      string
	refs     int // protected by Client.mu
	retired  bool
	once     sync.Once
}

func NewClient(binary string) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{binary: filepath.Clean(strings.TrimSpace(binary)), root: ctx, cancel: cancel}
}

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	old := c.current
	c.current = nil
	cleanup := retireClient(old)
	c.mu.Unlock()
	if cleanup {
		old.dispose()
	}
}

func retireClient(instance *clientInstance) bool {
	if instance == nil {
		return false
	}
	instance.retired = true
	return instance.refs == 0
}

func (instance *clientInstance) dispose() {
	instance.once.Do(func() {
		instance.cancel()
		stopProbeProcess(instance.process)
		_ = os.RemoveAll(instance.dir)
	})
}

// Prepare never publishes health. Invalid individual targets are omitted and
// cannot be checked; they cannot block renewal for the rest of the inventory.
// A failed replacement leaves the existing client available for unchanged rules.
func (c *Client) Prepare(ctx context.Context, specs []Spec) error {
	c.prepareMu.Lock()
	defer c.prepareMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.binary) {
		return ErrInvalidSpec
	}
	info, err := os.Lstat(c.binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return ErrInvalidSpec
	}
	targets := make(map[[sha256.Size]byte]Spec, len(specs))
	for _, spec := range specs {
		if spec.validate() != nil || filepath.Clean(spec.XrayBinary) != c.binary {
			continue
		}
		targets[probeCacheKey(spec, c.binary, info)] = spec
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, hex.EncodeToString(key[:]))
	}
	sort.Strings(keys)
	key := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return context.Canceled
	}
	if current := c.current; current != nil && current.key == key {
		select {
		case <-current.process.done:
		default:
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Unlock()
	var next *clientInstance
	if len(targets) > 0 {
		next, err = c.start(ctx, info, key, targets, keys)
		if err != nil {
			return err
		}
	}
	c.mu.Lock()
	if c.closed || ctx.Err() != nil {
		c.mu.Unlock()
		if next != nil {
			next.dispose()
		}
		return context.Canceled
	}
	old := c.current
	c.current = next
	cleanup := retireClient(old)
	c.mu.Unlock()
	if cleanup {
		old.dispose()
	}
	return nil
}

func (c *Client) start(ctx context.Context, info os.FileInfo, key [sha256.Size]byte, targets map[[sha256.Size]byte]Spec, keys []string) (*clientInstance, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, ErrUnhealthy
	}
	password := hex.EncodeToString(raw[:])
	for attempt := 0; attempt < maxPortStartAttempts; attempt++ {
		dir, err := os.MkdirTemp("", "hl-protocol-client-")
		if err != nil {
			return nil, ErrUnhealthy
		}
		life, cancel := context.WithCancel(c.root)
		instance := &clientInstance{key: key, info: info, targets: make(map[[sha256.Size]byte]string, len(targets)), password: password, dir: dir, cancel: cancel}
		// dispose must also work when validation or process creation fails.
		done := make(chan struct{})
		close(done)
		instance.process.done = done
		port, err := reserveLoopbackPort()
		if err == nil {
			instance.port = port
			var config []byte
			config, err = sharedClientConfig(targets, keys, port, password)
			path := filepath.Join(dir, "client.json")
			if err == nil {
				err = os.Chmod(dir, 0o700)
			}
			if err == nil {
				err = os.WriteFile(path, config, 0o600)
			}
			if err == nil {
				err = validateXray(ctx, c.binary, path)
			}
			if err == nil {
				var process probeProcess
				process, err = startXray(life, c.binary, path)
				if err == nil {
					instance.process = process
					for key := range targets {
						instance.targets[key] = hex.EncodeToString(key[:])
					}
					err = waitForAuthenticatedSOCKS(ctx, instance, keys[0])
				}
			}
		}
		if err == nil {
			return instance, nil
		}
		instance.dispose()
		if ctx.Err() != nil || c.root.Err() != nil {
			return nil, context.Canceled
		}
	}
	return nil, ErrUnhealthy
}

func waitForAuthenticatedSOCKS(ctx context.Context, instance *clientInstance, user string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(instance.port))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-instance.process.done:
			return ErrUnhealthy
		default:
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			deadline := time.Now().Add(time.Second)
			if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
				deadline = limit
			}
			_ = conn.SetDeadline(deadline)
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			err = authenticateSOCKS(conn, user, instance.password)
			stop()
			_ = conn.Close()
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-instance.process.done:
			return ErrUnhealthy
		case <-ticker.C:
		}
	}
}

// Check opens a fresh VLESS connection and random challenge on every call.
// Neither client readiness nor cached configuration grants or renews health.
func (c *Client) Check(ctx context.Context, spec Spec) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if spec.validate() != nil || filepath.Clean(spec.XrayBinary) != c.binary {
		return time.Time{}, ErrInvalidSpec
	}
	select {
	case sharedCheckSlots <- struct{}{}:
		defer func() { <-sharedCheckSlots }()
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
	c.mu.Lock()
	instance := c.current
	if c.closed || instance == nil {
		c.mu.Unlock()
		return time.Time{}, ErrUnhealthy
	}
	user, ok := instance.targets[probeCacheKey(spec, c.binary, instance.info)]
	if !ok {
		c.mu.Unlock()
		return time.Time{}, ErrUnhealthy
	}
	instance.refs++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		instance.refs--
		cleanup := instance.retired && instance.refs == 0
		c.mu.Unlock()
		if cleanup {
			instance.dispose()
		}
	}()
	select {
	case <-instance.process.done:
		return time.Time{}, ErrUnhealthy
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(instance.port)))
	if err != nil {
		return time.Time{}, ErrUnhealthy
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := echoThroughSOCKSAuth(ctx, conn, spec.EchoHost, spec.EchoPort, user, instance.password); err != nil || ctx.Err() != nil {
		return time.Time{}, ErrUnhealthy
	}
	return time.Now().UTC(), nil
}

func sharedClientConfig(targets map[[sha256.Size]byte]Spec, keys []string, port int, password string) ([]byte, error) {
	accounts := make([]any, 0, len(keys))
	outbounds := []any{map[string]any{"tag": "probe-block", "protocol": "blackhole"}}
	routes := make([]any, 0, len(keys))
	for _, name := range keys {
		raw, _ := hex.DecodeString(name)
		var key [sha256.Size]byte
		copy(key[:], raw)
		spec := targets[key]
		// Reuse the exact Reality/flow configuration of the standalone checker.
		data, err := clientConfig(spec, port)
		if err != nil {
			return nil, err
		}
		var single struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal(data, &single); err != nil {
			return nil, ErrInvalidSpec
		}
		single.Outbounds[0]["tag"] = name
		outbounds = append(outbounds, single.Outbounds[0])
		accounts = append(accounts, map[string]any{"user": name, "pass": password})
		routes = append(routes, map[string]any{"type": "field", "inboundTag": []string{"probe-socks"}, "user": []string{name}, "outboundTag": name})
	}
	return json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{
			"tag": "probe-socks", "listen": "127.0.0.1", "port": port,
			"protocol": "socks", "settings": map[string]any{"auth": "password", "accounts": accounts, "udp": false},
		}},
		"outbounds": outbounds,
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": routes},
	})
}
