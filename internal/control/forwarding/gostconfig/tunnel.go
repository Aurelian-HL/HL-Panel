package gostconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// TunnelMaterial is confidential: it is used only by private persistence and
// authenticated agent bundles, never by a public forwarding rule.
type TunnelMaterial struct {
	ExitGroupID string
	Transport   forwarding.TunnelTransport
	Port        int
	Username    string
	Password    string
	CertPEM     string
	KeyPEM      string
	ServerName  string
	KCPKey      string
}

// CompileTunnel emits a fixed-target, authenticated relay. Entry and exit may
// reside on the same physical node; one fragment then contains both services.
func CompileTunnel(rule forwarding.Rule, material TunnelMaterial, exits []string, entry, exit bool) ([]byte, error) {
	if !rule.UsesReliableUDPTunnel() || rule.PendingActivationReason() != "" || material.ExitGroupID != rule.ExitGroupID || material.Transport != rule.TunnelTransport || material.Port < 1 || material.Port > 65535 || material.Username == "" || len(material.Password) < 32 || !entry && !exit {
		return nil, fmt.Errorf("%w: invalid reliable UDP tunnel", faults.ErrValidation)
	}
	// Reuse the direct compiler's complete validation and target selector.
	direct := rule
	direct.EgressMode, direct.TunnelTransport, direct.ExitGroupID = forwarding.EgressDirect, "", ""
	raw, err := CompileDirectTCP([]forwarding.Rule{direct}, "0.0.0.0")
	if err != nil {
		return nil, err
	}
	var base Configuration
	if err := json.Unmarshal(raw, &base); err != nil || len(base.Services) != 1 {
		return nil, fmt.Errorf("invalid tunnel target configuration")
	}
	config := Configuration{Services: []Service{}, Metrics: base.Metrics}
	auth := &Auth{Username: material.Username, Password: material.Password}
	listener, dialer := EngineType{Type: string(rule.TunnelTransport)}, EngineType{Type: string(rule.TunnelTransport)}
	switch rule.TunnelTransport {
	case forwarding.TunnelQUIC:
		if material.CertPEM == "" || material.KeyPEM == "" || material.ServerName == "" {
			return nil, fmt.Errorf("missing tunnel TLS material")
		}
		listener.Metadata, dialer.Metadata = QUICMetadata(), QUICMetadata()
		listener.TLS = &TLS{CertPEM: material.CertPEM, KeyPEM: material.KeyPEM}
		if err := ValidateTunnelTLS(listener.TLS, true); err != nil {
			return nil, err
		}
		dialer.TLS = &TLS{CAPEM: material.CertPEM, Secure: true, ServerName: material.ServerName}
	case forwarding.TunnelKCP:
		if len(material.KCPKey) < 32 {
			return nil, fmt.Errorf("missing tunnel encryption key")
		}
		listener.Metadata, dialer.Metadata = KCPMetadata(material.KCPKey), KCPMetadata(material.KCPKey)
	default:
		return nil, fmt.Errorf("unsupported tunnel transport")
	}
	if exit {
		service := base.Services[0]
		service.Name = "tunnel-exit-" + rule.ID
		service.Addr = net.JoinHostPort("0.0.0.0", strconv.Itoa(material.Port))
		service.Listener = listener
		service.Handler = EngineType{Type: "relay", Auth: auth, Metadata: map[string]any{"readTimeout": "5s"}}
		config.Services = append(config.Services, service)
	}
	if entry {
		if len(exits) == 0 {
			return nil, fmt.Errorf("reliable UDP tunnel requires an exit")
		}
		name := "tunnel-" + rule.ID
		hop := Hop{Name: name, Selector: Selector{Strategy: "round", MaxFails: 1, FailTimeout: 5_000_000_000}}
		for i, host := range exits {
			hop.Nodes = append(hop.Nodes, ChainNode{Name: fmt.Sprintf("exit-%d", i+1), Addr: net.JoinHostPort(host, strconv.Itoa(material.Port)), Connector: EngineType{Type: "relay", Auth: auth}, Dialer: dialer})
		}
		config.Chains = []Chain{{Name: name, Hops: []Hop{hop}}}
		service := base.Services[0]
		service.Handler.Chain = name
		config.Services = append(config.Services, service)
	}
	return json.Marshal(config)
}

func KCPMetadata(key string) map[string]any {
	return map[string]any{"kcp.key": key, "kcp.crypt": "aes", "kcp.mode": "normal", "kcp.nocomp": true, "kcp.mtu": 1200, "kcp.sndwnd": 128, "kcp.rcvwnd": 128, "kcp.smuxver": 1, "kcp.smuxbuf": 4194304, "kcp.streambuf": 2097152, "kcp.keepalive": 2, "kcp.keepaliveTimeout": "5s"}
}

func QUICMetadata() map[string]any {
	return map[string]any{"keepAlive": true, "ttl": "2s", "maxIdleTimeout": "8s"}
}
