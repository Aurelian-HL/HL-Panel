package trafficcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type recordingRunner struct {
	commands [][]string
	failAt   int
}

func (r *recordingRunner) Run(_ context.Context, binary string, args ...string) error {
	command := append([]string{binary}, args...)
	r.commands = append(r.commands, command)
	if r.failAt > 0 && len(r.commands) == r.failAt {
		return errors.New("injected runner failure")
	}
	return nil
}

func bundleWithLimits(t *testing.T, limits map[int]int) agentv1.DesiredNodeConfig {
	t.Helper()
	fragments := make([]agentv1.ConfigurationFragment, 0, len(limits))
	for port, rate := range limits {
		fragments = append(fragments, agentv1.ConfigurationFragment{Usage: &agentv1.UsageMetadata{
			RuleID: "rule", Protocol: "tcp", ListenPort: port, SpeedLimitMbps: rate,
		}})
	}
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments})
	if err != nil {
		t.Fatal(err)
	}
	return agentv1.DesiredNodeConfig{Config: raw}
}

func TestApplyBundleShapesMultiplePortsAndRemovesOldPorts(t *testing.T) {
	runner := &recordingRunner{}
	path := filepath.Join(t.TempDir(), "traffic-control.json")
	c, err := New("eth0", "tc", path, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10, 12002: 20})); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12002: 30})); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"12002":30`) || strings.Contains(string(data), "12001") {
		t.Fatalf("unexpected persisted state: %s", data)
	}
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command, " "), "root") {
			t.Fatalf("traffic controller replaced root qdisc: %v", command)
		}
	}
}

func TestApplyBundleFailureRestoresPersistedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic-control.json")
	firstRunner := &recordingRunner{}
	c, err := New("eth0", "tc", path, firstRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10})); err != nil {
		t.Fatal(err)
	}
	secondRunner := &recordingRunner{failAt: 2}
	c.runner = secondRunner
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 50, 12002: 20})); err == nil {
		t.Fatal("expected injected failure")
	}
	var saved state
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Ports[12001] != 10 || len(saved.Ports) != 1 {
		t.Fatalf("rollback changed persisted state: %#v", saved.Ports)
	}
}

func TestStateReloadAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic-control.json")
	initial := &recordingRunner{}
	c, err := New("eth0", "tc", path, initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10})); err != nil {
		t.Fatal(err)
	}
	restartedRunner := &recordingRunner{}
	restarted, err := New("eth0", "tc", path, restartedRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(restartedRunner.commands) == 0 || !strings.Contains(strings.Join(restartedRunner.commands[0], " "), "clsact") {
		t.Fatalf("restart did not reapply saved state: %#v", restartedRunner.commands)
	}
}

func TestRejectsInvalidInterfaceAndNoShellMetacharacters(t *testing.T) {
	if _, err := New("eth0;touch /tmp/pwn", "tc", filepath.Join(t.TempDir(), "state"), &recordingRunner{}); err == nil {
		t.Fatal("expected invalid interface")
	}
	runner := &recordingRunner{}
	c, err := New("eth0", "tc", filepath.Join(t.TempDir(), "state"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyBundle(context.Background(), bundleWithLimits(t, map[int]int{12001: 10})); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		for _, arg := range command {
			if strings.ContainsAny(arg, ";|&$`()") {
				t.Fatalf("shell metacharacter reached argv: %#v", command)
			}
		}
	}
}
