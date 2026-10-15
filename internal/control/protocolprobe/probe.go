package protocolprobe

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var ErrUnhealthy = errors.New("VLESS protocol probe failed")

// Each check starts a validation process and a short-lived Xray process. The
// scheduler limits network workers, but without a second limit a large pool
// can still fork enough Xray processes to starve the control plane itself.
// Keep this guard package-wide so multiple runners cannot bypass the limit.
// It must match the protocol scheduler's worker budget: a smaller fixed value
// leaves workers queued inside Check and can make a large pool miss its health
// lease renewal window even when every node is healthy.
const maxConcurrentChecks = 8
const maxPortStartAttempts = 3

var checkSlots = make(chan struct{}, maxConcurrentChecks)

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
func Check(ctx context.Context, spec Spec) (time.Time, error) {
	if err := spec.validate(); err != nil {
		return time.Time{}, err
	}
	select {
	case checkSlots <- struct{}{}:
		defer func() { <-checkSlots }()
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
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
		if err := validateXray(ctx, binary, configPath); err != nil {
			return time.Time{}, ErrUnhealthy
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
			return time.Now().UTC(), nil
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
