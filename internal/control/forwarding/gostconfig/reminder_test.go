package gostconfig

import (
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func TestHistoricalExpiryStillCompilesTCPAndUDPListeners(t *testing.T) {
	for _, protocol := range []forwarding.Protocol{forwarding.ProtocolTCP, forwarding.ProtocolUDP} {
		rule := validRule()
		rule.Status, rule.Protocol = forwarding.StatusCustomerExpired, protocol
		compile := CompileDirectTCP
		if protocol == forwarding.ProtocolUDP {
			rule.IngressProtocol = forwarding.IngressUDP
			compile = CompileDirectUDP
		}
		raw, err := compile([]forwarding.Rule{rule}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		var config Configuration
		if err := json.Unmarshal(raw, &config); err != nil || len(config.Services) != 1 {
			t.Fatal("historical expiry omitted listener")
		}
	}
}
