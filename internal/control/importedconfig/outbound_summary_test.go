package importedconfig

import "testing"

func TestNativeOutboundSummaryRouting(t *testing.T) {
	for _, tc := range []struct {
		name, raw, mode, host string
		port                  int
	}{
		{"socks-catchall", `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"s","protocol":"socks","settings":{"servers":[{"address":"landing.test","port":1080,"users":[{"user":"private","pass":"secret"}]}]}}],"routing":{"rules":[{"type":"field","inboundTag":["i"],"outboundTag":"s"}]}}`, "SOCKS5", "landing.test", 1080},
		{"conditional-block", `{"outbounds":[{"tag":"d","protocol":"freedom"},{"tag":"b","protocol":"blackhole"}],"routing":{"rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"b"}]}}`, "DIRECT", "", 0},
		{"mixed", `{"outbounds":[{"tag":"d","protocol":"freedom"},{"tag":"s","protocol":"socks","settings":{"servers":[{"address":"landing.test","port":1080}]}}],"routing":{"rules":[{"domain":["example.test"],"outboundTag":"s"}]}}`, "MIXED", "", 0},
		{"unknown", `{}`, "UNKNOWN", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, h, p := NativeOutboundSummary([]byte(tc.raw))
			if m != tc.mode || h != tc.host || p != tc.port {
				t.Fatalf("summary %s %s %d", m, h, p)
			}
		})
	}
}
