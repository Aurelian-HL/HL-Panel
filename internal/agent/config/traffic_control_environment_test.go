package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManagedTrafficControlAfterLegacyConfigReplacement(t *testing.T) {
	t.Setenv(TrafficControlInterfaceEnvironment, "ens3")
	binary := filepath.Join(t.TempDir(), "tc")
	t.Setenv(TrafficControlBinaryEnvironment, binary)
	path := filepath.Join(t.TempDir(), "agent.json")
	legacy := map[string]any{"control_plane_url": "https://control.example", "data_dir": t.TempDir(), "dial_host": "192.0.2.10"}
	document, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TrafficControlInterface != "ens3" || cfg.TrafficControlBinary != binary || !hasCapability(cfg.NodeCapabilities(), CapabilityLinuxTCSpeedLimit) {
		t.Fatal("legacy JSON replacement lost the managed speed-limit capability")
	}
	if cfg.DialHost != legacy["dial_host"] || cfg.ControlPlaneURL != legacy["control_plane_url"] {
		t.Fatal("managed defaults changed node connection settings")
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(document) {
		t.Fatal("loading service defaults must not rewrite the operator configuration")
	}
	// Updating or restarting must respect explicit disabling, including null.
	for _, field := range []string{"traffic_control_interface", "traffic_control_binary", "traffic_control_state_file"} {
		for _, value := range []any{"", nil} {
			t.Run(field, func(t *testing.T) {
				legacy[field] = value
				document, _ := json.Marshal(legacy)
				if err := os.WriteFile(path, document, 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.TrafficControlInterface != "" || hasCapability(cfg.NodeCapabilities(), CapabilityLinuxTCSpeedLimit) {
					t.Fatal("service defaults overrode explicit operator settings")
				}
				delete(legacy, field)
			})
		}
	}
}

func TestManagedTrafficControlRejectsIncompleteOrInvalidEnvironment(t *testing.T) {
	for _, env := range [][2]string{{"ens3", ""}, {"", "/usr/sbin/tc"}, {"lo", "/usr/sbin/tc"}, {"eth0;bad", "/usr/sbin/tc"}, {"ens3", "tc"}} {
		t.Setenv(TrafficControlInterfaceEnvironment, env[0])
		t.Setenv(TrafficControlBinaryEnvironment, env[1])
		cfg := Config{}
		if err := cfg.applyTrafficControlEnvironment([]byte(`{}`)); err == nil {
			t.Fatalf("unsafe defaults accepted: %q", env)
		}
	}
}
