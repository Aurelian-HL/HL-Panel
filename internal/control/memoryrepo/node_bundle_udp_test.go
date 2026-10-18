package memoryrepo

import (
	"encoding/json"
	"testing"

	agentconfig "github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func udpNodeBundleFixture(t *testing.T, capabilities ...string) (*Store, forwarding.Rule, forwarding.Rule) {
	t.Helper()
	store, request := forwardingRepositoryFixture(t)
	const nodeID = "entry-node"
	store.nodes[nodeID] = nodes.Node{ID: nodeID, Name: nodeID, CredentialHash: "entry-node-credential-hash", Capabilities: capabilities}
	store.nodesByCredential["entry-node-credential-hash"] = nodeID
	store.membersByGroup[request.EntryGroupID][nodeID] = groups.Member{GroupID: request.EntryGroupID, NodeID: nodeID, DialHost: "entry.example.test", Weight: 1}
	base := forwarding.Rule{
		CustomerID: request.CustomerID, EntryGroupID: request.EntryGroupID, EgressMode: forwarding.EgressDirect,
		Status: forwarding.StatusActive, Deployed: true, ListenPort: 12000, Revision: 1,
		SelectionPolicy: forwarding.SelectionRoundRobin, Targets: []forwarding.Target{{Host: "landing.example.test", Port: 1080}},
	}
	tcp := base
	tcp.ID, tcp.Name = "tcp-rule", "tcp"
	tcp.IngressProtocol, tcp.Protocol = forwarding.IngressTCP, forwarding.ProtocolTCP
	udp := base
	udp.ID, udp.Name = "udp-rule", "udp"
	udp.IngressProtocol, udp.Protocol = forwarding.IngressUDP, forwarding.ProtocolUDP
	store.forwardRules[tcp.ID] = tcp
	store.forwardRules[udp.ID] = udp
	return store, tcp, udp
}

func bundleGOSTFragmentTypes(t *testing.T, fragments []agentv1.ConfigurationFragment) map[string]string {
	t.Helper()
	got := make(map[string]string, len(fragments))
	for _, fragment := range fragments {
		var config gostconfig.Configuration
		if err := json.Unmarshal(fragment.Config, &config); err != nil {
			t.Fatalf("decode fragment %q: %v", fragment.GroupID, err)
		}
		if len(config.Services) != 1 {
			t.Fatalf("fragment %q has %d services, want 1", fragment.GroupID, len(config.Services))
		}
		got[fragment.GroupID] = config.Services[0].Handler.Type
	}
	return got
}

func TestNodeBundleUDPAdmissionRequiresCapabilityAndSharesTCPPort(t *testing.T) {
	withoutUDP, tcp, udp := udpNodeBundleFixture(t, agentconfig.CapabilityGOSTTCPDirect)
	fragments, err := withoutUDP.compileForwardingFragmentsLocked("entry-node")
	if err != nil {
		t.Fatal(err)
	}
	types := bundleGOSTFragmentTypes(t, fragments)
	if len(types) != 1 || types[generations.ForwardingFragmentID(agentv1.EngineGOST, tcp.ID)] != "tcp" {
		t.Fatalf("legacy node fragments = %#v, want TCP only", types)
	}
	if _, exists := types[generations.ForwardingFragmentID(agentv1.EngineGOST, udp.ID)]; exists {
		t.Fatal("legacy node received an UDP fragment without gost-udp-direct")
	}

	withUDP, _, _ := udpNodeBundleFixture(t, agentconfig.CapabilityGOSTTCPDirect, agentconfig.CapabilityGOSTUDPDirect)
	fragments, err = withUDP.compileForwardingFragmentsLocked("entry-node")
	if err != nil {
		t.Fatal(err)
	}
	types = bundleGOSTFragmentTypes(t, fragments)
	if len(types) != 2 || types[generations.ForwardingFragmentID(agentv1.EngineGOST, tcp.ID)] != "tcp" || types[generations.ForwardingFragmentID(agentv1.EngineGOST, udp.ID)] != "udp" {
		t.Fatalf("capable node fragments = %#v, want TCP and UDP", types)
	}
}
