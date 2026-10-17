package deploymentreceipts

import (
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func pendingUnrelatedBundle(t testing.TB) Input {
	t.Helper()
	input := completeInput(t, agentv1.EngineXray)
	input.AppliedConfig, input.AppliedAttemptID, input.AppliedResults = input.CurrentConfig, input.CurrentAttemptID, input.ApplyResults
	input.Node.DesiredGeneration++
	setBundle(t, &input, []agentv1.ConfigurationFragment{
		{GroupID: "forwarding-vless/rule-one", GroupRevision: 3, Engine: agentv1.EngineXray, Config: json.RawMessage("{\"inbounds\":[]}")},
		{GroupID: "forwarding-vless/rule-two", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage("{\"inbounds\":[{\"port\":8443}]}")},
	})
	input.CurrentAttemptID = "fedcba9876543210fedcba9876543210"
	input.ApplyResults = nil
	return input
}

func TestUnchangedRuleRetainsVerifiedAppliedReceiptWhileUnrelatedBundlePending(t *testing.T) {
	input := pendingUnrelatedBundle(t)
	got := Evaluate(input)
	if !got.ReceiptVerified || got.NodeConfigGeneration != input.AppliedConfig.Generation || got.ConfigSHA256 != input.AppliedConfig.ConfigSHA256 || len(got.RuleConfigSHA256) != 64 {
		t.Fatalf("unchanged deployed fragment lost its verified receipt: %#v", got)
	}
	input.Node.AppliedGeneration = input.Node.DesiredGeneration
	for phase, previous := range input.AppliedResults {
		if input.ApplyResults == nil {
			input.ApplyResults = make(map[agentv1.ApplyPhase]generations.ApplyResult)
		}
		previous.Generation = input.Node.DesiredGeneration
		previous.ConfigSHA256 = input.CurrentConfig.ConfigSHA256
		previous.AttemptID = input.CurrentAttemptID
		input.ApplyResults[phase] = previous
	}
	after := Evaluate(input)
	if !after.ReceiptVerified || after.NodeConfigGeneration != input.Node.DesiredGeneration || after.RuleConfigSHA256 != got.RuleConfigSHA256 {
		t.Fatal("verified replacement bundle did not retain unchanged rule identity")
	}
}

func TestPendingBundleReuseFailsClosedForChangedOrInvalidEvidence(t *testing.T) {
	cases := map[string]func(*Input){
		"desired attempt invalidated": func(in *Input) { in.AttemptInvalidated = true },
		"applied attempt invalidated": func(in *Input) { in.AppliedInvalidated = true },
		"no applied config":           func(in *Input) { in.AppliedConfig = nil },
		"no applied receipts":         func(in *Input) { in.AppliedResults = nil },
		"no applied attempt":          func(in *Input) { in.AppliedAttemptID = "" },
		"wrong applied generation":    func(in *Input) { in.Node.AppliedGeneration-- },
		"member retired":              func(in *Input) { in.ActiveMember = false },
		"rule revision changed":       func(in *Input) { in.Rule.Revision++ },
		"rule paused":                 func(in *Input) { in.Rule.Paused = true },
		"applied verify failed": func(in *Input) {
			result := in.AppliedResults[agentv1.ApplyPhaseVerify]
			result.Status = agentv1.ApplyStatusFailed
			in.AppliedResults[agentv1.ApplyPhaseVerify] = result
		},
		"applied bundle corrupt": func(in *Input) { in.AppliedConfig.ConfigSHA256 = "bad" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := pendingUnrelatedBundle(t)
			change(&input)
			if Evaluate(input).ReceiptVerified {
				t.Fatal("invalid applied evidence retained rule readiness")
			}
		})
	}
	for _, change := range []string{"runtime credential", "metadata", "duplicate fragment", "removed fragment"} {
		t.Run(change, func(t *testing.T) {
			input := pendingUnrelatedBundle(t)
			var bundle agentv1.ConfigurationBundle
			if err := json.Unmarshal(input.CurrentConfig.Config, &bundle); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "runtime credential":
				bundle.Fragments[0].Config = json.RawMessage("{\"inbounds\":[],\"credential\":\"changed\"}")
			case "metadata":
				bundle.Fragments[0].Usage = &agentv1.UsageMetadata{RuleID: input.Rule.ID, CustomerID: "new-owner", SpeedLimitMbps: 1}
			case "duplicate fragment":
				bundle.Fragments = append(bundle.Fragments, bundle.Fragments[0])
			case "removed fragment":
				bundle.Fragments = bundle.Fragments[1:]
			}
			setBundle(t, &input, bundle.Fragments)
			if Evaluate(input).ReceiptVerified {
				t.Fatal("changed rule fragment reused applied evidence")
			}
		})
	}
}

func TestRuleFingerprintIncludesEntireFragmentAndIgnoresObjectKeyOrder(t *testing.T) {
	makeInput := func(raw string, usage *agentv1.UsageMetadata) Input {
		input := completeInput(t, agentv1.EngineXray)
		setBundle(t, &input, []agentv1.ConfigurationFragment{{GroupID: "forwarding-vless/rule-one", GroupRevision: 3, Engine: agentv1.EngineXray, Config: json.RawMessage(raw), Usage: usage}})
		return input
	}
	first := Evaluate(makeInput("{\"b\":2,\"a\":{\"z\":3,\"y\":1}}", nil))
	ordered := Evaluate(makeInput("{\"a\":{\"y\":1,\"z\":3},\"b\":2}", nil))
	if first.RuleConfigSHA256 == "" || first.RuleConfigSHA256 != ordered.RuleConfigSHA256 {
		t.Fatal("JSON object order changed fragment identity")
	}
	changed := Evaluate(makeInput("{\"a\":{\"y\":1,\"z\":4},\"b\":2}", nil))
	metadata := Evaluate(makeInput("{\"a\":{\"y\":1,\"z\":3},\"b\":2}", &agentv1.UsageMetadata{RuleID: "rule-one", SpeedLimitMbps: 5}))
	if first.RuleConfigSHA256 == changed.RuleConfigSHA256 || first.RuleConfigSHA256 == metadata.RuleConfigSHA256 {
		t.Fatal("runtime or metadata change retained fragment identity")
	}
	input := pendingUnrelatedBundle(t)
	encoded, err := json.Marshal(input)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("private applied config escaped Input JSON")
	}
}
