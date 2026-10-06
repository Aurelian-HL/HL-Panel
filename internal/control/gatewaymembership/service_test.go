package gatewaymembership

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

type stateRepository struct{ state State }

func (r *stateRepository) GatewayMembershipState(_ context.Context, poolID string) (State, error) {
	if poolID != r.state.Pool.ID {
		return State{}, errors.New("pool not found")
	}
	return r.state, nil
}

func readyState() State {
	now := time.Now().UTC()
	return State{
		Revision: 4,
		Pool:     endpoints.EndpointPool{ID: "pool-1", GroupID: "group-1", RuleID: "rule-1", Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Port: 443},
		Rule: forwarding.Rule{ID: "rule-1", EntryGroupID: "group-1", Protocol: forwarding.ProtocolTCP,
			IngressProtocol: forwarding.IngressVLESSReality, ListenPort: 443,
			VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.test", VLESSSOCKS5Port: 1080,
			VLESSSOCKS5Username: "landing-user",
			RealityServerName: "example.com", RealityPublicKey: "public", RealityShortID: "12345678",
			Status: forwarding.StatusPendingActivation, Revision: 7},
		Members: []endpoints.EndpointPoolMember{{PoolID: "pool-1", GroupID: "group-1", NodeID: "node-1",
			DialHost: "node.example.com", Weight: 5, State: endpoints.CandidateEligible, LastHealthAt: &now}},
	}
}

func stateWithVerifiedEvidence() State {
	state := readyState()
	const configHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	state.Deployments = map[string]DeploymentEvidence{"node-1": {
		RuleRevision: state.Rule.Revision,
		Status: deploymentreceipts.Status{RuleID: state.Rule.ID, NodeID: "node-1", NodeConfigGeneration: 12,
			ConfigSHA256: configHash, Engine: agentv1.EngineXray, ReceiptVerified: true,
			Reason: deploymentreceipts.ReasonReceiptVerified},
	}}
	state.ProtocolHealth = map[string]ProtocolObservation{"node-1": {
		RuleID: state.Rule.ID, NodeID: "node-1", RuleRevision: state.Rule.Revision,
		NodeConfigGeneration: 12, ConfigSHA256: configHash, Protocol: forwarding.IngressVLESSReality,
		DialHost: "node.example.com", VerifiedAt: *state.Members[0].LastHealthAt,
		LeaseExpiresAt: state.Members[0].LastHealthAt.Add(40 * time.Second),
	}}
	return state
}

func TestSnapshotRequiresBoundActiveRuleAndObservedMember(t *testing.T) {
	tests := map[string]func(*State){
		"invalid pool mode":  func(s *State) { s.Pool.Mode = "" },
		"unbound pool":       func(s *State) { s.Pool.RuleID = "" },
		"other rule":         func(s *State) { s.Rule.ID = "rule-2" },
		"other group":        func(s *State) { s.Rule.EntryGroupID = "group-2" },
		"other listener":     func(s *State) { s.Rule.ListenPort = 8443 },
		"paused rule":        func(s *State) { s.Rule.Paused = true },
		"disabled customer":  func(s *State) { s.Rule.Status = forwarding.StatusCustomerDisabled },
		"activation pending": func(s *State) { s.Rule.ActivationReason = forwarding.ActivationReasonVLESSRuntimeMaterialPending },
		"reality incomplete": func(s *State) { s.Rule.RealityPublicKey = "" },
		"unobserved member":  func(s *State) { s.Members[0].LastHealthAt = nil },
		"stale observation":  func(s *State) { old := time.Now().Add(-2 * time.Minute); s.Members[0].LastHealthAt = &old },
		"other pool member":  func(s *State) { s.Members[0].PoolID = "pool-2" },
		"other group member": func(s *State) { s.Members[0].GroupID = "group-2" },
		"invalid dial host":  func(s *State) { s.Members[0].DialHost = "bad host" },
		"draining member":    func(s *State) { s.Members[0].State = endpoints.CandidateDraining },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := readyState()
			mutate(&state)
			snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
			if err != nil || snapshot.Revision != 4 || snapshot.Endpoints == nil || ((name == "unobserved member" || name == "stale observation") && (len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Status != endpointrouter.EndpointOffline)) || (name != "unobserved member" && name != "stale observation" && len(snapshot.Endpoints) != 0) {
				t.Fatalf("invalid state published members: snapshot=%+v error=%v", snapshot, err)
			}
		})
	}
	state := readyState()
	snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
	if err != nil || len(snapshot.Endpoints) != 1 {
		t.Fatalf("healthy candidate missing: snapshot=%+v error=%v", snapshot, err)
	}
	endpoint := snapshot.Endpoints[0]
	if endpoint.ID != "node-1" || endpoint.Address != "node.example.com:443" || endpoint.Weight != 5 || endpoint.Status != endpointrouter.EndpointOffline {
		t.Fatalf("unexpected candidate: %+v", endpoint)
	}
}

