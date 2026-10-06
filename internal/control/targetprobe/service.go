// Package targetprobe performs an explicit, short-lived TCP reachability check
// for a forwarding target. A successful probe only describes connectivity from
// the control-plane host; it does not activate a rule or prove the data path.
package targetprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

const DefaultTimeout = 3 * time.Second

type Request struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Result struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Address   string `json:"address"`
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latency_ms"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type Service struct {
	timeout time.Duration
	dial    func(context.Context, string, string) (net.Conn, error)
}

func New(timeout time.Duration) *Service {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = DefaultTimeout
	}
	return &Service{timeout: timeout, dial: (&net.Dialer{Timeout: timeout}).DialContext}
}

func (s *Service) Probe(ctx context.Context, input Request) (Result, error) {
	host, err := serviceaddress.NormalizeHost(input.Host)
	if err != nil {
		return Result{}, fmt.Errorf("%w: target host is invalid", faults.ErrValidation)
	}
	if input.Port < 1 || input.Port > 65535 {
		return Result{}, fmt.Errorf("%w: target port must be 1 to 65535", faults.ErrValidation)
	}
	address := net.JoinHostPort(host, strconv.Itoa(input.Port))
	result := Result{Host: host, Port: input.Port, Address: address}
	probeContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	started := time.Now()
	conn, err := s.dial(probeContext, "tcp", address)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err == nil {
		result.Reachable = true
		_ = conn.Close()
		return result, nil
	}
	result.ErrorCode, result.Message = classifyError(err)
	return result, nil
}

func classifyError(err error) (string, string) {
	if errorsIsTimeout(err) {
		return "timeout", "连接超时"
	}
	if strings.Contains(strings.ToLower(err.Error()), "no such host") {
		return "dns_failure", "域名解析失败"
	}
	if strings.Contains(strings.ToLower(err.Error()), "refused") {
		return "connection_refused", "目标端口拒绝连接"
	}
	return "unreachable", "目标不可达"
}

// Kept local so probe error classification never exposes raw dialer details.
func errorsIsTimeout(err error) bool {
	type timeoutError interface{ Timeout() bool }
	var value timeoutError
	return errors.As(err, &value) && value.Timeout()
}
