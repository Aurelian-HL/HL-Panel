package trafficcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Model both kernel directions, including partial commands. A canceled command
// cannot repair them, unlike a recording-only runner that always succeeds.
type kernelRunner struct {
	filters  map[string]string
	commands [][]string
	after    func([]string)
	fail     func([]string) error
}

func (r *kernelRunner) Run(ctx context.Context, _ string, args ...string) error {
	r.commands = append(r.commands, append([]string(nil), args...))
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.fail != nil {
		if err := r.fail(args); err != nil {
			return err
		}
	}
	if args[0] == "filter" {
		priority, err := strconv.Atoi(args[6])
		if err != nil || priority < 1 || priority > 65535 {
			return errors.New("invalid priority value")
		}
		key := args[4] + "/" + args[6]
		if priority == 10000 {
			for i, arg := range args {
				if arg == "handle" {
					key += "/" + args[i+1]
					break
				}
			}
		}
		if args[1] == "del" {
			delete(r.filters, key)
		} else {
			for i, arg := range args {
				if arg == "rate" {
					r.filters[key] = args[i+1]
					break
				}
			}
		}
	}
	if r.after != nil {
		r.after(args)
	}
	return nil
}

func (r *kernelRunner) assertRates(t *testing.T, rates map[int]int) {
	t.Helper()
	want := map[string]string{}
	for port, rate := range rates {
		for _, dir := range []string{"ingress", "egress"} {
			want[dir+"/"+strconv.Itoa(10000+port)] = strconv.Itoa(rate) + "mbit"
		}
	}
	if !reflect.DeepEqual(r.filters, want) {
		t.Fatalf("kernel filters = %v, want %v", r.filters, want)
	}
}

func TestCanceledPartialApplyRestoresOnlyAttemptedPorts(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	path := filepath.Join(t.TempDir(), "tc.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10, 12002: 20, 12003: 30}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	runner.commands = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.after = func(args []string) {
		if args[0] == "filter" && args[1] == "replace" && args[4] == "ingress" && args[6] == "22002" {
			cancel()
		}
	}
	err = c.ApplyBundle(ctx, bundleWithLimits(t, map[int]int{12001: 10, 12002: 50, 12003: 60}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("apply error = %v", err)
	}
	runner.assertRates(t, previous)
	for _, args := range runner.commands {
		if args[0] == "filter" && args[6] != "22002" {
			t.Fatalf("touched unrelated or unattempted port: %v", args)
		}
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed apply changed durable limits: %s", after)
	}
}

func TestHighListenerPortsApplyRemoveAndRollbackIndependently(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "tc.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	limits := map[int]int{55535: 10, 55536: 20, 65535: 30}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, limits)); err != nil {
		t.Fatalf("valid high listener ports cannot apply: %v", err)
	}
	assert := func(want map[string]string) {
		t.Helper()
		for _, direction := range []string{"ingress", "egress"} {
			for identity, rate := range want {
				if got := runner.filters[direction+"/"+identity]; got != rate {
					t.Fatalf("%s filter %s: got %q, want %q", direction, identity, got, rate)
				}
			}
		}
		if len(runner.filters) != 2*len(want) {
			t.Fatalf("unexpected filters: %v", runner.filters)
		}
	}
	assert(map[string]string{"65535": "10mbit", "10000/1": "20mbit", "10000/10000": "30mbit"})
	delete(limits, 55536)
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, limits)); err != nil {
		t.Fatal(err)
	}
	assert(map[string]string{"65535": "10mbit", "10000/10000": "30mbit"})
	failure := errors.New("engine rejected high-port update")
	if err := c.WithBundle(context.Background(), bundleWithLimits(t, map[int]int{55536: 40, 65535: 50}), func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	assert(map[string]string{"65535": "10mbit", "10000/10000": "30mbit"})
}

func TestPersistenceFailureRestoresOnlyChangedPorts(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	path := filepath.Join(t.TempDir(), "tc.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10, 12002: 20}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	runner.commands = nil
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10, 12002: 50})); err == nil {
		t.Fatal("expected persistence failure")
	}
	runner.assertRates(t, previous)
	for _, args := range runner.commands {
		if args[0] == "filter" && args[6] != "22002" {
			t.Fatalf("persistence rollback touched unchanged port: %v", args)
		}
	}
}

func TestDeleteFailureDoesNotReportUnappliedLimitsAsSuccess(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "tc.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10, 12002: 20}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("permission denied")
	failed := false
	runner.fail = func(args []string) error {
		if !failed && args[0] == "filter" && args[1] == "del" && args[4] == "egress" {
			failed = true
			return failure
		}
		return nil
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12002: 20})); !errors.Is(err, failure) {
		t.Fatalf("delete error = %v", err)
	}
	runner.assertRates(t, previous)
}

