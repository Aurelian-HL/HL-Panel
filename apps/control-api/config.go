package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/securefile"
)

type runtimeConfig struct {
	ListenAddress                  string
	TLSCertificateFile             string
	TLSPrivateKeyFile              string
	AllowInsecureNonLoopback       bool
	AllowVolatileStore             bool
	DatabaseURL                    string
	AdministratorUsername          string
	AdministratorPasswordHash      []byte
	CustomerPasswordFingerprintKey []byte
	GatewayPoolTokens              map[string]string
	RealityDefaults                forwarding.RealityDefaults
	ProtocolProbeXrayBinary        string
	ProtocolProbeEchoPort          int
	ProtocolProbeInterval          time.Duration
	Nezha                          *nezhamonitor.Service
}

func loadRuntimeConfig() (runtimeConfig, error) {
	configuration := runtimeConfig{
		ListenAddress:         envOrDefault("CONTROL_LISTEN_ADDRESS", "127.0.0.1:8080"),
		TLSCertificateFile:    strings.TrimSpace(os.Getenv("CONTROL_TLS_CERT_FILE")),
		TLSPrivateKeyFile:     strings.TrimSpace(os.Getenv("CONTROL_TLS_KEY_FILE")),
		AdministratorUsername: strings.TrimSpace(os.Getenv("CONTROL_BOOTSTRAP_ADMIN_USERNAME")),
	}
	allowInsecure, err := strconv.ParseBool(envOrDefault("CONTROL_ALLOW_INSECURE_HTTP", "false"))
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("parse CONTROL_ALLOW_INSECURE_HTTP: %w", err)
	}
	configuration.AllowInsecureNonLoopback = allowInsecure
	allowVolatile, err := strconv.ParseBool(envOrDefault("CONTROL_ALLOW_VOLATILE_STORE", "false"))
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("parse CONTROL_ALLOW_VOLATILE_STORE: %w", err)
	}
	configuration.AllowVolatileStore = allowVolatile
	databaseURL := strings.TrimSpace(os.Getenv("CONTROL_DATABASE_URL"))
	databaseURLFile := strings.TrimSpace(os.Getenv("CONTROL_DATABASE_URL_FILE"))
	if databaseURL != "" && databaseURLFile != "" {
		return runtimeConfig{}, fmt.Errorf("set only one of CONTROL_DATABASE_URL and CONTROL_DATABASE_URL_FILE")
	}
	if databaseURLFile != "" {
		raw, err := os.ReadFile(databaseURLFile)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("read CONTROL_DATABASE_URL_FILE failed")
		}
		databaseURL = strings.TrimSpace(string(raw))
		if databaseURL == "" {
			return runtimeConfig{}, fmt.Errorf("CONTROL_DATABASE_URL_FILE is empty")
		}
	}
	configuration.DatabaseURL = databaseURL
	if configuration.AdministratorUsername == "" {
		return runtimeConfig{}, fmt.Errorf("CONTROL_BOOTSTRAP_ADMIN_USERNAME is required")
	}
	if len(configuration.AdministratorUsername) > 128 {
		return runtimeConfig{}, fmt.Errorf("CONTROL_BOOTSTRAP_ADMIN_USERNAME is too long")
	}
	passwordHash, err := loadAdministratorPasswordHash()
	if err != nil {
		return runtimeConfig{}, err
	}
	configuration.AdministratorPasswordHash = passwordHash
	passwordFingerprintKey, err := loadCustomerPasswordFingerprintKey()
	if err != nil {
		return runtimeConfig{}, err
	}
	configuration.CustomerPasswordFingerprintKey = passwordFingerprintKey
	configuration.GatewayPoolTokens, err = loadGatewayPoolTokens(strings.TrimSpace(os.Getenv("CONTROL_GATEWAY_CREDENTIALS_FILE")))
	if err != nil {
		return runtimeConfig{}, err
	}
	configuration.RealityDefaults, err = forwarding.NormalizeRealityDefaults(forwarding.RealityDefaults{
		ServerName:  os.Getenv("CONTROL_REALITY_SERVER_NAME"),
		Destination: os.Getenv("CONTROL_REALITY_DESTINATION"),
	})
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("parse automatic Reality target: %w", err)
	}
	configuration.ProtocolProbeXrayBinary = envOrDefault("CONTROL_PROTOCOL_PROBE_XRAY_BINARY", "/opt/hl-panel/current/bin/xray")
	configuration.ProtocolProbeEchoPort, err = strconv.Atoi(envOrDefault("CONTROL_PROTOCOL_PROBE_ECHO_PORT", fmt.Sprint(protocolprobe.DefaultEchoPort)))
	if err != nil || configuration.ProtocolProbeEchoPort < 1 || configuration.ProtocolProbeEchoPort > 65535 {
		return runtimeConfig{}, fmt.Errorf("CONTROL_PROTOCOL_PROBE_ECHO_PORT must be between 1 and 65535")
	}
	configuration.ProtocolProbeInterval, err = time.ParseDuration(envOrDefault("CONTROL_PROTOCOL_PROBE_INTERVAL", "15s"))
	if err != nil || configuration.ProtocolProbeInterval < time.Second {
		return runtimeConfig{}, fmt.Errorf("CONTROL_PROTOCOL_PROBE_INTERVAL must be at least 1s")
	}
	configuration.Nezha, err = loadNezhaMonitor()
	if err != nil {
		return runtimeConfig{}, err
	}
	if (configuration.TLSCertificateFile == "") != (configuration.TLSPrivateKeyFile == "") {
		return runtimeConfig{}, fmt.Errorf("CONTROL_TLS_CERT_FILE and CONTROL_TLS_KEY_FILE must be set together")
	}
	if configuration.TLSCertificateFile == "" && !configuration.AllowInsecureNonLoopback && !isLoopbackListener(configuration.ListenAddress) {
		return runtimeConfig{}, fmt.Errorf("refusing non-loopback HTTP listener without TLS; configure TLS or explicitly set CONTROL_ALLOW_INSECURE_HTTP=true")
	}
	if configuration.DatabaseURL == "" && !configuration.AllowVolatileStore {
		return runtimeConfig{}, fmt.Errorf("configure CONTROL_DATABASE_URL_FILE or CONTROL_DATABASE_URL for durable trial storage; local evaluation requires CONTROL_ALLOW_VOLATILE_STORE=true")
	}
	return configuration, nil
}

