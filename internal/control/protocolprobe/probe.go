package protocolprobe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnhealthy = errors.New("VLESS protocol probe failed")

// Each check starts a validation process and a short-lived Xray process. The
// scheduler limits network workers, but without a second limit a large pool
// can still fork enough Xray processes to starve the control plane itself.
// Keep this guard package-wide so multiple runners cannot bypass the limit.
// Keep the existing three-process budget: reducing it further can expire
// healthy targets in large pools. Cache successful static validation to
// remove redundant forks without sacrificing network-check throughput.
const maxConcurrentChecks = 3
const maxPortStartAttempts = 3

// A successful Reality check remains useful for one scheduler turn. The
// runner normally schedules a target every 30 seconds, so this avoids a
// second Xray fork for the same unchanged target while leaving enough margin
// before the 90-second health lease expires. Failed checks are never cached.
const successfulProbeCacheTTL = 35 * time.Second

// Xray's `run -test` validates the static config shape, but it is itself a
// process fork. The local SOCKS port is intentionally absent from the cache
// key, so a transient port collision can retry without repeating validation.
// Runtime connectivity is still checked by the short-lived Xray process.
const successfulValidationCacheTTL = 10 * time.Minute

var checkSlots = make(chan struct{}, maxConcurrentChecks)

type cachedSuccess struct {
	verifiedAt time.Time
	expiresAt  time.Time
}

var successCache = struct {
	sync.Mutex
	entries map[[sha256.Size]byte]cachedSuccess
}{entries: make(map[[sha256.Size]byte]cachedSuccess)}

var validationCache = struct {
	sync.Mutex
	entries map[[sha256.Size]byte]time.Time
}{entries: make(map[[sha256.Size]byte]time.Time)}

// A refresh notification and a periodic refresh can describe the same target
// at the same time. Share one in-flight check for an identical target so those
// paths cannot fork duplicate Xray processes before the success cache is set.
type probeFlight struct {
	done       chan struct{}
	verifiedAt time.Time
	err        error
}

var probeFlights = struct {
	sync.Mutex
	entries map[[sha256.Size]byte]*probeFlight
}{entries: make(map[[sha256.Size]byte]*probeFlight)}

type probeProcess struct {
	done <-chan struct{}
	stop func()
}

var validateXray = validateXrayProcess
var startXray = startXrayProcess

