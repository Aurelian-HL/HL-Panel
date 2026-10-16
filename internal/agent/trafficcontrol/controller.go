// Package trafficcontrol applies per-listener Linux traffic shaping without
// interpreting control-plane data as a shell command.
package trafficcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Runner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, binary string, args ...string) error {
	if strings.TrimSpace(binary) == "" {
		return errors.New("traffic control binary is empty")
	}
	return commandRunner{binary: binary}.run(ctx, args...)
}

type commandRunner struct{ binary string }

func (r commandRunner) run(ctx context.Context, args ...string) error {
	// Kept in a small platform-neutral helper so tests can replace Runner. The
	// production binary receives an argv vector; no shell is ever involved.
	cmd := exec.CommandContext(ctx, r.binary, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("tc %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("tc %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return nil
}

type state struct {
	Ports map[int]int `json:"ports"`
}

type Controller struct {
	mu            sync.Mutex
	interfaceName string
	binary        string
	statePath     string
	runner        Runner
	ports         map[int]int
}

func New(interfaceName, binary, statePath string, runner Runner) (*Controller, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || strings.ContainsAny(interfaceName, " \t\r\n/\\;&|$`") {
		return nil, errors.New("traffic control interface is invalid")
	}
	if strings.TrimSpace(binary) == "" {
		return nil, errors.New("traffic control binary is required")
	}
	if !filepath.IsAbs(statePath) {
		return nil, errors.New("traffic control state path must be absolute")
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	c := &Controller{interfaceName: interfaceName, binary: binary, statePath: statePath, runner: runner, ports: map[int]int{}}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) ApplyBundle(ctx context.Context, desired agentv1.DesiredNodeConfig) error {
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(desired.Config, &bundle); err != nil {
		return fmt.Errorf("decode node bundle for traffic control: %w", err)
	}
	want := make(map[int]int)
	for _, fragment := range bundle.Fragments {
		if fragment.Usage == nil || fragment.Usage.Protocol != "tcp" || fragment.Usage.ListenPort == 0 {
			continue
		}
		if fragment.Usage.ListenPort < 1 || fragment.Usage.ListenPort > 65535 || fragment.Usage.SpeedLimitMbps < 0 {
			return errors.New("invalid traffic control metadata")
		}
		if fragment.Usage.SpeedLimitMbps > 0 {
			want[fragment.Usage.ListenPort] = fragment.Usage.SpeedLimitMbps
		}
	}
	return c.apply(ctx, want)
}

func (c *Controller) apply(ctx context.Context, want map[int]int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := make(map[int]int, len(c.ports))
	for p, r := range c.ports {
		previous[p] = r
	}
	ports := make([]int, 0, len(previous)+len(want))
	seen := map[int]bool{}
	for p := range previous {
		ports = append(ports, p)
		seen[p] = true
	}
	for p := range want {
		if !seen[p] {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	for _, port := range ports {
		if err := c.applyPort(ctx, port, want[port]); err != nil {
			if restoreErr := c.restore(ctx, previous, ports); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore traffic control: %w", restoreErr))
			}
			return err
		}
	}
	if err := c.save(want); err != nil {
		if restoreErr := c.restore(ctx, previous, ports); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore traffic control: %w", restoreErr))
		}
		return err
	}
	c.ports = want
	return nil
}

func (c *Controller) applyPort(ctx context.Context, port, rate int) error {
	if port < 1 || port > 65535 {
		return errors.New("traffic control port is invalid")
	}
	pref := strconv.Itoa(10000 + port)
	if rate > 0 {
		// clsact is a dedicated ingress/egress hook. Do not replace the root
		// qdisc, which may belong to another service on the node.
		if err := c.runner.Run(ctx, c.binary, "qdisc", "replace", "dev", c.interfaceName, "clsact"); err != nil {
			return err
		}
	}
	// clsact police rules shape traffic entering and leaving the selected
	// listener. A zero rate removes only this controller's rule.
	for _, direction := range []string{"ingress", "egress"} {
		if rate == 0 {
			if err := c.runner.Run(ctx, c.binary, "filter", "del", "dev", c.interfaceName, direction, "pref", pref); err != nil { /* absent is idempotent */
			}
			continue
		}
		portArg := "dst_port"
		if direction == "egress" {
			portArg = "src_port"
		}
		args := []string{"filter", "replace", "dev", c.interfaceName, direction, "pref", pref, "protocol", "ip", "flower", portArg, strconv.Itoa(port), "action", "police", "rate", strconv.Itoa(rate) + "mbit", "burst", "64k", "conform-exceed", "drop"}
		if err := c.runner.Run(ctx, c.binary, args...); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) restore(ctx context.Context, ports map[int]int, all []int) error {
	var first error
	for _, p := range all {
		if err := c.applyPort(ctx, p, ports[p]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Restore reapplies the last successfully persisted map after an agent
// restart. A control-plane outage therefore cannot remove a working limit.
func (c *Controller) Restore(ctx context.Context) error {
	c.mu.Lock()
	want := make(map[int]int, len(c.ports))
	ports := make([]int, 0, len(c.ports))
	for port, rate := range c.ports {
		want[port] = rate
		ports = append(ports, port)
	}
	c.mu.Unlock()
	sort.Ints(ports)
	return c.apply(ctx, want)
}

func (c *Controller) load() error {
	raw, err := os.ReadFile(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved state
	if err := json.Unmarshal(raw, &saved); err != nil {
		return fmt.Errorf("decode traffic control state: %w", err)
	}
	for p, r := range saved.Ports {
		if p >= 1 && p <= 65535 && r > 0 {
			c.ports[p] = r
		}
	}
	return nil
}
func (c *Controller) save(ports map[int]int) error {
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(state{Ports: ports})
	if err != nil {
		return err
	}
	tmp := c.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath)
}
