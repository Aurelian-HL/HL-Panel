package importedconfig

import "encoding/json"

// NativeOutboundSummary projects routing without exposing upstream credentials.
// Conditional blocks do not turn a SOCKS landing into a direct route.
func NativeOutboundSummary(raw []byte) (string, string, int) {
	var c struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Servers []struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"servers"`
			} `json:"settings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"routing"`
	}
	if json.Unmarshal(raw, &c) != nil || len(c.Outbounds) == 0 {
		return "UNKNOWN", "", 0
	}
	indices := map[string]int{}
	for i, o := range c.Outbounds {
		indices[o.Tag] = i
	}
	used := map[int]bool{}
	terminal := false
	for _, r := range c.Routing.Rules {
		var tag string
		_ = json.Unmarshal(r["outboundTag"], &tag)
		i, ok := indices[tag]
		if !ok {
			return "UNKNOWN", "", 0
		}
		if c.Outbounds[i].Protocol == "blackhole" {
			continue
		}
		used[i] = true
		catchall := true
		for k := range r {
			if k != "type" && k != "outboundTag" && k != "inboundTag" {
				catchall = false
			}
		}
		if catchall {
			terminal = true
			break
		}
	}
	if !terminal {
		used[0] = true
	}
	mode, host, port := "", "", 0
	for i := range used {
		o := c.Outbounds[i]
		next := "UNKNOWN"
		h := ""
		p := 0
		if o.Protocol == "freedom" {
			next = "DIRECT"
		}
		if o.Protocol == "socks" && len(o.Settings.Servers) == 1 {
			next = "SOCKS5"
			h = o.Settings.Servers[0].Address
			p = o.Settings.Servers[0].Port
		}
		if mode != "" && (mode != next || host != h || port != p) {
			return "MIXED", "", 0
		}
		mode, host, port = next, h, p
	}
	if mode == "" {
		return "UNKNOWN", "", 0
	}
	return mode, host, port
}
