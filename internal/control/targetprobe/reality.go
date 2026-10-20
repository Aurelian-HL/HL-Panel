package targetprobe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type RealityRequest struct {
	ServerName  string `json:"server_name"`
	Destination string `json:"destination"`
}

type RealityResult struct {
	RealityRequest
	Compatible  bool      `json:"compatible"`
	LatencyMS   int64     `json:"latency_ms"`
	TLSVersion  string    `json:"tls_version,omitempty"`
	KeyExchange string    `json:"key_exchange,omitempty"`
	ALPN        string    `json:"alpn,omitempty"`
	Source      string    `json:"source"`
	CheckedAt   time.Time `json:"checked_at"`
	ErrorCode   string    `json:"error_code,omitempty"`
	Message     string    `json:"message,omitempty"`
}

// RealityService checks a decoy's certificate and TLS compatibility from the
// control plane. It neither activates rules nor attests node/client health.
type RealityService struct {
	timeout   time.Duration
	slots     chan struct{}
	lookup    func(context.Context, string, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
}

func NewReality(timeout time.Duration) *RealityService {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = DefaultTimeout
	}
	return &RealityService{timeout: timeout, slots: make(chan struct{}, 8),
		lookup: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{}).DialContext,
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
			CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2", "http/1.1"}},
	}
}

func (s *RealityService) Probe(ctx context.Context, input RealityRequest) (RealityResult, error) {
	serverName, err := serviceaddress.NormalizeHost(strings.TrimSpace(input.ServerName))
	if err != nil {
		return RealityResult{}, fmt.Errorf("%w: invalid Reality SNI", faults.ErrValidation)
	}
	destination := strings.TrimSpace(input.Destination)
	if destination == "" {
		destination = net.JoinHostPort(serverName, "443")
	}
	host, rawPort, err := net.SplitHostPort(destination)
	if err != nil {
		return RealityResult{}, fmt.Errorf("%w: Reality Target must be host:port", faults.ErrValidation)
	}
	host, err = serviceaddress.NormalizeHost(host)
	port, portErr := strconv.Atoi(rawPort)
	if err != nil || portErr != nil || port < 1 || port > 65535 {
		return RealityResult{}, fmt.Errorf("%w: invalid Reality Target", faults.ErrValidation)
	}
	result := RealityResult{RealityRequest: RealityRequest{serverName, net.JoinHostPort(host, strconv.Itoa(port))}, Source: "control_plane", CheckedAt: time.Now().UTC()}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return RealityResult{}, fmt.Errorf("%w: Reality probe busy; retry shortly", faults.ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	started := time.Now()
	addresses, err := s.lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		result.ErrorCode, result.Message = "dns_failure", "目标域名解析失败"
		if ctx.Err() != nil {
			result.ErrorCode, result.Message = "timeout", "检测超时"
		}
		result.LatencyMS = time.Since(started).Milliseconds()
		return result, nil
	}
	// Pin the resolved public address, preventing private-network probes and a
	// second DNS lookup from changing the destination after validation.
	for _, address := range addresses {
		if !publicProbeAddress(address) {
			result.ErrorCode, result.Message = "non_public_target", "仅支持检测公网目标"
			return result, nil
		}
	}
	var lastErr error
	for i, address := range addresses {
		// Bound each address attempt so a broken IPv6 path cannot consume the
		// whole budget before an available IPv4 address is attempted.
		remaining := s.timeout - time.Since(started)
		if remaining <= 0 {
			lastErr = context.DeadlineExceeded
			break
		}
		attempt, stop := context.WithTimeout(ctx, remaining/time.Duration(len(addresses)-i))
		conn, dialErr := s.dial(attempt, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
		if dialErr != nil {
			lastErr = dialErr
			stop()
			continue
		}
		config := s.tlsConfig.Clone()
		config.ServerName = serverName
		secure := tls.Client(conn, config)
		handshakeErr := secure.HandshakeContext(attempt)
		if handshakeErr == nil {
			state := secure.ConnectionState()
			result.Compatible, result.TLSVersion, result.KeyExchange, result.ALPN = true, "TLS 1.3", "X25519", state.NegotiatedProtocol
			_ = conn.Close()
			stop()
			break
		}
		_ = conn.Close()
		stop()
		lastErr = handshakeErr
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	if !result.Compatible {
		result.ErrorCode, result.Message = "tls_failed", "TLS 1.3 / X25519 握手或证书校验失败"
		if ctx.Err() != nil || errorsIsTimeout(lastErr) || lastErr == context.DeadlineExceeded {
			result.ErrorCode, result.Message = "timeout", "检测超时"
		}
	}
	return result, nil
}

func publicProbeAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, blocked := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(blocked).Contains(address) {
			return false
		}
	}
	return true
}
