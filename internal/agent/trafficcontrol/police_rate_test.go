package trafficcontrol

import (
	"regexp"
	"strconv"
	"testing"
)

var policeRatePattern = regexp.MustCompile(`\bpolice\s+0x[0-9a-fA-F]+\s+rate\s+([0-9]+(?:\.[0-9]+)?)([KMGT]?)bit\b`)

// iproute2's print_police uses PRINT_FP for rate and PRINT_JSON for index.
// Keep acceptance strict by reading the exact action's text, in bytes/second.
func policeRateBytes(output []byte) (float64, bool) {
	match := policeRatePattern.FindSubmatch(output)
	if len(match) != 3 {
		return 0, false
	}
	rate, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return 0, false
	}
	scale := map[string]float64{"": 1, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12}
	return rate * scale[string(match[2])] / 8, true
}

func TestPoliceRateOutputRequiresActualPoliceRate(t *testing.T) {
	for _, test := range []struct {
		name, output string
		want         float64
		found        bool
	}{
		{"megabit", "action order 1: police 0x1 rate 10Mbit burst 64Kb mtu 2Kb action drop", 1250000, true},
		{"fractional", "police 0xff rate 2.5Mbit burst 64Kb", 312500, true},
		{"bits", "police 0x2 rate 800bit burst 64Kb", 100, true},
		{"kilobit", "police 0x2 rate 80Kbit burst 64Kb", 10000, true},
		{"gigabit", "police 0x2 rate 1Gbit burst 64Kb", 125000000, true},
		{"missing", `{"kind":"police","index":1}`, 0, false},
		{"peak rate only", "police 0x1 peakrate 10Mbit", 0, false},
		{"not police", "rate 10Mbit", 0, false},
		{"unknown units", "police 0x1 rate 10MiB", 0, false},
		{"truncated", "police 0x1 rate 10", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			rate, found := policeRateBytes([]byte(test.output))
			if rate != test.want || found != test.found {
				t.Fatalf("rate=(%v, %v), want (%v, %v)", rate, found, test.want, test.found)
			}
		})
	}
}
