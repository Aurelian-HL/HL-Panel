package kcp

import (
	"strings"
	"testing"
	"time"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	kcp_util "github.com/go-gost/x/internal/util/kcp"
	xmetadata "github.com/go-gost/x/metadata"
	"github.com/xtaci/smux"
)

const timeoutContractError = "kcp.keepaliveTimeout must be a positive duration at least the effective keepalive interval"

// Upstream metadata mutates DefaultConfig. Snapshot it for isolation; the
// candidate intentionally does not repair that independent upstream behavior.
func timeoutSnapshot(t *testing.T) {
	t.Helper()
	original := *kcp_util.DefaultConfig
	t.Cleanup(func() { *kcp_util.DefaultConfig = original })
}

func timeoutNew(t *testing.T, values map[string]any) (*kcpDialer, error) {
	t.Helper()
	d := NewDialer(
		dialer.LoggerOption(logger.LoggerGroup()),
	).(*kcpDialer)
	err := d.Init(xmetadata.NewMetadata(values))
	return d, err
}

func TestKCPTimeoutEffectiveConfig(t *testing.T) {
	tests := []struct {
		name              string
		values            map[string]any
		interval, timeout time.Duration
	}{
		{"default", nil, 10 * time.Second, 30 * time.Second},
		{"explicit", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "5s"}, 2 * time.Second, 5 * time.Second},
		{"integer", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": 5}, 2 * time.Second, 5 * time.Second},
		{"string_integer", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "5"}, 2 * time.Second, 5 * time.Second},
		{"equal", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "2s"}, 2 * time.Second, 2 * time.Second},
		{"fractional", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "2500ms"}, 2 * time.Second, 2500 * time.Millisecond},
		{"timeout_absent", map[string]any{"kcp.keepalive": 2}, 2 * time.Second, 30 * time.Second},
		{"negative_interval", map[string]any{"kcp.keepalive": -1, "kcp.keepaliveTimeout": "10s"}, 10 * time.Second, 10 * time.Second},
		{"zero_interval", map[string]any{"kcp.keepalive": 0, "kcp.keepaliveTimeout": "10s"}, 10 * time.Second, 10 * time.Second},
		{"upper_case", map[string]any{"KCP.KEEPALIVE": 2, "KCP.KEEPALIVETIMEOUT": "5s"}, 2 * time.Second, 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timeoutSnapshot(t)
			d, err := timeoutNew(t, tc.values)
			if err != nil {
				t.Fatal(err)
			}
			cfg := d.smuxConfig()
			if cfg.KeepAliveInterval != tc.interval || cfg.KeepAliveTimeout != tc.timeout {
				t.Fatalf("effective interval/timeout = %v/%v, want %v/%v", cfg.KeepAliveInterval, cfg.KeepAliveTimeout, tc.interval, tc.timeout)
			}
			if cfg.Version != d.md.config.SmuxVer ||
				cfg.MaxReceiveBuffer != d.md.config.SmuxBuf ||
				cfg.MaxStreamBuffer != d.md.config.StreamBuf {
				t.Fatal("original smux version or buffer mapping changed")
			}
			if err := smux.VerifyConfig(cfg); err != nil {
				t.Fatal(err)
			}
			defaults := smux.DefaultConfig()
			if defaults.KeepAliveInterval != 10*time.Second || defaults.KeepAliveTimeout != 30*time.Second {
				t.Fatal("explicit timeout changed smux defaults")
			}
		})
	}
}

func TestKCPTimeoutInvalidRejectedByInit(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
	}{
		{"nil", map[string]any{"kcp.keepaliveTimeout": nil}},
		{"empty", map[string]any{"kcp.keepaliveTimeout": ""}},
		{"malformed", map[string]any{"kcp.keepaliveTimeout": "private-invalid-value"}},
		{"zero", map[string]any{"kcp.keepaliveTimeout": 0}},
		{"negative", map[string]any{"kcp.keepaliveTimeout": "-1s"}},
		{"bool", map[string]any{"kcp.keepaliveTimeout": true}},
		{"float", map[string]any{"kcp.keepaliveTimeout": 5.0}},
		{"int64", map[string]any{"kcp.keepaliveTimeout": int64(5)}},
		{"duration_overflow", map[string]any{"kcp.keepaliveTimeout": "999999999999999999999999h"}},
		{"seconds_overflow", map[string]any{"kcp.keepaliveTimeout": "9223372037"}},
		{"integer_parse_overflow", map[string]any{"kcp.keepaliveTimeout": "999999999999999999999999"}},
		{"below_explicit_interval", map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "1999ms"}},
		{"below_default_interval", map[string]any{"kcp.keepaliveTimeout": "9s"}},
		{"below_negative_fallback", map[string]any{"kcp.keepalive": -1, "kcp.keepaliveTimeout": "9s"}},
		{"nonpositive_string", map[string]any{"kcp.keepaliveTimeout": "0"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timeoutSnapshot(t)
			_, err := timeoutNew(t, tc.values)
			if err == nil || err.Error() != timeoutContractError {
				t.Fatalf("expected fixed validation error, got %v", err)
			}
			if strings.Contains(err.Error(), "private-invalid-value") {
				t.Fatal("invalid input leaked into error")
			}
		})
	}
	// On 64-bit hosts an integer can exceed the duration's seconds capacity.
	if ^uint(0)>>63 == 1 {
		t.Run("integer_seconds_overflow", func(t *testing.T) {
			timeoutSnapshot(t)
			maximum := int(^uint(0) >> 1)
			_, err := timeoutNew(t, map[string]any{"kcp.keepaliveTimeout": maximum})
			if err == nil || err.Error() != timeoutContractError {
				t.Fatalf("expected overflow rejection, got %v", err)
			}
		})
	}
}

func TestKCPTimeoutInstanceIsolationAndReset(t *testing.T) {
	timeoutSnapshot(t)
	first, err := timeoutNew(t, map[string]any{"kcp.keepalive": 2, "kcp.keepaliveTimeout": "5s"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := timeoutNew(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.md.keepAliveTimeout != 5*time.Second || second.md.keepAliveTimeout != 0 ||
		first.smuxConfig().KeepAliveTimeout != 5*time.Second ||
		second.smuxConfig().KeepAliveTimeout != 30*time.Second {
		t.Fatal("timeout leaked across instances")
	}
	// Exercise the same parseMetadata path again without allocating a second
	// listener socket. An absent field must clear only this metadata override.
	if err := first.parseMetadata(nil); err != nil {
		t.Fatal(err)
	}
	first.md.config.Init()
	if first.md.keepAliveTimeout != 0 || first.smuxConfig().KeepAliveTimeout != 30*time.Second {
		t.Fatal("absent timeout did not reset the explicit override")
	}
}
