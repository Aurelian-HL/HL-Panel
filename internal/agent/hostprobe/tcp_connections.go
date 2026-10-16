package hostprobe

import (
	"os"
	"strconv"
	"strings"
)

// Scan both families once per host sample, not once per forwarding rule.
// Outbound sockets and TIME_WAIT must not make a listener appear online.
func tcpSockets(paths ...string) (*uint64, map[string]uint64) {
	var tables []string
	for i, path := range paths {
		raw, err := os.ReadFile(path)
		if i > 0 && os.IsNotExist(err) {
			continue
		} // IPv6 may be disabled.
		if err != nil {
			return nil, nil
		}
		tables = append(tables, string(raw))
	}
	return parseTCPSockets(tables...)
}

func parseTCPSockets(tables ...string) (*uint64, map[string]uint64) {
	listeners, established := map[string]uint64{}, map[string]uint64{}
	var total uint64
	for _, table := range tables {
		lines := strings.Split(strings.TrimSpace(table), "\n")
		if len(lines) == 0 || !strings.Contains(lines[0], "local_address") {
			return nil, nil
		}
		for _, line := range lines[1:] {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if len(f) < 4 {
				return nil, nil
			}
			_, hexPort, ok := strings.Cut(f[1], ":")
			port, err := strconv.ParseUint(hexPort, 16, 16)
			if !ok || err != nil {
				return nil, nil
			}
			key := strconv.FormatUint(port, 10)
			switch f[3] {
			case "0A":
				listeners[key] = 0
			case "01":
				// Match the NY probe's TCP metric: count only sockets that
				// are currently established. LISTEN, TIME_WAIT and handshake
				// states describe socket lifecycle, not active connections.
				total++
				established[key]++
			}
		}
	}
	for port := range listeners {
		listeners[port] = established[port]
	}
	return &total, listeners
}
