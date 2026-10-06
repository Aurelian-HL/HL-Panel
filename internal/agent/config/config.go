package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/agent/probeecho"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	DefaultEnrollmentTokenEnvironment = "NYVP_ENROLLMENT_TOKEN"
	CredentialFileName                = "credentials.json"
	StateFileName                     = "state.json"
	UsageJournalFileName              = "usage-journal.json"
	CapabilityNodeBundleV1            = "node-bundle-v1"
	CapabilityDryRunFileAdapter       = "dry-run-file-adapter"
	CapabilityXrayVLESSReality        = "xray-vless-reality"
	CapabilityGOSTTCPDirect           = "gost-tcp-direct"
	EngineModeDryRun                  = "dry_run"
	EngineModeXray                    = "xray"
	EngineModeGOST                    = "gost"
	EngineModeMixed                   = "mixed"
)

// Overridden by the release build; configuration may explicitly override it.
var DefaultAgentVersion = "dev"

type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

type BackoffConfig struct {
	Initial        Duration `json:"initial"`
	Maximum        Duration `json:"maximum"`
	Multiplier     float64  `json:"multiplier"`
	JitterFraction float64  `json:"jitter_fraction"`
}

type Config struct {
	ControlPlaneURL       string   `json:"control_plane_url"`
	AllowInsecureLoopback bool     `json:"allow_insecure_loopback,omitempty"`
	DataDir               string   `json:"data_dir"`
	CAFile                string   `json:"ca_file,omitempty"`
	AgentVersion          string   `json:"agent_version,omitempty"`
	Hostname              string   `json:"hostname,omitempty"`
	DialHost              string   `json:"dial_host,omitempty"`
	EnrollmentTokenEnv    string   `json:"enrollment_token_env,omitempty"`
	Capabilities          []string `json:"capabilities,omitempty"`
	EngineMode            string   `json:"engine_mode,omitempty"`
	XrayBinaryPath        string   `json:"xray_binary_path,omitempty"`
	XrayAutoStart         bool     `json:"xray_auto_start,omitempty"`
	// XrayAPIAddress is retained for compatibility with older agent
	// configurations. Stats collection uses XrayStatsAPIAddress below.
	XrayAPIAddress        string        `json:"xray_api_address,omitempty"`
	XrayStatsAPIAddress   string        `json:"xray_stats_api_address,omitempty"`
	GOSTStatsAPIAddress   string        `json:"gost_stats_api_address,omitempty"`
	GOSTBinaryPath        string        `json:"gost_binary_path,omitempty"`
	GOSTAutoStart         bool          `json:"gost_auto_start,omitempty"`
	HeartbeatInterval     Duration      `json:"heartbeat_interval"`
	DesiredPollInterval   Duration      `json:"desired_poll_interval"`
	UsageReportInterval   Duration      `json:"usage_report_interval,omitempty"`
	EnforcementInterval   Duration      `json:"enforcement_poll_interval,omitempty"`
	ProtocolProbeEchoPort int           `json:"protocol_probe_echo_port,omitempty"`
	UsageMaxBatch         int           `json:"usage_max_batch,omitempty"`
	RequestTimeout        Duration      `json:"request_timeout"`
	Backoff               BackoffConfig `json:"backoff"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open agent config: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode agent config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("agent config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode trailing agent config data: %w", err)
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) setDefaults() {
	if c.AgentVersion == "" {
		c.AgentVersion = DefaultAgentVersion
	}
	if c.EnrollmentTokenEnv == "" {
		c.EnrollmentTokenEnv = DefaultEnrollmentTokenEnvironment
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = Duration(20 * time.Second)
	}
	if c.DesiredPollInterval == 0 {
		c.DesiredPollInterval = Duration(10 * time.Second)
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = Duration(15 * time.Second)
	}
	if c.UsageReportInterval == 0 {
		c.UsageReportInterval = Duration(30 * time.Second)
	}
	if c.EnforcementInterval == 0 {
		c.EnforcementInterval = Duration(10 * time.Second)
	}
	if c.ProtocolProbeEchoPort == 0 {
		c.ProtocolProbeEchoPort = probeecho.DefaultPort
	}
	if c.UsageMaxBatch == 0 {
		c.UsageMaxBatch = 500
	}
	if c.Backoff.Initial == 0 {
		c.Backoff.Initial = Duration(time.Second)
	}
	if c.Backoff.Maximum == 0 {
		c.Backoff.Maximum = Duration(time.Minute)
	}
	if c.Backoff.Multiplier == 0 {
		c.Backoff.Multiplier = 2
	}
	if c.Backoff.JitterFraction == 0 {
		c.Backoff.JitterFraction = 0.2
	}
	if c.Hostname == "" {
		c.Hostname, _ = os.Hostname()
	}
	c.Capabilities = uniqueStrings(append(c.Capabilities, CapabilityNodeBundleV1, CapabilityDryRunFileAdapter, agentv1.CapabilityUsageGeneration))
	if c.EngineMode == "" {
		c.EngineMode = EngineModeDryRun
	}
	if c.XrayAPIAddress == "" {
		c.XrayAPIAddress = "127.0.0.1:10085"
	}
	if c.XrayStatsAPIAddress == "" {
		c.XrayStatsAPIAddress = c.XrayAPIAddress
	}
	if c.GOSTStatsAPIAddress == "" {
		c.GOSTStatsAPIAddress = "127.0.0.1:18090"
	}
	if c.EngineMode == EngineModeXray || c.EngineMode == EngineModeMixed {
		c.Capabilities = uniqueStrings(append(c.Capabilities, CapabilityXrayVLESSReality))
	}
	if c.EngineMode == EngineModeGOST || c.EngineMode == EngineModeMixed {
		c.Capabilities = uniqueStrings(append(c.Capabilities, CapabilityGOSTTCPDirect))
	}
}

func (c Config) Validate() error {
	// Validate is also called directly by existing callers that do not pass
	// through Load; keep the new optional probe port backward compatible.
	if c.ProtocolProbeEchoPort == 0 {
		c.ProtocolProbeEchoPort = probeecho.DefaultPort
	}
	engineMode := strings.TrimSpace(c.EngineMode)
	if engineMode == "" {
		engineMode = EngineModeDryRun
	}
	// Keep direct Validate calls compatible with configurations created before
	// the StatsService address was added. Load applies the same defaults, but
	// callers and tests are allowed to validate an in-memory Config directly.
	if strings.TrimSpace(c.XrayAPIAddress) == "" {
		c.XrayAPIAddress = "127.0.0.1:10085"
	}
	if strings.TrimSpace(c.XrayStatsAPIAddress) == "" {
		c.XrayStatsAPIAddress = c.XrayAPIAddress
	}
	if strings.TrimSpace(c.GOSTStatsAPIAddress) == "" {
		c.GOSTStatsAPIAddress = "127.0.0.1:18090"
	}
	if engineMode != EngineModeDryRun && engineMode != EngineModeXray && engineMode != EngineModeGOST && engineMode != EngineModeMixed {
		return fmt.Errorf("engine_mode must be %q, %q, %q or %q", EngineModeDryRun, EngineModeXray, EngineModeGOST, EngineModeMixed)
	}
	if c.XrayAutoStart && engineMode != EngineModeXray && engineMode != EngineModeMixed {
		return errors.New("xray_auto_start requires engine_mode xray")
	}
	if (engineMode == EngineModeXray || engineMode == EngineModeMixed) && (strings.TrimSpace(c.XrayBinaryPath) == "" || !filepath.IsAbs(c.XrayBinaryPath)) {
		return errors.New("xray_binary_path must be an absolute path when engine_mode is xray")
	}
	if engineMode == EngineModeXray || engineMode == EngineModeMixed {
		host, port, err := net.SplitHostPort(strings.TrimSpace(c.XrayAPIAddress))
		if err != nil || !isLoopbackHost(host) || port == "" {
			return errors.New("xray_api_address must be a loopback host:port")
		}
		host, port, err = net.SplitHostPort(strings.TrimSpace(c.XrayStatsAPIAddress))
		if err != nil || !isLoopbackHost(host) || port == "" {
			return errors.New("xray_stats_api_address must be a loopback host:port")
		}
	}
	if engineMode == EngineModeGOST || engineMode == EngineModeMixed {
		host, port, err := net.SplitHostPort(strings.TrimSpace(c.GOSTStatsAPIAddress))
		if err != nil || !isLoopbackHost(host) || port == "" {
			return errors.New("gost_stats_api_address must be a loopback host:port")
		}
	}
	if c.GOSTAutoStart && engineMode != EngineModeGOST && engineMode != EngineModeMixed {
		return errors.New("gost_auto_start requires engine_mode gost")
	}
	if (engineMode == EngineModeGOST || engineMode == EngineModeMixed) && (strings.TrimSpace(c.GOSTBinaryPath) == "" || !filepath.IsAbs(c.GOSTBinaryPath)) {
		return errors.New("gost_binary_path must be an absolute path when engine_mode is gost")
	}
	if engineMode == EngineModeMixed && (!c.XrayAutoStart || !c.GOSTAutoStart) {
		return errors.New("mixed engine_mode requires both xray_auto_start and gost_auto_start")
	}
	parsed, err := url.Parse(c.ControlPlaneURL)
	if err != nil {
		return fmt.Errorf("invalid control_plane_url: %w", err)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("control_plane_url must be an origin without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && c.AllowInsecureLoopback && isLoopbackHost(parsed.Hostname())) {
		return errors.New("control_plane_url must use HTTPS unless explicit loopback HTTP is enabled")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("control_plane_url must not contain a path")
	}
	if strings.TrimSpace(c.DataDir) == "" || !filepath.IsAbs(c.DataDir) {
		return errors.New("data_dir must be an absolute path")
	}
	if strings.TrimSpace(c.Hostname) == "" {
		return errors.New("hostname could not be determined")
	}
	if c.HeartbeatInterval.Duration() < time.Second || c.DesiredPollInterval.Duration() < time.Second {
		return errors.New("heartbeat_interval and desired_poll_interval must be at least 1s")
	}
	if c.RequestTimeout.Duration() <= 0 {
		return errors.New("request_timeout must be positive")
	}
	if c.UsageReportInterval != 0 && c.UsageReportInterval.Duration() < time.Second {
		return errors.New("usage_report_interval must be at least 1s")
	}
	if c.EnforcementInterval != 0 && c.EnforcementInterval.Duration() < time.Second {
		return errors.New("enforcement_poll_interval must be at least 1s")
	}
	if c.ProtocolProbeEchoPort < 1 || c.ProtocolProbeEchoPort > 65535 {
		return errors.New("protocol_probe_echo_port must be between 1 and 65535")
	}
	if c.UsageMaxBatch < 0 || c.UsageMaxBatch > 10_000 {
		return errors.New("usage_max_batch must be between 1 and 10000 when configured")
	}
	if c.Backoff.Initial.Duration() <= 0 || c.Backoff.Maximum.Duration() < c.Backoff.Initial.Duration() {
		return errors.New("backoff initial/maximum values are invalid")
	}
	if c.Backoff.Multiplier < 1 || c.Backoff.Multiplier > 10 {
		return errors.New("backoff multiplier must be between 1 and 10")
	}
	if c.Backoff.JitterFraction < 0 || c.Backoff.JitterFraction > 0.5 {
		return errors.New("backoff jitter_fraction must be between 0 and 0.5")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func (c Config) CredentialPath() string {
	return filepath.Join(c.DataDir, CredentialFileName)
}

func (c Config) StatePath() string {
	return filepath.Join(c.DataDir, StateFileName)
}

func (c Config) UsageJournalPath() string {
	return filepath.Join(c.DataDir, UsageJournalFileName)
}

func (c Config) ConfigurationDir() string {
	return filepath.Join(c.DataDir, "configurations")
}

func (c Config) EngineStagingPath() string {
	return filepath.Join(c.DataDir, "engine", "staging", "node-bundle.json")
}

func (c Config) EngineActivePath() string {
	return filepath.Join(c.DataDir, "engine", "active", "node-bundle.json")
}

func (c Config) XrayStagingDir() string {
	return filepath.Join(c.DataDir, "engine", "xray", "staging")
}

func (c Config) XrayActivePath() string {
	return filepath.Join(c.DataDir, "engine", "xray", "active", "config.json")
}

func (c Config) GOSTStagingDir() string {
	return filepath.Join(c.DataDir, "engine", "gost", "staging")
}

func (c Config) GOSTActivePath() string {
	return filepath.Join(c.DataDir, "engine", "gost", "active", "config.json")
}

func (c Config) HTTPClient() (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file contains no valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: c.RequestTimeout.Duration()}, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
