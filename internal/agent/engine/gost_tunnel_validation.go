package engine

import (
	"encoding/json"
	"errors"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"strings"
)

func sameGOSTMetadata(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func validateTunnelTransport(t gostconfig.EngineType, server bool) error {
	if t.Chain != "" || t.Auth != nil {
		return errors.New("unsupported tunnel transport options")
	}
	switch t.Type {
	case "quic":
		if !sameGOSTMetadata(t.Metadata, gostconfig.QUICMetadata()) {
			return errors.New("unsupported QUIC metadata")
		}
		return gostconfig.ValidateTunnelTLS(t.TLS, server)
	case "kcp":
		key, ok := t.Metadata["kcp.key"].(string)
		if t.TLS != nil || !ok || len(key) < 32 || len(key) > 128 || !sameGOSTMetadata(t.Metadata, gostconfig.KCPMetadata(key)) {
			return errors.New("unsupported KCP profile")
		}
	default:
		return errors.New("unsupported tunnel transport")
	}
	return nil
}
func validTunnelAuth(a *gostconfig.Auth) bool {
	return a != nil && strings.HasPrefix(a.Username, "hl-") && len(a.Username) <= 131 && len(a.Password) >= 32 && len(a.Password) <= 128
}
func validateGOSTChains(config gostconfig.Configuration) error {
	names := map[string]bool{}
	for _, chain := range config.Chains {
		if !strings.HasPrefix(chain.Name, "tunnel-") || names[chain.Name] || len(chain.Hops) != 1 {
			return errors.New("invalid tunnel chain")
		}
		names[chain.Name] = true
		h := chain.Hops[0]
		if h.Name != chain.Name || len(h.Nodes) == 0 || len(h.Nodes) > 1024 || h.Selector.Strategy != "round" || h.Selector.MaxFails != 1 || h.Selector.FailTimeout != 5000000000 {
			return errors.New("unsupported tunnel hop")
		}
		nodeNames := map[string]bool{}
		for _, n := range h.Nodes {
			if n.Name == "" || nodeNames[n.Name] || validateGOSTAddress(n.Addr, false) != nil || n.Connector.Type != "relay" || !validTunnelAuth(n.Connector.Auth) || n.Connector.Chain != "" || n.Connector.TLS != nil || len(n.Connector.Metadata) != 0 {
				return errors.New("invalid authenticated tunnel connector")
			}
			nodeNames[n.Name] = true
			if err := validateTunnelTransport(n.Dialer, false); err != nil {
				return err
			}
		}
	}
	used := map[string]bool{}
	for _, service := range config.Services {
		if service.Handler.Chain == "" {
			continue
		}
		if !names[service.Handler.Chain] || service.Name != "forward-"+strings.TrimPrefix(service.Handler.Chain, "tunnel-") || used[service.Handler.Chain] {
			return errors.New("invalid tunnel service reference")
		}
		used[service.Handler.Chain] = true
	}
	if len(used) != len(names) {
		return errors.New("unreferenced tunnel chain")
	}
	return nil
}
