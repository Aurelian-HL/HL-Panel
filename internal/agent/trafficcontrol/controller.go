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
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Runner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

var (
	ErrFilterNotFound   = errors.New("traffic control filter does not exist")
	ErrRecoveryRequired = errors.New("traffic control recovery required before another configuration change")
)

const recoveryTimeout = 30 * time.Second

// Existing lower ports retain their durable filter identity. High listener
// ports share the unused priority 10000 with distinct flower handles, avoiding
// the 16-bit tc priority limit without removing another listener's filter.
const highPortPriority = 10000
const lastIndividualPriorityPort = 65535 - highPortPriority

func filterIdentity(port int) (priority, handle string) {
	if port > lastIndividualPriorityPort {
		return strconv.Itoa(highPortPriority), strconv.Itoa(port - lastIndividualPriorityPort)
	}
	return strconv.Itoa(highPortPriority + port), "1"
}

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
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("tc %s: %w", strings.Join(args, " "), ctx.Err())
		}
		message := strings.TrimSpace(string(output))
		if missingFilterDelete(args, message) {
			return fmt.Errorf("tc %s: %w", strings.Join(args, " "), ErrFilterNotFound)
		}
		if message == "" {
			return fmt.Errorf("tc %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("tc %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return nil
}

func missingFilterDelete(args []string, message string) bool {
	if len(args) < 2 || args[0] != "filter" || args[1] != "del" {
		return false
	}
	found := false
	for _, line := range strings.Split(message, "\n") {
		switch strings.TrimSpace(line) {
		case "RTNETLINK answers: No such file or directory", "Error: Cannot find specified filter chain.", "Error: Parent Qdisc doesn't exists.":
			found = true
		case "", "We have an error talking to the kernel":
		default:
			return false
		}
	}
	return found
}

type state struct {
	Ports         map[int]int `json:"ports"`
	RecoveryPorts []int       `json:"recovery_ports,omitempty"`
}

type Controller struct {
	mu            sync.Mutex
	interfaceName string
	binary        string
	statePath     string
	runner        Runner
	ports         map[int]int
	recoveryPorts []int
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
	return c.WithBundle(ctx, desired, nil)
}

// WithBundle applies limits before activation, restoring this controller's exact
// previous limits if activation fails. The snapshot does not depend on the engine
// LKG, which can be missing while a process and its limits are still serving.
// activate must not call back into this controller.
func (c *Controller) WithBundle(ctx context.Context, desired agentv1.DesiredNodeConfig, activate func() error) error {
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
	return c.apply(ctx, want, false, activate)
}

func (c *Controller) apply(ctx context.Context, want map[int]int, force bool, activate func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyLocked(ctx, want, force, activate)
}

func (c *Controller) applyLocked(ctx context.Context, want map[int]int, force bool, activate func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.recoveryPorts) > 0 && !force {
		return ErrRecoveryRequired
	}
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
			seen[p] = true
		}
	}
	for _, p := range c.recoveryPorts {
		if !seen[p] {
			ports = append(ports, p)
			seen[p] = true
		}
	}
	sort.Ints(ports)
	touched := make([]int, 0, len(ports))
	for _, port := range ports {
		if !force {
			oldRate, hadOld := previous[port]
			newRate, hasNew := want[port]
			if hadOld == hasNew && (!hadOld || oldRate == newRate) {
				continue
			}
		}
		touched = append(touched, port)
		if err := c.applyPort(ctx, port, want[port]); err != nil {
			return c.rollback(ctx, previous, touched, err, false)
		}
	}
	if err := ctx.Err(); err != nil {
		return c.rollback(ctx, previous, touched, err, false)
	}
	// An identical generation must not rewrite filters or the state file.
	if len(touched) == 0 {
		if activate != nil {
			return activate()
		}
		return nil
	}
	c.recoveryPorts = nil
	if err := c.save(want); err != nil {
		return c.rollback(ctx, previous, touched, err, false)
	}
	c.ports = want
	if activate != nil {
		if err := activate(); err != nil {
			return c.rollback(ctx, previous, touched, err, true)
		}
	}
	return nil
}

func (c *Controller) rollback(ctx context.Context, previous map[int]int, touched []int, cause error, persisted bool) error {
	// The request deadline is unrelated to local safety recovery. Bound recovery
	// independently, and never rewrite unchanged or not-yet-attempted filters.
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryTimeout)
	defer cancel()
	c.ports = previous
	if err := c.restore(restoreCtx, previous, touched); err != nil {
		c.recoveryPorts = append(c.recoveryPorts, touched...)
		cause = errors.Join(cause, fmt.Errorf("%w: %w", ErrRecoveryRequired, err))
	}
	if persisted || len(c.recoveryPorts) > 0 {
		if err := c.save(previous); err != nil {
			c.recoveryPorts = append(c.recoveryPorts, touched...)
			cause = errors.Join(cause, fmt.Errorf("%w: persist previous traffic control: %w", ErrRecoveryRequired, err))
		}
	}
	return cause
}

func (c *Controller) applyPort(ctx context.Context, port, rate int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return errors.New("traffic control port is invalid")
	}
	pref, handle := filterIdentity(port)
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if rate == 0 {
			// Even an individual priority may contain a filter owned by another
			// service. Select our exact handle instead of removing that group.
			args := []string{"filter", "del", "dev", c.interfaceName, direction, "pref", pref, "handle", handle, "protocol", "ip", "flower"}
			if err := c.runner.Run(ctx, c.binary, args...); err != nil && !errors.Is(err, ErrFilterNotFound) {
				return err
			}
			continue
		}
		portArg := "dst_port"
		if direction == "egress" {
			portArg = "src_port"
		}
		// Use a deterministic handle so replace updates an existing rule with
		// the same preference instead of failing with EEXIST during restore.
		args := []string{"filter", "replace", "dev", c.interfaceName, direction, "pref", pref, "handle", handle, "protocol", "ip", "flower", "ip_proto", "tcp", portArg, strconv.Itoa(port), "action", "police", "rate", strconv.Itoa(rate) + "mbit", "burst", "64k", "conform-exceed", "drop"}
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
	defer c.mu.Unlock()
	want := make(map[int]int, len(c.ports))
	for port, rate := range c.ports {
		want[port] = rate
	}
	return c.applyLocked(ctx, want, true, nil)
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
	for _, p := range saved.RecoveryPorts {
		if p >= 1 && p <= 65535 {
			c.recoveryPorts = append(c.recoveryPorts, p)
		}
	}
	return nil
}
func (c *Controller) save(ports map[int]int) error {
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(state{Ports: ports, RecoveryPorts: c.recoveryPorts})
	if err != nil {
		return err
	}
	tmp := c.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath)
}
