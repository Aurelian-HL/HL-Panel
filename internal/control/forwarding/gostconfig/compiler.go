// Package gostconfig compiles the verified subset of structured forwarding
// rules into GOST v3 JSON. Compilation alone is never an activation signal.
package gostconfig

import (
	"encoding/json"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"net"
	"net/netip"
	"strconv"
)

type Configuration struct {
	Services []Service `json:"services"`
	Metrics  *Metrics  `json:"metrics,omitempty"`
	Chains   []Chain   `json:"chains,omitempty"`
}

// StatsAPIAddress is deliberately loopback-only. The edge agent reads this
// endpoint for authoritative per-service byte counters; it is never exposed
// through the customer or control-plane network.
const StatsAPIAddress = "127.0.0.1:18090"

type Metrics struct {
	Addr string `json:"addr"`
}
type Service struct {
	Name      string         `json:"name"`
	Addr      string         `json:"addr"`
	Handler   EngineType     `json:"handler"`
	Listener  EngineType     `json:"listener"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Forwarder Forwarder      `json:"forwarder"`
}
type EngineType struct {
	Type     string         `json:"type"`
	Chain    string         `json:"chain,omitempty"`
	Auth     *Auth          `json:"auth,omitempty"`
	TLS      *TLS           `json:"tls,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Auth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PEM fields belong to the authenticated node bundle only. The agent validates
// them and writes content-addressed private files before starting native GOST.
// File paths supplied by a control-plane fragment are never accepted.
type TLS struct {
	CertPEM    string `json:"certPEM,omitempty"`
	KeyPEM     string `json:"keyPEM,omitempty"`
	CAPEM      string `json:"caPEM,omitempty"`
	CertFile   string `json:"certFile,omitempty"`
	KeyFile    string `json:"keyFile,omitempty"`
	CAFile     string `json:"caFile,omitempty"`
	Secure     bool   `json:"secure,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

type Chain struct {
	Name string `json:"name"`
	Hops []Hop  `json:"hops"`
}
type Hop struct {
	Name     string      `json:"name"`
	Nodes    []ChainNode `json:"nodes"`
	Selector Selector    `json:"selector"`
}
type ChainNode struct {
	Name      string     `json:"name"`
	Addr      string     `json:"addr"`
	Connector EngineType `json:"connector"`
	Dialer    EngineType `json:"dialer"`
}
type Forwarder struct {
	Nodes    []Node   `json:"nodes"`
	Selector Selector `json:"selector"`
}
type Node struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}
type Selector struct {
	Strategy string `json:"strategy"`
	MaxFails int    `json:"maxFails"`
	// GOST's JSON selector configuration represents time.Duration in ns.
	FailTimeout int64 `json:"failTimeout"`
}

// CompileDirectTCP does not query a server or write any configuration. The
// caller must authorize customer state and supply only rules eligible for this
// node. A concrete bind IP is required so wildcard exposure is always explicit.
func CompileDirectTCP(rules []forwarding.Rule, bindIP string) ([]byte, error) {
	return compileDirect(rules, bindIP, forwarding.IngressTCP, forwarding.ProtocolTCP, "tcp", "TCP DIRECT")
}

// CompileDirectUDP compiles the ordinary UDP DIRECT subset supported by the
// GOST UDP listener/handler pair. It intentionally does not enable a reliable
// overlay: the node receives a native UDP socket and forwards datagrams to the
// configured UDP targets.
func CompileDirectUDP(rules []forwarding.Rule, bindIP string) ([]byte, error) {
	return compileDirect(rules, bindIP, forwarding.IngressUDP, forwarding.ProtocolUDP, "udp", "UDP DIRECT")
}

func compileDirect(rules []forwarding.Rule, bindIP string, ingress forwarding.IngressProtocol, protocol forwarding.Protocol, engineType, routeName string) ([]byte, error) {
	ip, err := netip.ParseAddr(bindIP)
	if err != nil || ip.Zone() != "" {
		return nil, fmt.Errorf("%w: bind IP must be an explicit IPv4 or IPv6 address", faults.ErrValidation)
	}
	config := Configuration{Services: make([]Service, 0, len(rules)), Metrics: &Metrics{Addr: StatsAPIAddress}}
	ports := make(map[int]struct{})
	ids := make(map[string]struct{})
	for _, rule := range rules {
		if rule.Paused || rule.Status == forwarding.StatusPaused || rule.Status == forwarding.StatusCustomerDisabled || rule.Status == forwarding.StatusQuotaExhausted {
			continue
		}
		if rule.ID == "" || len(rule.ID) > 128 || rule.ListenPort < 1 {
			return nil, fmt.Errorf("%w: a saved rule with an allocated port is required", faults.ErrValidation)
		}
		if _, duplicate := ids[rule.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate rule id", faults.ErrConflict)
		}
		ids[rule.ID] = struct{}{}
		if rule.EffectiveIngressProtocol() != ingress || rule.Protocol != protocol || rule.EgressMode != forwarding.EgressDirect {
			return nil, fmt.Errorf("%w: this compiler supports %s only; other routes require a verified engine adapter", faults.ErrValidation, routeName)
		}
		request, err := forwarding.NormalizeRequest(forwarding.Request{Name: rule.Name, CustomerID: rule.CustomerID, EntryGroupID: rule.EntryGroupID, ExitGroupID: rule.ExitGroupID,
			EgressMode: rule.EgressMode, IngressProtocol: rule.EffectiveIngressProtocol(), Protocol: rule.Protocol, ListenPort: rule.ListenPort, Targets: rule.Targets, SelectionPolicy: rule.SelectionPolicy,
			SendProxyProtocol: rule.SendProxyProtocol, AcceptProxyProtocol: rule.AcceptProxyProtocol, SpeedLimitMbps: rule.SpeedLimitMbps, IPLimit: rule.IPLimit, ConnectionLimit: rule.ConnectionLimit})
		if err != nil {
			return nil, err
		}
		if _, duplicate := ports[rule.ListenPort]; duplicate {
			return nil, fmt.Errorf("%w: duplicate listener port", faults.ErrConflict)
		}
		ports[rule.ListenPort] = struct{}{}
		strategy := ""
		switch rule.SelectionPolicy {
		case forwarding.SelectionRoundRobin:
			strategy = "round"
		case forwarding.SelectionRandom:
			strategy = "rand"
		case forwarding.SelectionIPHash:
			// GOST 3.3.1 (go-gost/x v0.17.2) sets the service hash
			// context to the source IP, excluding the ephemeral source port.
			strategy = "hash"
		case forwarding.SelectionFailover:
			strategy = "fifo"
		case forwarding.SelectionLeastLoad:
			return nil, fmt.Errorf("%w: least_load requires a connection-aware target selector", faults.ErrValidation)
		default:
			return nil, fmt.Errorf("%w: unsupported target selection policy", faults.ErrValidation)
		}
		metadata := map[string]any{"enableStats": true}
		listenerMetadata := map[string]any(nil)
		if engineType == "udp" {
			// Keep the per-client UDP session alive across datagrams. GOST's
			// default keepalive=false closes the demultiplexed session after the
			// first response, which creates a race when the same client socket
			// immediately sends its next datagram. The listener TTL still bounds
			// idle session lifetime.
			// This is listener metadata: GOST does not read UDP listener options
			// from the service-level metadata map.
			listenerMetadata = map[string]any{"keepalive": true}
		}
		service := Service{Name: "forward-" + rule.ID, Addr: net.JoinHostPort(ip.String(), strconv.Itoa(rule.ListenPort)),
			Handler: EngineType{Type: engineType}, Listener: EngineType{Type: engineType, Metadata: listenerMetadata}, Metadata: metadata, Forwarder: Forwarder{Nodes: make([]Node, 0, len(request.Targets)), Selector: Selector{Strategy: strategy, MaxFails: 1, FailTimeout: 30_000_000_000}}}
		for index, target := range request.Targets {
			service.Forwarder.Nodes = append(service.Forwarder.Nodes, Node{Name: fmt.Sprintf("target-%d", index+1), Addr: net.JoinHostPort(target.Host, strconv.Itoa(target.Port))})
		}
		config.Services = append(config.Services, service)
	}
	return json.MarshalIndent(config, "", "  ")
}