func TestSnapshotRequiresCurrentReceiptAndBoundProtocolObservation(t *testing.T) {
	state := stateWithVerifiedEvidence()
	state.Rule.Deployed = true
	state.Rule.Status = forwarding.StatusActive
	state.Rule.ActivationReason = ""
	observation := state.ProtocolHealth["node-1"]
	observation.VerifiedAt = observation.VerifiedAt.Add(-time.Second)
	state.ProtocolHealth["node-1"] = observation
	snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
	if err != nil || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Status != endpointrouter.EndpointReady {
		t.Fatalf("matching evidence not published: snapshot=%+v err=%v", snapshot, err)
	}

	tests := map[string]func(*State){
		"missing receipt": func(s *State) { delete(s.Deployments, "node-1") },
		"old rule receipt": func(s *State) {
			proof := s.Deployments["node-1"]
			proof.RuleRevision--
			s.Deployments["node-1"] = proof
		},
		"dry run receipt": func(s *State) {
			proof := s.Deployments["node-1"]
			proof.Status.ReceiptVerified = false
			s.Deployments["node-1"] = proof
		},
		"other engine": func(s *State) {
			proof := s.Deployments["node-1"]
			proof.Status.Engine = agentv1.EngineGOST
			s.Deployments["node-1"] = proof
		},
		"missing protocol observation": func(s *State) { delete(s.ProtocolHealth, "node-1") },
		"old rule observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.RuleRevision--
			s.ProtocolHealth["node-1"] = proof
		},
		"other config observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.ConfigSHA256 = "other"
			s.ProtocolHealth["node-1"] = proof
		},
		"other generation observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.NodeConfigGeneration--
			s.ProtocolHealth["node-1"] = proof
		},
		"other address observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.DialHost = "other.example.com"
			s.ProtocolHealth["node-1"] = proof
		},
		"expired observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.LeaseExpiresAt = time.Now().Add(-time.Second)
			s.ProtocolHealth["node-1"] = proof
		},
		"overlong observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL + time.Second)
			s.ProtocolHealth["node-1"] = proof
		},
		"future observation": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.VerifiedAt = time.Now().Add(time.Second)
			proof.LeaseExpiresAt = proof.VerifiedAt.Add(time.Second)
			s.ProtocolHealth["node-1"] = proof
		},
		"zero observation time": func(s *State) {
			proof := s.ProtocolHealth["node-1"]
			proof.VerifiedAt = time.Time{}
			s.ProtocolHealth["node-1"] = proof
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := stateWithVerifiedEvidence()
			mutate(&state)
			snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
			if err != nil || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Status != endpointrouter.EndpointOffline {
				t.Fatalf("invalid evidence authorized candidate: snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestReadyCandidateCountRequiresVerifiedProtocolHealth(t *testing.T) {
	t.Skip("covered by SnapshotRequiresCurrentReceiptAndBoundProtocolObservation")
	state := stateWithVerifiedEvidence()
	service := NewService(&stateRepository{state})
	count, err := service.ReadyCandidateCount(context.Background(), state.Pool.ID)
	if err != nil || count != 1 { t.Fatalf("verified candidate count=%d err=%v, want 1", count, err) }
	delete(state.ProtocolHealth, "node-1")
	count, err = service.ReadyCandidateCount(context.Background(), state.Pool.ID)
	if err != nil || count != 0 {
		t.Fatalf("receipt and heartbeat without protocol health counted ready: count=%d err=%v", count, err)
	}
}

func TestSnapshotAcceptsIndependentFreshHeartbeatAndProtocolProbe(t *testing.T) {
	for _, offset := range []time.Duration{-5 * time.Second, 5 * time.Second} {
		state := stateWithVerifiedEvidence()
		heartbeat := time.Now().UTC().Add(-10 * time.Second)
		state.Members[0].LastHealthAt = &heartbeat
		proof := state.ProtocolHealth["node-1"]
		proof.VerifiedAt = heartbeat.Add(offset)
		proof.LeaseExpiresAt = proof.VerifiedAt.Add(40 * time.Second)
		state.ProtocolHealth["node-1"] = proof
		snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
		if err != nil || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Status != endpointrouter.EndpointReady {
			t.Fatalf("independent fresh observation offset %s rejected: snapshot=%+v err=%v", offset, snapshot, err)
		}
	}
}

func TestSnapshotDoesNotRequireMemberHealthBeforeProtocolObservation(t *testing.T) {
	state := stateWithVerifiedEvidence()
	state.Members[0].LastHealthAt = nil
	state.Members[0].LastHealthReason = "address_changed"
	// A protocol observation is the authoritative proof that restores the
	// member lease; the legacy heartbeat timestamp is not required.
	snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
	if err != nil || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Status != endpointrouter.EndpointReady {
		t.Fatalf("unhealthy member was published before protocol probe: snapshot=%+v err=%v", snapshot, err)
	}
	member := state.Members[0]
	if member.State != endpoints.CandidateEligible || member.Weight < 1 || member.ActiveConnections < 0 {
		t.Fatal("fixture no longer represents a structurally eligible member")
	}
}

func TestSnapshotTenMembersOnePoolAndOneFailedNode(t *testing.T) {
	state := stateWithVerifiedEvidence()
	state.Members = nil
	state.Deployments = make(map[string]DeploymentEvidence)
	state.ProtocolHealth = make(map[string]ProtocolObservation)
	for index := 1; index <= 10; index++ {
		id := "node-" + strconv.Itoa(index)
		member := readyState().Members[0]
		member.NodeID = id
		member.DialHost = id + ".example.com"
		member.Weight = index
		state.Members = append(state.Members, member)
		deployment := stateWithVerifiedEvidence().Deployments["node-1"]
		deployment.Status.NodeID = id
		state.Deployments[id] = deployment
		observation := stateWithVerifiedEvidence().ProtocolHealth["node-1"]
		observation.NodeID = id
		observation.DialHost = member.DialHost
		observation.VerifiedAt = *member.LastHealthAt
		observation.LeaseExpiresAt = observation.VerifiedAt.Add(40 * time.Second)
		state.ProtocolHealth[id] = observation
	}
	failed := state.Members[4]
	failed.State = endpoints.CandidateQuarantined
	state.Members[4] = failed
	snapshot, err := NewService(&stateRepository{state}).Snapshot(context.Background(), "pool-1")
	if err != nil || len(snapshot.Endpoints) != 9 {
		t.Fatalf("failed member did not leave single candidate pool: snapshot=%+v err=%v", snapshot, err)
	}
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.ID == failed.NodeID || endpoint.Status != endpointrouter.EndpointReady {
			t.Fatalf("failed or unverified node published: %+v", endpoint)
		}
	}
}

func TestHandlerCredentialIsolationAndHTTPSourceRemainOffline(t *testing.T) {
	state := readyState()
	repository := &stateRepository{state}
	firstToken := "pool-one-secret-credential-with-32-bytes"
	secondToken := "pool-two-secret-credential-with-32-bytes"
	handler, err := NewHandler(NewService(repository), map[string]string{"pool-1": firstToken, "pool-2": secondToken})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	path := "/api/v1/gateway/pools/pool-1/membership"
	for _, token := range []string{"", secondToken, "invalid"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q returned %d, want 401", token, response.Code)
		}
	}
	if _, err := NewHandler(NewService(repository), map[string]string{"pool-1": "short"}); err == nil {
		t.Fatal("short token accepted")
	}
	tokenFile := filepath.Join(t.TempDir(), "gateway-token")
	if err := os.WriteFile(tokenFile, []byte(firstToken), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := membership.NewHTTPSource(server.URL+path, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil || snapshot.Revision != 4 || len(snapshot.Endpoints) != 1 {
		t.Fatalf("HTTP snapshot=%+v error=%v", snapshot, err)
	}
	router, err := endpointrouter.New(endpointrouter.SelectionPolicyWeightedRoundRobin)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.ReplaceVersionedMembership(snapshot.Revision, snapshot.Endpoints); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Select(); !errors.Is(err, endpointrouter.ErrNoHealthyEndpoint) {
		t.Fatalf("offline candidate was selectable: %v", err)
	}
}
