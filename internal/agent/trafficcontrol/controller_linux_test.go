package trafficcontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// This test touches only an interface created here on an explicitly disposable
// runner. It never discovers or changes the host's real egress interface.
func TestDisposableLinuxElevenLimitsRestoreAndRemove(t *testing.T) {
	if os.Getenv("HL_PANEL_DISPOSABLE_INSTALL_TEST") != "true" || os.Geteuid() != 0 {
		t.Skip("requires an explicitly disposable Linux runner and root")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := exec.LookPath("tc")
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	iface := "hltc" + hex.EncodeToString(random)
	command := func(binary string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", binary, args, err, output)
		}
		return output
	}
	command(ip, "link", "add", "name", iface, "type", "dummy")
	t.Cleanup(func() { command(ip, "link", "del", "dev", iface) })
	command(ip, "link", "set", "dev", iface, "up")
	rootQdisc := func() []map[string]any {
		t.Helper()
		var all []map[string]any
		if err := json.Unmarshal(command(tc, "-j", "qdisc", "show", "dev", iface), &all); err != nil {
			t.Fatal(err)
		}
		var roots []map[string]any
		for _, qdisc := range all {
			if qdisc["root"] == true {
				roots = append(roots, qdisc)
			}
		}
		return roots
	}
	originalRoot := rootQdisc()
	command(tc, "qdisc", "add", "dev", iface, "clsact")
	for _, direction := range []string{"ingress", "egress"} {
		command(tc, "filter", "add", "dev", iface, direction, "pref", "9000", "matchall", "action", "pass")
	}
	limits := map[int]int{}
	for port := 24001; port <= 24011; port++ {
		limits[port] = 10 + port - 24001
	}
	statePath := filepath.Join(t.TempDir(), "traffic-control.json")
	controller, err := New(iface, tc, statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := controller.ApplyBundle(ctx, bundleWithLimits(t, limits)); err != nil {
		t.Fatal(err)
	}
	assertFilters := func(want map[int]int) {
		t.Helper()
		for _, direction := range []string{"ingress", "egress"} {
			var filters []map[string]any
			if err := json.Unmarshal(command(tc, "-j", "filter", "show", "dev", iface, direction), &filters); err != nil {
				t.Fatal(err)
			}
			seen := map[int]bool{}
			unrelated := false
			for _, filter := range filters {
				pref, _ := filter["pref"].(float64)
				if pref == 9000 {
					unrelated = true
				}
				options, ok := filter["options"].(map[string]any)
				if pref < 10000 || !ok {
					continue
				}
				port := int(pref) - 10000
				if _, ok := want[port]; !ok {
					t.Fatalf("unexpected %s limit at port %d", direction, port)
				}
				keys, _ := options["keys"].(map[string]any)
				portKey := "dst_port"
				if direction == "egress" {
					portKey = "src_port"
				}
				if keys["ip_proto"] != "tcp" || keys[portKey] != float64(port) {
					t.Fatalf("incorrect %s TCP port selector: %v", direction, keys)
				}
				actions, _ := options["actions"].([]any)
				police := false
				for _, raw := range actions {
					action, _ := raw.(map[string]any)
					police = police || action["kind"] == "police"
				}
				if !police {
					t.Fatalf("missing real police action at port %d: %v", port, options)
				}
				seen[port] = true
			}
			if !unrelated || len(seen) != len(want) {
				t.Fatalf("%s: got %d limits, want %d; unrelated filter retained=%v", direction, len(seen), len(want), unrelated)
			}
		}
		if !reflect.DeepEqual(originalRoot, rootQdisc()) {
			t.Fatal("controller changed the root qdisc")
		}
	}
	assertFilters(limits)
	// Remove one kernel filter, then recreate the controller from durable state.
	// Restore must repair it without EEXIST on the other ten existing filters.
	command(tc, "filter", "del", "dev", iface, "ingress", "pref", strconv.Itoa(10000+24001))
	restarted, err := New(iface, tc, statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	assertFilters(limits)
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, nil)); err != nil {
		t.Fatal(err)
	}
	assertFilters(nil)
}
