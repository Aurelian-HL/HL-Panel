package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/agent/controlclient"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const updateSocket = "/run/hl-panel-node-update.sock"

type localUpdateTask struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	BackupDirectory string `json:"backup_directory"`
}
type localUpdateStatus struct {
	Capability string           `json:"capability"`
	Task       *localUpdateTask `json:"task"`
}

func localUpdateCall(ctx context.Context, path string, in any) (localUpdateStatus, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", updateSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	method := "GET"
	var data []byte
	if in != nil {
		method = "POST"
		var err error
		data, err = json.Marshal(in)
		if err != nil {
			return localUpdateStatus{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return localUpdateStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return localUpdateStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return localUpdateStatus{}, fmt.Errorf("local updater status %d", response.StatusCode)
	}
	var out localUpdateStatus
	err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&out)
	return out, err
}

func (a *Agent) nodeUpdateLoop(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	next := time.Time{}
	for {
		if time.Now().After(next) {
			call, cancel := context.WithTimeout(ctx, 20*time.Second)
			active, err := a.nodeUpdateOnce(call)
			cancel()
			delay := 30 * time.Second
			if active {
				delay = 5 * time.Second
			}
			var remote *controlclient.HTTPError
			if errors.As(err, &remote) && remote.StatusCode == 404 {
				delay = 5 * time.Minute
			}
			// No credentials, command output or private configuration in maintenance logs.
			if err != nil && active {
				a.logger.Warn("node update progress unavailable; task retained")
			}
			next = time.Now().Add(delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *Agent) nodeUpdateOnce(ctx context.Context) (bool, error) {
	local, err := localUpdateCall(ctx, "/status", nil)
	a.nodeUpdateReady.Store(err == nil && local.Capability == nodeupdates.Capability)
	if err != nil {
		return false, nil
	}
	task, err := a.client.ClaimNodeUpdate(ctx, a.nodeSecret)
	if err != nil || task == nil {
		return false, err
	}
	if !nodeupdates.IDPattern.MatchString(task.ID) || !nodeupdates.SHA256Pattern.MatchString(task.ArchiveSHA256) || !nodeupdates.SHA256Pattern.MatchString(task.Lease) {
		return true, errors.New("invalid task")
	}
	local, err = localUpdateCall(ctx, "/start", map[string]string{"id": task.ID, "version": task.Version, "archive_sha256": task.ArchiveSHA256})
	if err != nil {
		return true, err
	}
	if local.Task == nil || local.Task.ID != task.ID {
		return true, errors.New("local task mismatch")
	}
	result := agentv1.NodeUpdateResult{ID: task.ID, Lease: task.Lease, State: local.Task.State, Phase: local.Task.Phase, BackupDirectory: local.Task.BackupDirectory}
	if (result.State == "verifying" || result.State == "uncertain" || result.State == "succeeded") && a.cfg.AgentVersion == task.Version {
		generation, sha, err := a.verifyNodeUpdate(ctx)
		if err == nil {
			verified, err := localUpdateCall(ctx, "/verified", map[string]string{"id": task.ID})
			if err == nil && verified.Task != nil && verified.Task.State == "succeeded" {
				result.State = "succeeded"
				result.Phase = "verified"
				result.VerifiedGeneration = generation
				result.VerifiedSHA256 = sha
			}
		} else if result.State == "succeeded" {
			result.State = "verifying"
		}
	}
	return true, a.client.ReportNodeUpdate(ctx, a.nodeSecret, result)
}

func (a *Agent) verifyNodeUpdate(ctx context.Context) (int64, string, error) {
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	desired, err := a.client.Desired(ctx, a.nodeSecret)
	if err != nil {
		return 0, "", err
	}
	current, err := a.state.Load()
	if err != nil || current.Pending != nil {
		return 0, "", errors.New("configuration pending")
	}
	if desired == nil {
		if current.Applied != nil {
			return 0, "", errors.New("desired absent")
		}
		return 0, "", nil
	}
	if current.Applied == nil || desired.Generation != current.Applied.Generation || desired.ConfigSHA256 != current.Applied.ConfigSHA256 {
		return 0, "", errors.New("configuration not applied")
	}
	prepared, err := a.adapter.Prepare(ctx, *desired)
	if err != nil {
		return 0, "", err
	}
	if err = a.adapter.Verify(ctx, prepared); err != nil {
		return 0, "", err
	}
	// Check the sockets of the owned engines, including UDP listeners. A process
	// and matching file alone do not prove that its configured ports were bound.
	for _, fragment := range prepared.Bundle.Fragments {
		pid := 0
		if fragment.Engine == agentv1.EngineXray && a.xrayAdapter != nil {
			pid = a.xrayAdapter.RuntimeMetrics().PID
		}
		if fragment.Engine == agentv1.EngineGOST && a.gostAdapter != nil {
			pid = a.gostAdapter.RuntimeMetrics().PID
		}
		if err := verifyFragmentSockets(pid, fragment); err != nil {
			return 0, "", err
		}
	}
	return int64(desired.Generation), desired.ConfigSHA256, nil
}

func verifyFragmentSockets(pid int, fragment agentv1.ConfigurationFragment) error {
	if pid <= 0 {
		return errors.New("engine not running")
	}
	var config struct {
		Inbounds []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Network string `json:"network"`
			} `json:"settings"`
		} `json:"inbounds"`
		Services []struct {
			Addr     string `json:"addr"`
			Listener struct {
				Type string `json:"type"`
			} `json:"listener"`
		} `json:"services"`
	}
	if json.Unmarshal(fragment.Config, &config) != nil {
		return errors.New("invalid engine config")
	}
	owned := map[string]bool{}
	files, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return err
	}
	for _, f := range files {
		target, e := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", f.Name()))
		if e == nil && strings.HasPrefix(target, "socket:[") {
			owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	bound := map[string]bool{}
	for _, network := range []string{"tcp", "tcp6", "udp", "udp6"} {
		raw, e := os.ReadFile("/proc/net/" + network)
		if e != nil {
			return e
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			columns := strings.Fields(line)
			if len(columns) < 10 || !owned[columns[9]] {
				continue
			}
			if strings.HasPrefix(network, "tcp") && columns[3] != "0A" {
				continue
			}
			parts := strings.Split(columns[1], ":")
			if len(parts) != 2 {
				continue
			}
			port, e := strconv.ParseInt(parts[1], 16, 32)
			if e == nil {
				bound[strings.TrimSuffix(network, "6")+":"+strconv.FormatInt(port, 10)] = true
			}
		}
	}
	check := func(network string, port int) error {
		if port < 1 || port > 65535 || !bound[network+":"+strconv.Itoa(port)] {
			return errors.New("engine listener not bound")
		}
		return nil
	}
	for _, in := range config.Inbounds {
		network := "tcp"
		if in.Protocol == "dokodemo-door" && in.Settings.Network == "udp" {
			network = "udp"
		}
		if err := check(network, in.Port); err != nil {
			return err
		}
	}
	for _, service := range config.Services {
		_, port, e := net.SplitHostPort(service.Addr)
		if e != nil {
			return e
		}
		n, e := strconv.Atoi(port)
		if e != nil {
			return e
		}
		network := "tcp"
		switch service.Listener.Type {
		case "udp", "quic", "kcp":
			network = "udp"
		}
		if err := check(network, n); err != nil {
			return err
		}
	}
	return nil
}