func TestCanceledDeleteRestoresBothDirections(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "tc.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.after = func(args []string) {
		if strings.Join(args[:2], " ") == "filter del" {
			cancel()
		}
	}
	if err := c.ApplyBundle(ctx, bundleWithLimits(t, nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled deletion error = %v", err)
	}
	runner.assertRates(t, previous)
}

func TestCanceledActivationRestoresExactCheckpointWithoutEngineLKG(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	path := filepath.Join(t.TempDir(), "tc.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10, 12002: 20}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("engine activation rejected")
	err = c.WithBundle(ctx, bundleWithLimits(t, map[int]int{12002: 50, 12003: 60}), func() error {
		cancel()
		return failure
	})
	if !errors.Is(err, failure) || errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("activation error = %v", err)
	}
	runner.assertRates(t, previous)
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("activation failure changed saved checkpoint: %s", after)
	}
}

func TestFailedRecoveryBlocksMutationsAcrossReloadUntilRestore(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	path := filepath.Join(t.TempDir(), "tc.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[int]int{12001: 10}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("engine rejected configuration")
	err = c.WithBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 50, 12002: 20}), func() error {
		runner.fail = func([]string) error { return errors.New("temporary kernel failure") }
		return failure
	})
	if !errors.Is(err, failure) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("failed recovery = %v", err)
	}
	runner.fail = nil
	reloaded, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, controller := range []*Controller{c, reloaded} {
		before := len(runner.commands)
		if err := controller.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("unrecovered limits were accepted: %v", err)
		}
		if len(runner.commands) != before {
			t.Fatal("blocked mutation still issued commands")
		}
	}
	if err := reloaded.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertRates(t, previous)
	if err := reloaded.ApplyBundle(context.Background(), bundleWithLimits(t, previous)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "recovery_ports") {
		t.Fatalf("successful recovery retained a failure marker: %s", data)
	}
}

func TestMissingFilterDeleteClassificationIsNarrow(t *testing.T) {
	for _, tt := range []struct {
		text    string
		missing bool
	}{
		{"RTNETLINK answers: No such file or directory\nWe have an error talking to the kernel", true},
		{"Error: Cannot find specified filter chain.\nWe have an error talking to the kernel", true},
		{"Error: Parent Qdisc doesn't exists.", true},
		{"RTNETLINK answers: Operation not permitted", false},
		{"Cannot find device eth0", false},
		{"RTNETLINK answers: No such file or directory\npermission denied", false},
		{"", false},
	} {
		if got := missingFilterDelete([]string{"filter", "del"}, tt.text); got != tt.missing {
			t.Errorf("%q missing=%v", tt.text, got)
		}
		if missingFilterDelete([]string{"filter", "replace"}, tt.text) {
			t.Errorf("replace error misclassified: %q", tt.text)
		}
	}
}

func TestAbsentFilterDeletionIsIdempotent(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "tc.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10})); err != nil {
		t.Fatal(err)
	}
	runner.filters = map[string]string{}
	runner.fail = func(args []string) error {
		if args[0] == "filter" && args[1] == "del" {
			return ErrFilterNotFound
		}
		return nil
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, nil)); err != nil {
		t.Fatal(err)
	}
	runner.assertRates(t, nil)
}

func TestIdenticalBundleDoesNotRequireStateRewrite(t *testing.T) {
	runner := &kernelRunner{filters: map[string]string{}}
	path := filepath.Join(t.TempDir(), "tc.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleWithLimits(t, map[int]int{12001: 10})
	if err := c.ApplyBundle(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := c.WithBundle(context.Background(), bundle, func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !called || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged bundle was rewritten or activation skipped")
	}
}

func TestRecoveryContextHasIndependentBoundedDeadline(t *testing.T) {
	runner := &contextCheckingRunner{kernelRunner: kernelRunner{filters: map[string]string{}}}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "tc.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = c.WithBundle(ctx, bundleWithLimits(t, map[int]int{12001: 20}), func() error {
		runner.check = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok || ctx.Err() != nil || time.Until(deadline) > recoveryTimeout {
				t.Errorf("invalid recovery context: deadline=%v err=%v", deadline, ctx.Err())
			}
		}
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	runner.assertRates(t, map[int]int{12001: 10})
}

type contextCheckingRunner struct {
	kernelRunner
	check func(context.Context)
}

func (r *contextCheckingRunner) Run(ctx context.Context, binary string, args ...string) error {
	if r.check != nil {
		r.check(ctx)
	}
	return r.kernelRunner.Run(ctx, binary, args...)
}
