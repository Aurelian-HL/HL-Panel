package trafficcontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
				keys, _ := options["keys"].(map[string]any)
				portKey := "dst_port"
				if direction == "egress" {
					portKey = "src_port"
				}
				portValue, _ := keys[portKey].(float64)
				port := int(portValue)
				if _, ok := want[port]; !ok {
					t.Fatalf("unexpected %s limit at port %d", direction, port)
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
				rate, found := findPoliceRate(actions)
				if !found || rate != float64(want[port])*125000 {
					t.Fatalf("incorrect real %s rate at port %d: got %v, want %d Mbit/s; actions=%v", direction, port, rate, want[port], actions)
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
	// Cancel after a real ingress replacement. Egress has not changed yet;
	// recovery must use a fresh bounded context and leave other ports alone.
	candidate := map[int]int{}
	for port, rate := range limits {
		candidate[port] = rate
	}
	candidate[24002] = 50
	candidate[24012] = 60
	cancelCtx, cancelApply := context.WithCancel(ctx)
	defer cancelApply()
	hook := &hookedRealTCRunner{after: func(args []string) {
		if args[0] == "filter" && args[1] == "replace" && args[4] == "ingress" && args[6] == "34002" {
			cancelApply()
		}
	}}
	restarted.runner = hook
	if err := restarted.ApplyBundle(cancelCtx, bundleWithLimits(t, candidate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled real apply: %v", err)
	}
	assertFilters(limits)
	for _, args := range hook.commands {
		if args[0] == "filter" && args[6] != "34002" {
			t.Fatalf("real rollback touched unrelated port: %v", args)
		}
	}
	// Engine rejection after all new limits are installed must also recover
	// the exact old map, including removal of the newly created port.
	restarted.runner = ExecRunner{}
	activationCtx, cancelActivation := context.WithCancel(ctx)
	defer cancelActivation()
	err = restarted.WithBundle(activationCtx, bundleWithLimits(t, candidate), func() error { cancelActivation(); return context.Canceled })
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("real activation rollback: %v", err)
	}
	assertFilters(limits)
	// A real ingress deletion followed by an egress failure must be repaired.
	withoutFirst := map[int]int{}
	for port, rate := range limits {
		if port != 24001 {
			withoutFirst[port] = rate
		}
	}
	denied := errors.New("injected tc permission failure")
	failed := false
	restarted.runner = &hookedRealTCRunner{before: func(args []string) error {
		if !failed && args[0] == "filter" && args[1] == "del" && args[4] == "egress" {
			failed = true
			return denied
		}
		return nil
	}}
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, withoutFirst)); !errors.Is(err, denied) {
		t.Fatalf("real partial delete failure: %v", err)
	}
	assertFilters(limits)
	// Missing filters are the sole idempotent delete error. Exercise the actual
	// iproute2 diagnostic instead of accepting all errors as absence.
	command(tc, "filter", "del", "dev", iface, "ingress", "pref", "34001")
	restarted.runner = ExecRunner{}
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, withoutFirst)); err != nil {
		t.Fatalf("delete already missing real filter: %v", err)
	}
	assertFilters(withoutFirst)
	// All accepted listener ports must work with Linux's 16-bit priorities.
	// Two high ports intentionally share a priority; deleting or restoring one
	// must preserve the other filter and all eleven unrelated listeners.
	highLimits := map[int]int{}
	for port, rate := range withoutFirst {
		highLimits[port] = rate
	}
	for port, rate := range map[int]int{55535: 25, 55536: 30, 65535: 35} {
		highLimits[port] = rate
	}
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, highLimits)); err != nil {
		t.Fatalf("apply high listener ports: %v", err)
	}
	assertFilters(highLimits)
	delete(highLimits, 55536)
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, highLimits)); err != nil {
		t.Fatalf("delete one shared-priority listener: %v", err)
	}
	assertFilters(highLimits)
	err = restarted.WithBundle(ctx, bundleWithLimits(t, withoutFirst), func() error { return denied })
	if !errors.Is(err, denied) {
		t.Fatalf("rollback high listener ports: %v", err)
	}
	assertFilters(highLimits)
	if err := restarted.ApplyBundle(ctx, bundleWithLimits(t, nil)); err != nil {
		t.Fatal(err)
	}
	assertFilters(nil)
	t.Log("real TC cancellation, activation rejection, partial deletion, missing-filter cleanup, high ports, exact rates and unrelated filters verified")
}

func findPoliceRate(value any) (float64, bool) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if rate, ok := findPoliceRate(item); ok {
				return rate, true
			}
		}
	case map[string]any:
		if rate, ok := v["rate"].(float64); ok {
			return rate, true
		}
		for _, item := range v {
			if rate, ok := findPoliceRate(item); ok {
				return rate, true
			}
		}
	}
	return 0, false
}

type hookedRealTCRunner struct {
	before   func([]string) error
	after    func([]string)
	commands [][]string
}

func (r *hookedRealTCRunner) Run(ctx context.Context, binary string, args ...string) error {
	r.commands = append(r.commands, append([]string(nil), args...))
	if r.before != nil {
		if err := r.before(args); err != nil {
			return err
		}
	}
	if err := (ExecRunner{}).Run(ctx, binary, args...); err != nil {
		return err
	}
	if r.after != nil {
		r.after(args)
	}
	return nil
}
