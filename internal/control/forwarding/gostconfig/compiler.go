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
	Type string `json:"type"`
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
	ip, err := netip.ParseAddr(bindIP)
	if err != nil || ip.Zone() != "" {
		return nil, fmt.Errorf("%w: bind IP must be an explicit IPv4 or IPv6 address", faults.ErrValidation)
	}
	config := Configuration{Services: make([]Service, 0, len(rules)), Metrics: &Metrics{Addr: StatsAPIAddress}}
	ports := make(map[int]struct{})
	ids := make(map[string]struct{})
	for _, rule := range rules {
		if rule.Paused || rule.Status == forwarding.StatusPaused || rule.Status == forwarding.StatusCustomerDisabled || rule.Status == forwarding.StatusCustomerExpired || rule.Status == forwarding.StatusQuotaExhausted {
			continue
		}
		if rule.ID == "" || len(rule.ID) > 128 || rule.ListenPort < 1 {
			return nil, fmt.Errorf("%w: a saved rule with an allocated port is required", faults.ErrValidation)
		}
		if _, duplicate := ids[rule.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate rule id", faults.ErrConflict)
		}
		ids[rule.ID] = struct{}{}
		if rule.EffectiveIngressProtocol() != forwarding.IngressTCP || rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect {
			return nil, fmt.Errorf("%w: this compiler supports TCP DIRECT only; other routes require a verified engine adapter", faults.ErrValidation)
		}
		request, err := forwarding.NormalizeRequest(forwarding.Request{Name: rule.Name, CustomerID: rule.CustomerID, EntryGroupID: rule.EntryGroupID, ExitGroupID: rule.ExitGroupID,
			EgressMode: rule.EgressMode, Protocol: rule.Protocol, ListenPort: rule.ListenPort, Targets: rule.Targets, SelectionPolicy: rule.SelectionPolicy})
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
		service := Service{Name: "forward-" + rule.ID, Addr: net.JoinHostPort(ip.String(), strconv.Itoa(rule.ListenPort)),
			Handler: EngineType{Type: "tcp"}, Listener: EngineType{Type: "tcp"}, Metadata: map[string]any{"enableStats": true}, Forwarder: Forwarder{Nodes: make([]Node, 0, len(request.Targets)), Selector: Selector{Strategy: strategy, MaxFails: 1, FailTimeout: 30_000_000_000}}}
		for index, target := range request.Targets {
			service.Forwarder.Nodes = append(service.Forwarder.Nodes, Node{Name: fmt.Sprintf("target-%d", index+1), Addr: net.JoinHostPort(target.Host, strconv.Itoa(target.Port))})
		}
		config.Services = append(config.Services, service)
	}
	return json.MarshalIndent(config, "", "  ")
}
