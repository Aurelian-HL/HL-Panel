package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func TestLoadRuntimeConfigUsesSafeLoopbackDefaults(t *testing.T) {
	membershipPath := filepath.Join(t.TempDir(), "members.json")
	configuration := loadConfigForTest(t, "{\"membership_file\":"+quoteJSON(membershipPath)+"}")
	if configuration.ListenAddress != "127.0.0.1:8443" || configuration.AllowNonLoopback {
		t.Fatalf("unsafe defaults: %#v", configuration)
	}
	if configuration.SelectionPolicy != endpointrouter.SelectionPolicyWeightedRoundRobin {
		t.Fatalf("selection policy = %q", configuration.SelectionPolicy)
	}
	if configuration.MembershipRefreshInterval != time.Second || configuration.DialTimeout != 10*time.Second {
		t.Fatalf("duration defaults = %s, %s", configuration.MembershipRefreshInterval, configuration.DialTimeout)
	}
	if configuration.TCPReachability.Enabled || configuration.TCPReachability.Interval != 5*time.Second ||
		configuration.TCPReachability.Timeout != time.Second || configuration.TCPReachability.FailureThreshold != 3 ||
		configuration.TCPReachability.SuccessThreshold != 2 || configuration.TCPReachability.MaxConcurrency != 32 {
		t.Fatalf("unsafe TCP reachability defaults: %#v", configuration.TCPReachability)
	}
}

func TestLoadRuntimeConfigRequiresExplicitNonLoopbackSwitch(t *testing.T) {
	membershipPath := filepath.Join(t.TempDir(), "members.json")
	path := writeConfigForTest(t, "{\"listen_address\":\"0.0.0.0:443\",\"membership_file\":"+quoteJSON(membershipPath)+"}")
	if _, err := loadRuntimeConfig(path); err == nil {
		t.Fatal("loadRuntimeConfig() accepted non-loopback listener without explicit switch")
	}
	configuration := loadConfigForTest(t, "{\"listen_address\":\"0.0.0.0:443\",\"allow_non_loopback\":true,\"membership_file\":"+quoteJSON(membershipPath)+"}")
	if !configuration.AllowNonLoopback {
		t.Fatal("explicit non-loopback switch was not preserved")
	}
}

func TestLoadRuntimeConfigValidatesTCPReachability(t *testing.T) {
	membershipPath := filepath.Join(t.TempDir(), "members.json")
	prefix := "{\"membership_file\":" + quoteJSON(membershipPath) + ",\"tcp_reachability\":{\"enabled\":true,"
	for name, settings := range map[string]string{
		"short_interval":         "\"interval\":\"99ms\"",
		"timeout_over_interval":  "\"interval\":\"100ms\",\"timeout\":\"101ms\"",
		"zero_failure_threshold": "\"failure_threshold\":0",
		"zero_success_threshold": "\"success_threshold\":0",
		"zero_max_concurrency":   "\"max_concurrency\":0",
		"failure_threshold":      "\"failure_threshold\":1001",
		"success_threshold":      "\"success_threshold\":1001",
		"max_concurrency":        "\"max_concurrency\":4097",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadRuntimeConfig(writeConfigForTest(t, prefix+settings+"}}")); err == nil {
				t.Fatal("loadRuntimeConfig() accepted invalid TCP reachability configuration")
			}
		})
	}
	configuration := loadConfigForTest(t, prefix+
		"\"interval\":\"2s\",\"timeout\":\"500ms\",\"failure_threshold\":4,\"success_threshold\":3,\"max_concurrency\":8}}")
	if !configuration.TCPReachability.Enabled || configuration.TCPReachability.Interval != 2*time.Second ||
		configuration.TCPReachability.Timeout != 500*time.Millisecond || configuration.TCPReachability.FailureThreshold != 4 ||
		configuration.TCPReachability.SuccessThreshold != 3 || configuration.TCPReachability.MaxConcurrency != 8 {
		t.Fatalf("TCP reachability configuration = %#v", configuration.TCPReachability)
	}
}

func TestLoadRuntimeConfigRejectsUnknownFieldsAndRelativeMembership(t *testing.T) {
	absoluteMembership := filepath.Join(t.TempDir(), "members.json")
	for name, raw := range map[string]string{
		"unknown":  "{\"membership_file\":" + quoteJSON(absoluteMembership) + ",\"token\":\"secret\"}",
		"relative": "{\"membership_file\":\"members.json\"}",
		"policy":   "{\"membership_file\":" + quoteJSON(absoluteMembership) + ",\"selection_policy\":\"random\"}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadRuntimeConfig(writeConfigForTest(t, raw)); err == nil {
				t.Fatal("loadRuntimeConfig() accepted invalid configuration")
			}
		})
	}
}

func TestLoadRuntimeConfigHTTPSourceIsMutuallyExclusive(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "gateway-token")
	configuration := loadConfigForTest(t, "{\"membership_url\":\"http://127.0.0.1:8080/api/v1/gateway/pools/test/membership\",\"membership_token_file\":"+quoteJSON(tokenPath)+"}")
	if configuration.MembershipFile != "" || configuration.MembershipTokenFile != tokenPath {
		t.Fatalf("HTTP source configuration = %#v", configuration)
	}
	for _, raw := range []string{
		"{\"membership_url\":\"http://127.0.0.1:8080/membership\"}",
		"{\"membership_file\":" + quoteJSON(tokenPath) + ",\"membership_url\":\"https://example.com/membership\",\"membership_token_file\":" + quoteJSON(tokenPath) + "}",
	} {
		if _, err := loadRuntimeConfig(writeConfigForTest(t, raw)); err == nil {
			t.Fatalf("accepted invalid source configuration: %s", raw)
		}
	}
}

func loadConfigForTest(t *testing.T, raw string) runtimeConfig {
	t.Helper()
	configuration, err := loadRuntimeConfig(writeConfigForTest(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func writeConfigForTest(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
