package hostprobe

import (
	"encoding/json"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"testing"
)

func TestTCPListenerConnectionsAcrossFamilies(t *testing.T) {
	total, ports := parseTCPSockets(`sl local_address rem_address st
0: 00000000:2710 00000000:0000 0A
1: 0100007F:2710 0200007F:C001 01
2: 0100007F:2710 0300007F:C002 06
3: 0100007F:C003 0400007F:2710 01
4: 00000000:2711 00000000:0000 0A`, `sl local_address remote_address st
0: 00000000000000000000000000000000:2710 00000000000000000000000000000000:0000 0A
1: 00000000000000000000000001000000:2710 00000000000000000000000002000000:C004 01`)
	if total == nil || *total != 7 || len(ports) != 2 || ports["10000"] != 2 || ports["10001"] != 0 {
		t.Fatalf("counts: %v %v", total, ports)
	}
}

func TestTCPUnknownIsDifferentFromNoListeners(t *testing.T) {
	for _, raw := range []string{"", "bad header", "sl local_address rem_address st\n0: broken"} {
		if count, ports := parseTCPSockets(raw); count != nil || ports != nil {
			t.Fatal("malformed sample was reported as known")
		}
	}
	count, ports := parseTCPSockets("sl local_address rem_address st")
	if count == nil || *count != 0 || ports == nil || len(ports) != 0 {
		t.Fatal("empty valid table was not a known sample")
	}
	for _, tt := range []struct {
		m    map[string]uint64
		want string
	}{{nil, "null"}, {map[string]uint64{}, "{}"}} {
		raw, _ := json.Marshal(agentv1.HostSnapshot{TCPListeners: tt.m})
		var value map[string]json.RawMessage
		json.Unmarshal(raw, &value)
		if string(value["tcp_listeners"]) != tt.want {
			t.Fatalf("lost sample availability: %s", raw)
		}
	}
}
