package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var (
	ErrGOSTUnsupportedFragment = errors.New("gost adapter received an unsupported fragment engine")
	ErrGOSTConfigConflict      = errors.New("gost fragments contain conflicting services")
)

// CompileGOSTBundle accepts the verified TCP DIRECT subset only. Unknown
// fields and mixed-engine bundles are rejected rather than silently omitted.
func CompileGOSTBundle(bundle agentv1.ConfigurationBundle) ([]byte, error) {
	if bundle.SchemaVersion != BundleSchemaVersion || bundle.Fragments == nil {
		return nil, fmt.Errorf("%w: invalid node bundle", ErrInvalidConfiguration)
	}
	fragments := append([]agentv1.ConfigurationFragment(nil), bundle.Fragments...)
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].GroupID < fragments[j].GroupID })
	merged := gostconfig.Configuration{Services: []gostconfig.Service{}, Metrics: &gostconfig.Metrics{Addr: gostconfig.StatsAPIAddress}}
	names := make(map[string]struct{})
	listeners := make(map[string]struct{})
	for _, fragment := range fragments {
		if fragment.Engine != agentv1.EngineGOST {
			return nil, fmt.Errorf("%w: fragment %q uses %q", ErrGOSTUnsupportedFragment, fragment.GroupID, fragment.Engine)
		}
		var config gostconfig.Configuration
		decoder := json.NewDecoder(bytes.NewReader(fragment.Config))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil || config.Services == nil {
			return nil, fmt.Errorf("%w: fragment %q must contain a services array in the supported GOST shape", ErrInvalidConfiguration, fragment.GroupID)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: fragment %q must contain a services array in the supported GOST shape", ErrInvalidConfiguration, fragment.GroupID)
		}
		if config.Metrics != nil {
			if err := validateGOSTMetrics(*config.Metrics); err != nil {
				return nil, fmt.Errorf("%w: fragment %q: %v", ErrInvalidConfiguration, fragment.GroupID, err)
			}
			if config.Metrics.Addr != gostconfig.StatsAPIAddress {
				return nil, fmt.Errorf("%w: fragment %q uses a non-standard GOST metrics address", ErrInvalidConfiguration, fragment.GroupID)
			}
		}
		for _, service := range config.Services {
			if err := validateGOSTService(service); err != nil {
				return nil, fmt.Errorf("%w: fragment %q: %v", ErrInvalidConfiguration, fragment.GroupID, err)
			}
			if _, exists := names[service.Name]; exists {
				return nil, fmt.Errorf("%w: duplicate service name", ErrGOSTConfigConflict)
			}
			if _, exists := listeners[service.Addr]; exists {
				return nil, fmt.Errorf("%w: duplicate listener address", ErrGOSTConfigConflict)
			}
			names[service.Name] = struct{}{}
			listeners[service.Addr] = struct{}{}
			merged.Services = append(merged.Services, service)
		}
	}
	return json.Marshal(merged)
}

func validateGOSTMetrics(metrics gostconfig.Metrics) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(metrics.Addr))
	if err != nil || strings.TrimSpace(port) == "" {
		return errors.New("GOST metrics address must be host:port")
	}
	if host == "localhost" {
		return errors.New("GOST metrics address must use the fixed loopback address")
	}
	parsed, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !parsed.IsLoopback() {
		return errors.New("GOST metrics address must bind to loopback")
	}
	return nil
}

func validateGOSTService(service gostconfig.Service) error {
	if strings.TrimSpace(service.Name) == "" || service.Handler.Type != "tcp" || service.Listener.Type != "tcp" {
		return errors.New("service must have a name and TCP handler/listener")
	}
	if err := validateGOSTAddress(service.Addr, true); err != nil {
		return fmt.Errorf("listener address: %w", err)
	}
	if len(service.Forwarder.Nodes) == 0 {
		return errors.New("forwarder must have at least one target")
	}
	if enabled, ok := service.Metadata["enableStats"].(bool); !ok || !enabled {
		return errors.New("service metadata.enableStats must be true")
	}
	switch service.Forwarder.Selector.Strategy {
	case "round", "rand", "hash", "fifo":
	default:
		return errors.New("unsupported target selection strategy")
	}
	if service.Forwarder.Selector.MaxFails < 1 || service.Forwarder.Selector.FailTimeout < 0 {
		return errors.New("invalid target failure policy")
	}
	seen := make(map[string]struct{}, len(service.Forwarder.Nodes))
	for _, node := range service.Forwarder.Nodes {
		if strings.TrimSpace(node.Name) == "" {
			return errors.New("target name is required")
		}
		if _, exists := seen[node.Name]; exists {
			return errors.New("duplicate target name")
		}
		seen[node.Name] = struct{}{}
		if err := validateGOSTAddress(node.Addr, false); err != nil {
			return fmt.Errorf("target address: %w", err)
		}
	}
	return nil
}

func validateGOSTAddress(address string, bind bool) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("host and port are required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if bind {
		if _, err := netip.ParseAddr(host); err != nil {
			return errors.New("listener must bind an explicit IP address")
		}
	}
	return nil
}