// Check returns a verification time only after Xray has completed a Reality
// Vision connection and the configured target echoed a fresh challenge. A
// caller must separately authenticate and bind any resulting observation to a
// rule revision and applied node configuration before publishing health.
func Check(ctx context.Context, spec Spec) (verifiedAt time.Time, err error) {
	if err := spec.validate(); err != nil {
		return time.Time{}, err
	}
	binary := filepath.Clean(strings.TrimSpace(spec.XrayBinary))
	if !filepath.IsAbs(binary) {
		return time.Time{}, ErrInvalidSpec
	}
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return time.Time{}, ErrInvalidSpec
	}
	cacheKey := probeCacheKey(spec, binary, info)
	if cachedAt, ok := cachedProbeSuccess(cacheKey, time.Now().UTC()); ok {
		verifiedAt = cachedAt
		return verifiedAt, nil
	}
	flight, owner := beginProbeFlight(cacheKey)
	if !owner {
		select {
		case <-flight.done:
			return flight.verifiedAt, flight.err
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		}
	}
	defer func() {
		finishProbeFlight(cacheKey, flight, verifiedAt, err)
	}()
	// The owner may have waited while another caller populated the cache just
	// before it acquired the flight. Avoid starting a second Xray process.
	if verifiedAt, ok := cachedProbeSuccess(cacheKey, time.Now().UTC()); ok {
		return verifiedAt, nil
	}
	select {
	case checkSlots <- struct{}{}:
		defer func() { <-checkSlots }()
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	privateDir, err := os.MkdirTemp("", "hl-protocol-probe-")
	if err != nil {
		return time.Time{}, ErrUnhealthy
	}
	defer os.RemoveAll(privateDir)
	if err := os.Chmod(privateDir, 0o700); err != nil {
		return time.Time{}, ErrUnhealthy
	}
	configPath := filepath.Join(privateDir, "client.json")
	for attempt := 0; attempt < maxPortStartAttempts; attempt++ {
		socksPort, err := reserveLoopbackPort()
		if err != nil {
			return time.Time{}, ErrUnhealthy
		}
		config, err := clientConfig(spec, socksPort)
		if err != nil {
			return time.Time{}, err
		}
		if err := os.WriteFile(configPath, config, 0o600); err != nil {
			return time.Time{}, ErrUnhealthy
		}
		if shouldValidateXray(cacheKey, time.Now().UTC()) {
			if err := validateXray(ctx, binary, configPath); err != nil {
				return time.Time{}, ErrUnhealthy
			}
			rememberXrayValidation(cacheKey, time.Now().UTC())
		}
		process, err := startXray(ctx, binary, configPath)
		if err != nil {
			if ctx.Err() != nil {
				return time.Time{}, ErrUnhealthy
			}
			continue
		}
		verified, exitedBeforeReady := waitForSOCKS(ctx, process.done, socksPort, spec)
		stopProbeProcess(process)
		if verified {
			verifiedAt = time.Now().UTC()
			rememberProbeSuccess(cacheKey, verifiedAt)
			return verifiedAt, nil
		}
		if !exitedBeforeReady || ctx.Err() != nil {
			return time.Time{}, ErrUnhealthy
		}
		// Xray can lose the short reservation window between closing the
		// allocator listener and binding its SOCKS inbound. An early process
		// exit is the observable symptom; use a fresh port rather than turning
		// that transient collision into a node health failure.
	}
	return time.Time{}, ErrUnhealthy
}

func beginProbeFlight(key [sha256.Size]byte) (*probeFlight, bool) {
	probeFlights.Lock()
	defer probeFlights.Unlock()
	if existing := probeFlights.entries[key]; existing != nil {
		return existing, false
	}
	flight := &probeFlight{done: make(chan struct{})}
	probeFlights.entries[key] = flight
	return flight, true
}

func finishProbeFlight(key [sha256.Size]byte, flight *probeFlight, verifiedAt time.Time, err error) {
	probeFlights.Lock()
	flight.verifiedAt = verifiedAt
	flight.err = err
	delete(probeFlights.entries, key)
	close(flight.done)
	probeFlights.Unlock()
}

func probeCacheKey(spec Spec, binary string, info os.FileInfo) [sha256.Size]byte {
	// Delimit fields so values cannot collide when concatenated. The binary
	// metadata invalidates the cache after an engine upgrade or replacement.
	value := strings.Join([]string{
		binary,
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
		strconv.FormatUint(uint64(info.Mode()), 10),
		spec.DialHost, strconv.Itoa(spec.DialPort), spec.UUID, spec.ServerName,
		spec.PublicKey, spec.ShortID, spec.EchoHost, strconv.Itoa(spec.EchoPort),
	}, "\x00")
	return sha256.Sum256([]byte(value))
}

func cachedProbeSuccess(key [sha256.Size]byte, now time.Time) (time.Time, bool) {
	successCache.Lock()
	defer successCache.Unlock()
	entry, ok := successCache.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(successCache.entries, key)
		}
		return time.Time{}, false
	}
	// Treat the cached validation as fresh for the current health lease. The
	// cache is bounded to a short window and keyed by all connection material.
	return now, true
}

func rememberProbeSuccess(key [sha256.Size]byte, verifiedAt time.Time) {
	successCache.Lock()
	defer successCache.Unlock()
	if len(successCache.entries) >= 4096 {
		for oldKey, entry := range successCache.entries {
			if !verifiedAt.Before(entry.expiresAt) {
				delete(successCache.entries, oldKey)
			}
		}
		if len(successCache.entries) >= 4096 {
			return
		}
	}
	successCache.entries[key] = cachedSuccess{verifiedAt: verifiedAt, expiresAt: verifiedAt.Add(successfulProbeCacheTTL)}
}

