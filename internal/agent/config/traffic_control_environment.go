package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	TrafficControlInterfaceEnvironment = "HL_PANEL_TC_INTERFACE"
	TrafficControlBinaryEnvironment    = "HL_PANEL_TC_BINARY"
)

// Older updaters install their saved JSON after the new maintenance installer
// runs. Root-managed service defaults survive that ordering without rewriting
// credentials or running a privileged startup command. Explicit JSON settings,
// including empty values used to disable TC, always take precedence.
func (c *Config) applyTrafficControlEnvironment(document []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		return err
	}
	for key := range fields {
		if strings.HasPrefix(key, "traffic_control_") {
			return nil
		}
	}
	iface := os.Getenv(TrafficControlInterfaceEnvironment)
	binary := os.Getenv(TrafficControlBinaryEnvironment)
	if iface == "" && binary == "" {
		return nil
	}
	valid := len(iface) > 0 && len(iface) <= 15 && iface != "lo"
	for _, char := range iface {
		valid = valid && (char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("_.:-", char))
	}
	if !valid || !filepath.IsAbs(binary) || strings.ContainsAny(binary, "\r\n\x00") {
		return errors.New("invalid managed traffic-control service defaults")
	}
	c.TrafficControlInterface = iface
	c.TrafficControlBinary = binary
	return nil
}
