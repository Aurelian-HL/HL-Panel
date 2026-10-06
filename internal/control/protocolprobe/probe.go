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

// Check returns a verification time only after Xray has completed a Reality
// Vision connection and the configured target echoed a fresh challenge. A
// caller must separately authenticate and bind any resulting observation to a
// rule revision and applied node configuration before publishing health.
func Check(ctx context.Context, spec Spec) (time.Time, error) {
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return time.Time{}, ErrUnhealthy
	}
	socksPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config, err := clientConfig(spec, socksPort)
	if err != nil {
		return time.Time{}, err
	}
	privateDir, err := os.MkdirTemp("", "hl-protocol-probe-")
	if err != nil {
		return time.Time{}, ErrUnhealthy
	}
	defer os.RemoveAll(privateDir)
	if err := os.Chmod(privateDir, 0o700); err != nil {
		return time.Time{}, ErrUnhealthy
	}
	configPath := filepath.Join(privateDir, "client.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return time.Time{}, ErrUnhealthy
	}
	validate := exec.CommandContext(ctx, binary, "run", "-test", "-config", configPath)
	validate.Stdout, validate.Stderr = io.Discard, io.Discard
	if err := validate.Run(); err != nil {
		return time.Time{}, ErrUnhealthy
	}
	process := exec.CommandContext(ctx, binary, "run", "-config", configPath)
	process.Stdout, process.Stderr = io.Discard, io.Discard
	if err := process.Start(); err != nil {
		return time.Time{}, ErrUnhealthy
	}
	done := make(chan struct{})
	go func() { _ = process.Wait(); close(done) }()
	defer func() {
		// Xray may still be starting when the probe fails or its deadline fires.
		// Killing it is best effort, but waiting forever here would permanently
		// stall the runner after the first failed probe.
		_ = process.Process.Kill()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	for {
		if ctx.Err() != nil {
			return time.Time{}, ErrUnhealthy
		}
		select {
		case <-done:
			return time.Time{}, ErrUnhealthy
		default:
		}
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if dialErr == nil {
			defer conn.Close()
			if err := echoThroughSOCKS(ctx, conn, spec.EchoHost, spec.EchoPort); err != nil {
				return time.Time{}, ErrUnhealthy
			}
			return time.Now().UTC(), nil
		}
		select {
		case <-ctx.Done():
			return time.Time{}, ErrUnhealthy
		case <-done:
			return time.Time{}, ErrUnhealthy
		case <-time.After(25 * time.Millisecond):
		}
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
