package deploymentreceipts

import (
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func completeInput(t *testing.T, engine agentv1.Engine) Input {
	t.Helper()
	rule := forwarding.Rule{ID: "rule-one", EntryGroupID: "group-one", Revision: 3, Protocol: forwarding.ProtocolTCP,
		Status: forwarding.StatusPendingActivation, RealityServerName: "example.org", RealityPublicKey: "public", RealityShortID: "abcd"}
	fragmentID := "forwarding-gost/rule-one"
	if engine == agentv1.EngineXray {
		rule.IngressProtocol = forwarding.IngressVLESSReality
		fragmentID = "forwarding-vless/rule-one"
	}
	const attemptID = "0123456789abcdef0123456789abcdef"
	input := Input{Rule: rule, Node: nodes.Node{ID: "node-one", DesiredGeneration: 2, AppliedGeneration: 2}, ActiveMember: true,
		CurrentAttemptID: attemptID}
	setBundle(t, &input, []agentv1.ConfigurationFragment{{GroupID: fragmentID, GroupRevision: 3, Engine: engine, Config: json.RawMessage(`{"inbounds":[]}`)}})
	input.ApplyResults = map[agentv1.ApplyPhase]generations.ApplyResult{
		agentv1.ApplyPhaseCommit: {NodeID: input.Node.ID, Generation: 2, ConfigSHA256: input.CurrentConfig.ConfigSHA256,
			Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: string(engine), AttemptID: attemptID},
		agentv1.ApplyPhaseVerify: {NodeID: input.Node.ID, Generation: 2, ConfigSHA256: input.CurrentConfig.ConfigSHA256,
			Phase: agentv1.ApplyPhaseVerify, Status: agentv1.ApplyStatusSucceeded, EngineMode: string(engine), AttemptID: attemptID},
	}
	return input
}

func setBundle(t *testing.T, input *Input, fragments []agentv1.ConfigurationFragment) {
	t.Helper()
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
	if err != nil {
		t.Fatal(err)
	}
	input.CurrentConfig = &generations.NodeConfigGeneration{NodeID: input.Node.ID, Generation: input.Node.DesiredGeneration,
		Engine: agentv1.EngineNodeBundle, Config: raw, ConfigSHA256: generations.SHA256Hex(raw)}
}

func TestRuleNodeDeploymentEvidenceFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Input)
		want   Reason
	}{
		{"verified xray receipt", func(*testing.T, *Input) {}, ReasonReceiptVerified},
		{"paused rule", func(_ *testing.T, input *Input) { input.Rule.Paused = true }, ReasonRuleInactive},
		{"retired member", func(_ *testing.T, input *Input) { input.ActiveMember = false }, ReasonNodeNotMember},
		{"missing config", func(_ *testing.T, input *Input) { input.CurrentConfig = nil }, ReasonConfigMissing},
		{"historical config", func(_ *testing.T, input *Input) { input.CurrentConfig.Generation-- }, ReasonConfigMissing},
		{"corrupt bundle hash", func(_ *testing.T, input *Input) {
			input.CurrentConfig.ConfigSHA256 = "0" + input.CurrentConfig.ConfigSHA256[1:]
		}, ReasonConfigInvalid},
		{"fragment missing", func(t *testing.T, input *Input) { setBundle(t, input, nil) }, ReasonFragmentMissing},
		{"wrong fragment engine", func(t *testing.T, input *Input) {
			setBundle(t, input, []agentv1.ConfigurationFragment{{GroupID: "forwarding-vless/rule-one", GroupRevision: 3,
				Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`)}})
		}, ReasonFragmentMismatch},
		{"wrong rule revision", func(_ *testing.T, input *Input) { input.Rule.Revision++ }, ReasonFragmentMismatch},
		{"node has not applied", func(_ *testing.T, input *Input) { input.Node.AppliedGeneration-- }, ReasonApplyPending},
		{"attempt rolled back", func(_ *testing.T, input *Input) { input.AttemptInvalidated = true }, ReasonApplyPending},
		{"stale verify hash", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseVerify]
			result.ConfigSHA256 = "stale"
			input.ApplyResults[agentv1.ApplyPhaseVerify] = result
		}, ReasonApplyPending},
		{"stale attempt", func(_ *testing.T, input *Input) {
			input.CurrentAttemptID = "fedcba9876543210fedcba9876543210"
		}, ReasonApplyPending},
		{"mixed attempts", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseVerify]
			result.AttemptID = "fedcba9876543210fedcba9876543210"
			input.ApplyResults[agentv1.ApplyPhaseVerify] = result
		}, ReasonApplyPending},
		{"old ack lacks attempt", func(_ *testing.T, input *Input) {
			input.CurrentAttemptID = ""
			for phase, result := range input.ApplyResults {
				result.AttemptID = ""
				input.ApplyResults[phase] = result
			}
		}, ReasonEngineUnverified},
		{"old ack lacks mode", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseVerify]
			result.EngineMode = ""
			input.ApplyResults[agentv1.ApplyPhaseVerify] = result
		}, ReasonEngineUnverified},
		{"dry run ack", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseVerify]
			result.EngineMode = "dry_run"
			input.ApplyResults[agentv1.ApplyPhaseVerify] = result
		}, ReasonEngineUnverified},
		{"dry run commit", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseCommit]
			result.EngineMode = "dry_run"
			input.ApplyResults[agentv1.ApplyPhaseCommit] = result
		}, ReasonEngineUnverified},
		{"wrong real engine", func(_ *testing.T, input *Input) {
			result := input.ApplyResults[agentv1.ApplyPhaseVerify]
			result.EngineMode = "gost"
			input.ApplyResults[agentv1.ApplyPhaseVerify] = result
		}, ReasonEngineUnverified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := completeInput(t, agentv1.EngineXray)
			tt.change(t, &input)
			got := Evaluate(input)
			if got.Reason != tt.want || got.ReceiptVerified != (tt.want == ReasonReceiptVerified) {
				t.Fatalf("status = %#v; want reason %q", got, tt.want)
			}
		})
	}
}

func TestGOSTRuleRequiresGOSTReceipt(t *testing.T) {
	input := completeInput(t, agentv1.EngineGOST)
	status := Evaluate(input)
	if !status.ReceiptVerified || status.Engine != agentv1.EngineGOST || !status.FragmentEmitted ||
		status.NodeConfigGeneration != 2 || status.ConfigSHA256 != input.CurrentConfig.ConfigSHA256 {
		t.Fatalf("GOST deployment status = %#v", status)
	}
}

func TestMixedReceiptRequiresBothFragmentsInCurrentBundle(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	for phase, result := range input.ApplyResults {
		result.EngineMode = "mixed"
		input.ApplyResults[phase] = result
	}
	if got := Evaluate(input); got.Reason != ReasonEngineUnverified {
		t.Fatalf("mixed receipt without GOST fragment = %q", got.Reason)
	}
	setBundle(t, &input, []agentv1.ConfigurationFragment{
		{GroupID: "forwarding-vless/rule-one", GroupRevision: 3, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[]}`)},
		{GroupID: "forwarding-gost/other-rule", GroupRevision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`)},
	})
	for phase, result := range input.ApplyResults {
		result.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
		result.EngineMode = "xray"
		input.ApplyResults[phase] = result
	}
	if got := Evaluate(input); got.Reason != ReasonEngineUnverified {
		t.Fatalf("single-engine receipt for mixed bundle = %q", got.Reason)
	}
	for phase, result := range input.ApplyResults {
		result.EngineMode = "mixed"
		input.ApplyResults[phase] = result
	}
	if got := Evaluate(input); !got.ReceiptVerified || got.Reason != ReasonReceiptVerified {
		t.Fatalf("valid mixed receipt = %#v", got)
	}
}

func TestInternalDeploymentInputCannotSerializeBundle(t *testing.T) {
	input := completeInput(t, agentv1.EngineXray)
	encoded, err := json.Marshal(input)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("internal deployment input JSON = %s; error = %v", encoded, err)
	}
	public, err := json.Marshal(Evaluate(input))
	if err != nil || !json.Valid(public) {
		t.Fatalf("public deployment status JSON = %s; error = %v", public, err)
	}
}