// resetProbeCacheForTests prevents package-level state from coupling tests.
func resetProbeCacheForTests() {
	successCache.Lock()
	defer successCache.Unlock()
	successCache.entries = make(map[[sha256.Size]byte]cachedSuccess)
	validationCache.Lock()
	defer validationCache.Unlock()
	validationCache.entries = make(map[[sha256.Size]byte]time.Time)
	probeFlights.Lock()
	defer probeFlights.Unlock()
	probeFlights.entries = make(map[[sha256.Size]byte]*probeFlight)
}

func shouldValidateXray(key [sha256.Size]byte, now time.Time) bool {
	validationCache.Lock()
	defer validationCache.Unlock()
	expiresAt, ok := validationCache.entries[key]
	if !ok || !now.Before(expiresAt) {
		if ok {
			delete(validationCache.entries, key)
		}
		return true
	}
	return false
}

func rememberXrayValidation(key [sha256.Size]byte, now time.Time) {
	validationCache.Lock()
	defer validationCache.Unlock()
	if len(validationCache.entries) >= 4096 {
		for oldKey, expiresAt := range validationCache.entries {
			if !now.Before(expiresAt) {
				delete(validationCache.entries, oldKey)
			}
		}
		if len(validationCache.entries) >= 4096 {
			return
		}
	}
	validationCache.entries[key] = now.Add(successfulValidationCacheTTL)
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func validateXrayProcess(ctx context.Context, binary, configPath string) error {
	validate := exec.CommandContext(ctx, binary, "run", "-test", "-config", configPath)
	validate.Stdout, validate.Stderr = io.Discard, io.Discard
	return validate.Run()
}

func startXrayProcess(ctx context.Context, binary, configPath string) (probeProcess, error) {
	process := exec.CommandContext(ctx, binary, "run", "-config", configPath)
	process.Stdout, process.Stderr = io.Discard, io.Discard
	if err := process.Start(); err != nil {
		return probeProcess{}, err
	}
	done := make(chan struct{})
	go func() { _ = process.Wait(); close(done) }()
	return probeProcess{
		done: done,
		stop: func() { _ = process.Process.Kill() },
	}, nil
}

func waitForSOCKS(ctx context.Context, done <-chan struct{}, port int, spec Spec) (verified, exitedBeforeReady bool) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		if ctx.Err() != nil {
			return false, false
		}
		select {
		case <-done:
			return false, true
		default:
		}
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if dialErr == nil {
			err := echoThroughSOCKS(ctx, conn, spec.EchoHost, spec.EchoPort)
			_ = conn.Close()
			return err == nil, false
		}
		select {
		case <-ctx.Done():
			return false, false
		case <-done:
			return false, true
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func stopProbeProcess(process probeProcess) {
	// Xray may still be starting when the probe fails or its deadline fires.
	// Killing it is best effort, but waiting forever here would permanently
	// stall the runner after the first failed probe.
	if process.stop != nil {
		process.stop()
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
	}
}

func echoThroughSOCKS(ctx context.Context, conn net.Conn, host string, port int) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ErrUnhealthy
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return ErrUnhealthy
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return ErrUnhealthy
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response[:2]); err != nil || response[0] != 5 || response[1] != 0 {
		return ErrUnhealthy
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return ErrUnhealthy
	}
	if _, err := io.ReadFull(conn, response); err != nil || response[0] != 5 || response[1] != 0 {
		return ErrUnhealthy
	}
	var addressLength int
	switch response[3] {
	case 1:
		addressLength = 4
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return ErrUnhealthy
		}
		addressLength = int(length[0])
	case 4:
		addressLength = 16
	default:
		return ErrUnhealthy
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addressLength+2)); err != nil {
		return ErrUnhealthy
	}
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return ErrUnhealthy
	}
	if _, err := conn.Write(challenge); err != nil {
		return ErrUnhealthy
	}
	reply := make([]byte, len(challenge))
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, challenge) {
		return ErrUnhealthy
	}
	return nil
}
