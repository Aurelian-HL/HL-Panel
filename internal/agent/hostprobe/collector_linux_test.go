//go:build linux

package hostprobe

import "testing"

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
	rx, tx, ok := network("lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\neth0: 30 0 0 0 0 0 0 0 50 0 0 0 0 0 0 0\n")
	if !ok || rx != 30 || tx != 50 {
		t.Fatal("loopback counted in network")
	}
	sample := Snapshot()
	if sample.MemoryTotalBytes == nil || *sample.MemoryTotalBytes == 0 || sample.DiskTotalBytes == nil || *sample.DiskTotalBytes == 0 || sample.UptimeSeconds == nil {
		t.Fatal("real host collection failed")
	}
}
