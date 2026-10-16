package forwarding

import "testing"

func TestNormalizeExplicitAndLegacyVLESSFlow(t *testing.T) {
	base := validRequest()
	base.IngressProtocol = IngressVLESSReality
	base.Targets = nil
	base.VLESSOutboundMode = VLESSOutboundSOCKS5
	base.VLESSSOCKS5Host, base.VLESSSOCKS5Port = "landing.example.test", 1080
	base.VLESSSOCKS5Username, base.VLESSSOCKS5Password = "synthetic", "synthetic"
	for _, test := range []struct {
		name, flow, want  string
		explicit, invalid bool
	}{
		{name: "legacy-default", want: "xtls-rprx-vision"},
		{name: "explicit-empty", explicit: true},
		{name: "explicit-vision", flow: "xtls-rprx-vision", explicit: true, want: "xtls-rprx-vision"},
		{name: "unsupported", flow: "unknown", explicit: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.VLESSFlow, input.VLESSFlowExplicit = test.flow, test.explicit
			got, err := NormalizeRequest(input)
			if test.invalid {
				if err == nil {
					t.Fatal("unsupported flow accepted")
				}
				return
			}
			if err != nil || got.VLESSFlow != test.want || got.VLESSFlowExplicit != test.explicit {
				t.Fatalf("flow normalization failed: %v", err)
			}
		})
	}
	tcp := validRequest()
	tcp.VLESSFlowExplicit = true
	if _, err := NormalizeRequest(tcp); err == nil {
		t.Fatal("non-VLESS marker accepted")
	}
}
