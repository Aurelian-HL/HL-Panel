//go:build linux

package hostprobe

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestCachedTCPSocketsRefreshesAtFiveMinutesAndOnGenerationChange(t *testing.T) {
	previous.Lock()
	oldCount, oldListeners, oldAt, oldGeneration := previous.tcpCount, previous.tcpListeners, previous.tcpSampledAt, previous.tcpGeneration
	previous.tcpCount, previous.tcpListeners, previous.tcpSampledAt, previous.tcpGeneration = nil, nil, time.Time{}, 0
	previous.Unlock()
	defer func() {
		previous.Lock()
		previous.tcpCount, previous.tcpListeners, previous.tcpSampledAt, previous.tcpGeneration = oldCount, oldListeners, oldAt, oldGeneration
		previous.Unlock()
	}()
	now := time.Now()
	calls := 0
	scan := func() (*uint64, map[string]uint64) {
		calls++
		count := uint64(calls)
		return &count, map[string]uint64{"443": count}
	}
	check := func(at time.Time, wantCalls int) {
		previous.Lock()
		count, listeners, age := cachedTCPSockets(at, scan)
		previous.Unlock()
		if calls != wantCalls || count == nil || *count != uint64(wantCalls) || listeners["443"] != uint64(wantCalls) || age == nil {
			t.Fatalf("calls=%d count=%v listeners=%v age=%v", calls, count, listeners, age)
		}
	}
	check(now, 1)
	check(now.Add(4*time.Minute), 1)
	InvalidateTCP()
	check(now.Add(4*time.Minute+time.Second), 2)
	SetTCPGeneration(agentv1.NodeConfigGeneration(1))
	check(now.Add(4*time.Minute+2*time.Second), 3)
	check(now.Add(9*time.Minute+2*time.Second), 4)
}

func TestProcMeasurements(t *testing.T) {
	total, idle, ok := cpuCounters("cpu 10 2 3 40 5 6 7 8 9 10\n")
	if !ok || total != 81 || idle != 45 {
		t.Fatalf("CPU guest double counted: %d %d", total, idle)
	}
	if _, _, ok := cpuCounters("cpu invalid 2 3 4"); ok {
		t.Fatal("malformed CPU accepted")
	}
	used, all := memory("MemTotal: 100 kB\nMemAvailable: 30 kB\n")
	if used == nil || all == nil || *used != 70*1024 || *all != 100*1024 {
		t.Fatal("host memory incorrect")
	}
	if used, _ := memory("MemTotal: 100 kB\n"); used != nil {
		t.Fatal("missing available fabricated memory")
	}
	swapUsed, swapTotal := swap("SwapTotal: 100 kB\nSwapFree: 25 kB\n")
	if swapUsed == nil || swapTotal == nil || *swapUsed != 75*1024 || *swapTotal != 100*1024 {
		t.Fatal("host swap incorrect")
	}
	if swapUsed, _ := swap("SwapTotal: 100 kB\n"); swapUsed != nil {
		t.Fatal("missing free swap fabricated usage")
	}
	if swapUsed, swapTotal = swap("SwapTotal: 0 kB\nSwapFree: 0 kB\n"); swapUsed == nil || swapTotal == nil || *swapUsed != 0 || *swapTotal != 0 {
		t.Fatal("zero-sized swap should remain a valid measurement")
	}
	one, five, fifteen := loadAverage("0.03 0.05 0.08 1/123 4567")
	if one == nil || five == nil || fifteen == nil || *one != 0.03 || *five != 0.05 || *fifteen != 0.08 {
		t.Fatal("system load averages incorrect")
	}
	if one, _, _ := loadAverage("bad 0.05 0.08"); one != nil {
		t.Fatal("malformed system load accepted")
	}
	if one, _, _ := loadAverage("NaN 0.05 0.08"); one != nil {
		t.Fatal("non-finite system load accepted")
	}
	rx, tx, ok := network("lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\neth0: 30 0 0 0 0 0 0 0 50 0 0 0 0 0 0 0\n")
	if !ok || rx != 30 || tx != 50 {
		t.Fatal("loopback counted in network")
	}
	sample := Snapshot()
	if sample.MemoryTotalBytes == nil || *sample.MemoryTotalBytes == 0 || sample.DiskTotalBytes == nil || *sample.DiskTotalBytes == 0 || sample.UptimeSeconds == nil || sample.LoadAverage1 == nil {
		t.Fatal("real host collection failed")
	}
}

func TestTCPCongestionControl(t *testing.T) {
	files := map[string][]byte{
		"/proc/sys/net/ipv4/tcp_congestion_control":           []byte(" bbr2\n"),
		"/proc/sys/net/ipv4/tcp_available_congestion_control": []byte("reno bbr2 bbr2 cubic\n"),
	}
	current, available := tcpCongestionControl(func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return value, nil
	})
	if current != "bbr2" {
		t.Fatalf("current congestion control = %q, want bbr2", current)
	}
	if want := []string{"reno", "bbr2", "cubic"}; !reflect.DeepEqual(available, want) {
		t.Fatalf("available congestion control = %#v, want %#v", available, want)
	}
	current, available = tcpCongestionControl(func(string) ([]byte, error) { return nil, os.ErrNotExist })
	if current != "" || available != nil {
		t.Fatalf("unavailable congestion control fabricated values: %q %#v", current, available)
	}
}
