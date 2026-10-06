package gostconfig

import (
	"encoding/json"
	"errors"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"testing"
)

func validRule() forwarding.Rule {
	return forwarding.Rule{ID: "fwd-1", Name: "test", CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: 12345, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 9000}, {Host: "::1", Port: 9001}}, SelectionPolicy: forwarding.SelectionRoundRobin, Status: forwarding.StatusPendingActivation}
}

func TestCompileExplicitListenerAndMultipleTargets(t *testing.T) {
	data, err := CompileDirectTCP([]forwarding.Rule{validRule()}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var config Configuration
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	service := config.Services[0]
	if service.Addr != "127.0.0.1:12345" || service.Handler.Type != "tcp" || service.Listener.Type != "tcp" || service.Metadata["enableStats"] != true || service.Forwarder.Selector.Strategy != "round" || len(service.Forwarder.Nodes) != 2 || service.Forwarder.Nodes[1].Addr != "[::1]:9001" {
		t.Fatalf("invalid generated configuration: %s", data)
	}
}

func TestCompilerRefusesUnsupportedOrConflictingRoutes(t *testing.T) {
	for _, change := range []func(*forwarding.Rule){
		func(r *forwarding.Rule) { r.Protocol = forwarding.ProtocolUDP },
		func(r *forwarding.Rule) { r.EgressMode = forwarding.EgressExitGroup; r.ExitGroupID = "exit-1" },
		func(r *forwarding.Rule) { r.ListenPort = 0 },
	} {
		rule := validRule()
		change(&rule)
		if _, err := CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1"); !errors.Is(err, faults.ErrValidation) {
			t.Fatal("unsupported configuration was silently compiled")
		}
	}
	first := validRule()
	second := validRule()
	second.ID = "fwd-2"
	if _, err := CompileDirectTCP([]forwarding.Rule{first, second}, "127.0.0.1"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("conflicting bind accepted")
	}
	if _, err := CompileDirectTCP([]forwarding.Rule{first}, ""); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("implicit wildcard bind accepted")
	}
}

func TestCompilerUsesFIFOForFailoverAndRejectsLeastLoad(t *testing.T) {
	rule := validRule()
	rule.SelectionPolicy = forwarding.SelectionFailover
	raw, err := CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var configuration Configuration
	if err := json.Unmarshal(raw, &configuration); err != nil {
		t.Fatal(err)
	}
	if got := configuration.Services[0].Forwarder.Selector.Strategy; got != "fifo" {
		t.Fatalf("strategy = %q, want fifo", got)
	}
	rule.SelectionPolicy = forwarding.SelectionLeastLoad
	if _, err := CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("least_load error = %v, want validation error", err)
	}
}

func TestInactiveRulesProduceNoListeningService(t *testing.T) {
	for _, status := range []forwarding.Status{forwarding.StatusPaused, forwarding.StatusCustomerDisabled, forwarding.StatusCustomerExpired, forwarding.StatusQuotaExhausted} {
		rule := validRule()
		rule.Status = status
		data, err := CompileDirectTCP([]forwarding.Rule{rule}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		var config Configuration
		if err := json.Unmarshal(data, &config); err != nil || len(config.Services) != 0 {
			t.Fatal("blocked rule became listening service")
		}
	}
}
