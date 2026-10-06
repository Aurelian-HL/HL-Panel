package forwarding

import "testing"

func TestPendingActivationReasonRequiresCompleteRealityParameters(t *testing.T) {
	rule := Rule{IngressProtocol: IngressVLESSReality, Protocol: ProtocolTCP, EgressMode: EgressDirect}
	if got := rule.PendingActivationReason(); got != "" {
		t.Fatalf("incomplete Reality rule reason = %q, want empty while ingress status is pending", got)
	}
	rule.RealityServerName = "www.example.com"
	rule.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	rule.RealityShortID = "0123456789abcdef"
	if got := rule.PendingActivationReason(); got != ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("complete Reality rule reason = %q, want %q", got, ActivationReasonVLESSRuntimeMaterialPending)
	}
}

func TestPendingActivationReasonDoesNotPromiseUnsupportedNYRoutes(t *testing.T) {
	base := Rule{IngressProtocol: IngressTCP, Protocol: ProtocolTCP, EgressMode: EgressDirect, SelectionPolicy: SelectionRoundRobin}
	tests := []struct {
		name string
		edit func(*Rule)
		want ActivationReason
	}{
		{"direct TCP", func(*Rule) {}, ""},
		{"exit group", func(r *Rule) { r.EgressMode = EgressExitGroup }, ActivationReasonExitGroupUnsupported},
		{"UDP", func(r *Rule) { r.IngressProtocol, r.Protocol = IngressUDP, ProtocolUDP }, ActivationReasonUDPUnsupported},
		{"SOCKS5", func(r *Rule) { r.IngressProtocol, r.Protocol = IngressSOCKS5, ProtocolTCP }, ActivationReasonSOCKS5RuntimeMaterialPending},
		{"Proxy Protocol", func(r *Rule) { r.SendProxyProtocol = SendProxyV1TCP }, ActivationReasonAdvancedOptionsUnsupported},
		{"speed limit", func(r *Rule) { r.SpeedLimitMbps = 10 }, ActivationReasonAdvancedOptionsUnsupported},
		{"least load", func(r *Rule) { r.SelectionPolicy = SelectionLeastLoad }, ActivationReasonSelectionUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := base
			tt.edit(&rule)
			if got := rule.PendingActivationReason(); got != tt.want {
				t.Fatalf("activation reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPendingActivationReasonRejectsVLESSOptionsIgnoredByEngine(t *testing.T) {
	rule := Rule{IngressProtocol: IngressVLESSReality, Protocol: ProtocolTCP, EgressMode: EgressDirect,
		RealityServerName: "www.example.com", RealityPublicKey: "AbCdEf0123456789AbCdEf0123456789AbCdEf01234", RealityShortID: "0123456789abcdef"}
	rule.ConnectionLimit = 2
	if got := rule.PendingActivationReason(); got != ActivationReasonAdvancedOptionsUnsupported {
		t.Fatalf("VLESS rule with unenforced connection limit reason = %q", got)
	}
}