func loadNezhaMonitor() (*nezhamonitor.Service, error) {
	address := strings.TrimSpace(os.Getenv("CONTROL_NEZHA_DASHBOARD_URL"))
	patFile := strings.TrimSpace(os.Getenv("CONTROL_NEZHA_PAT_FILE"))
	mappingFile := strings.TrimSpace(os.Getenv("CONTROL_NEZHA_NODE_MAP_FILE"))
	if address == "" && patFile == "" && mappingFile == "" {
		return nil, nil
	}
	if address == "" || patFile == "" {
		return nil, fmt.Errorf("CONTROL_NEZHA_DASHBOARD_URL and CONTROL_NEZHA_PAT_FILE must be set together")
	}
	if !filepath.IsAbs(patFile) {
		return nil, fmt.Errorf("CONTROL_NEZHA_PAT_FILE must be absolute")
	}
	rawPAT, err := readNezhaPAT(patFile)
	if err != nil {
		return nil, fmt.Errorf("read CONTROL_NEZHA_PAT_FILE failed")
	}
	pat := strings.TrimRight(string(rawPAT), "\r\n")
	mapping := make(map[string]uint64)
	if mappingFile != "" {
		if !filepath.IsAbs(mappingFile) {
			return nil, fmt.Errorf("CONTROL_NEZHA_NODE_MAP_FILE must be absolute")
		}
		rawMap, err := securefile.ReadBoundedRegular(mappingFile, 64<<10)
		if err != nil {
			return nil, fmt.Errorf("read CONTROL_NEZHA_NODE_MAP_FILE failed")
		}
		if err := json.Unmarshal(rawMap, &mapping); err != nil || mapping == nil {
			return nil, fmt.Errorf("CONTROL_NEZHA_NODE_MAP_FILE must contain a node-to-server ID object")
		}
	}
	if err := nezhamonitor.ValidateMapping(mapping); err != nil {
		return nil, fmt.Errorf("CONTROL_NEZHA_NODE_MAP_FILE is invalid: %w", err)
	}
	timeout, err := time.ParseDuration(envOrDefault("CONTROL_NEZHA_TIMEOUT", "3s"))
	if err != nil {
		return nil, fmt.Errorf("CONTROL_NEZHA_TIMEOUT is invalid")
	}
	service, err := nezhamonitor.New(address, pat, mapping, timeout)
	if err != nil {
		return nil, fmt.Errorf("Nezha monitor configuration is invalid: %w", err)
	}
	return service, nil
}

