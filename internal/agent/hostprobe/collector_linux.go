//go:build linux

// Package hostprobe collects Linux host metrics without root or external commands.
package hostprobe

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"golang.org/x/sys/unix"
)

var previous struct {
	sync.Mutex
	total, idle, rx, tx uint64
	cpuValid, netValid  bool
	at                  time.Time
}

func number(s string) (uint64, bool) { v, err := strconv.ParseUint(s, 10, 64); return v, err == nil }
func ptr(v uint64) *uint64           { return &v }

func cpuCounters(text string) (total, idle uint64, ok bool) {
	fields := strings.Fields(strings.SplitN(text, "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}
	// guest and guest_nice are already included in user and nice.
	for i := 1; i < len(fields) && i <= 8; i++ {
		value, valid := number(fields[i])
		if !valid {
			return 0, 0, false
		}
		total += value
		if i == 4 || i == 5 {
			idle += value
		}
	}
	return total, idle, total >= idle
}

func memory(text string) (used, total *uint64) {
	values := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "kB" {
			continue
		}
		if n, ok := number(f[1]); ok {
			values[strings.TrimSuffix(f[0], ":")] = n * 1024
		}
	}
	t, tok := values["MemTotal"]
	available, aok := values["MemAvailable"]
	if tok && aok && t > 0 && available <= t {
		return ptr(t - available), ptr(t)
	}
	return nil, nil
}

func network(text string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		name, body, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(body)
		if len(f) < 16 {
			continue
		}
		r, rok := number(f[0])
		t, tok := number(f[8])
		if !rok || !tok {
			continue
		}
		rx += r
		tx += t
		ok = true
	}
	return
}

func connections(files ...string) *uint64 {
	var total uint64
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) != "" {
				total++
			}
		}
	}
	return ptr(total)
}

func Snapshot() *agentv1.HostSnapshot {
	previous.Lock()
	defer previous.Unlock()
	now := time.Now()
	s := &agentv1.HostSnapshot{}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		total, idle, ok := cpuCounters(string(data))
		if ok {
			if previous.cpuValid && total > previous.total && idle >= previous.idle && idle-previous.idle <= total-previous.total {
				percent := 100 * float64((total-previous.total)-(idle-previous.idle)) / float64(total-previous.total)
				s.CPUPercent = &percent
			}
			previous.total, previous.idle, previous.cpuValid = total, idle, true
		} else {
			previous.cpuValid = false
		}
	} else {
		previous.cpuValid = false
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.MemoryUsedBytes, s.MemoryTotalBytes = memory(string(data))
	}
	var disk unix.Statfs_t
	if unix.Statfs("/", &disk) == nil && disk.Bsize > 0 && disk.Blocks >= disk.Bfree {
		s.DiskTotalBytes = ptr(disk.Blocks * uint64(disk.Bsize))
		s.DiskUsedBytes = ptr((disk.Blocks - disk.Bfree) * uint64(disk.Bsize))
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(data))
		if len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil && v >= 0 {
				s.UptimeSeconds = ptr(uint64(v))
			}
		}
	}
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		rx, tx, ok := network(string(data))
		if ok {
			s.NetInTransferBytes, s.NetOutTransferBytes = ptr(rx), ptr(tx)
			elapsed := now.Sub(previous.at).Seconds()
			if previous.netValid && elapsed > 0 && rx >= previous.rx && tx >= previous.tx {
				s.NetInSpeedBytesPerSecond = ptr(uint64(float64(rx-previous.rx) / elapsed))
				s.NetOutSpeedBytesPerSecond = ptr(uint64(float64(tx-previous.tx) / elapsed))
			}
			previous.rx, previous.tx, previous.netValid = rx, tx, true
		} else {
			previous.netValid = false
		}
	} else {
		previous.netValid = false
	}
	previous.at = now
	s.TCPConnCount = connections("/proc/net/tcp", "/proc/net/tcp6")
	s.UDPConnCount = connections("/proc/net/udp", "/proc/net/udp6")
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			if ip.To4() != nil && s.IPv4 == "" {
				s.IPv4 = ip.String()
			} else if ip.To4() == nil && s.IPv6 == "" {
				s.IPv6 = ip.String()
			}
		}
	}
	return s
}