func readNezhaPAT(path string) ([]byte, error) {
	const maximum = 512
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maximum || !nezhaPATPermissionsSafe(before.Mode()) {
		return nil, fmt.Errorf("Nezha PAT must be a bounded private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || after.Size() > maximum || !os.SameFile(before, after) || !nezhaPATPermissionsSafe(after.Mode()) {
		return nil, fmt.Errorf("Nezha PAT file changed or has unsafe permissions")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(raw) > maximum {
		return nil, fmt.Errorf("Nezha PAT file exceeds size limit or could not be read")
	}
	return raw, nil
}

func nezhaPATPermissionsSafe(mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return true // Windows ACLs are not represented reliably by FileMode.Perm.
	}
	return nezhaPOSIXPATPermissionsSafe(mode)
}

func nezhaPOSIXPATPermissionsSafe(mode os.FileMode) bool {
	return mode.Perm()&0o137 == 0 // No execute, group write, or access for others.
}

func loadGatewayPoolTokens(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CONTROL_GATEWAY_CREDENTIALS_FILE must be absolute")
	}
	raw, err := securefile.ReadBoundedRegular(path, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("read CONTROL_GATEWAY_CREDENTIALS_FILE failed")
	}
	var tokens map[string]string
	if err := json.Unmarshal(raw, &tokens); err != nil || len(tokens) == 0 {
		return nil, fmt.Errorf("CONTROL_GATEWAY_CREDENTIALS_FILE must contain a pool-to-token object")
	}
	for poolID, token := range tokens {
		if poolID == "" || len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, " \r\n\t") {
			return nil, fmt.Errorf("CONTROL_GATEWAY_CREDENTIALS_FILE contains an invalid pool credential")
		}
	}
	return tokens, nil
}

func loadCustomerPasswordFingerprintKey() ([]byte, error) {
	encoded := strings.TrimSpace(os.Getenv("CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64"))
	keyFile := strings.TrimSpace(os.Getenv("CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_FILE"))
	if (encoded == "") == (keyFile == "") {
		return nil, fmt.Errorf("set exactly one customer password fingerprint key source: CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64 or CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_FILE")
	}
	if keyFile != "" {
		raw, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read customer password fingerprint key file: %w", err)
		}
		encoded = strings.TrimSpace(string(raw))
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode customer password fingerprint key: expected base64")
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("customer password fingerprint key must contain at least 32 decoded bytes")
	}
	return append([]byte(nil), key...), nil
}

func loadAdministratorPasswordHash() ([]byte, error) {
	hashValue := strings.TrimSpace(os.Getenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH"))
	passwordFile := strings.TrimSpace(os.Getenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD_FILE"))
	plainValue := os.Getenv("CONTROL_BOOTSTRAP_ADMIN_PASSWORD")
	configured := 0
	for _, value := range []string{hashValue, passwordFile, plainValue} {
		if value != "" {
			configured++
		}
	}
	if configured != 1 {
		return nil, fmt.Errorf("set exactly one bootstrap password source: CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH, CONTROL_BOOTSTRAP_ADMIN_PASSWORD_FILE, or CONTROL_BOOTSTRAP_ADMIN_PASSWORD")
	}
	if hashValue != "" {
		hash := []byte(hashValue)
		if err := auth.ValidatePasswordHash(hash); err != nil {
			return nil, err
		}
		return hash, nil
	}
	if passwordFile != "" {
		raw, err := os.ReadFile(passwordFile)
		if err != nil {
			return nil, fmt.Errorf("read bootstrap password file: %w", err)
		}
		plainValue = strings.TrimRight(string(raw), "\r\n")
	}
	return auth.HashPassword(plainValue)
}

func isLoopbackListener(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
