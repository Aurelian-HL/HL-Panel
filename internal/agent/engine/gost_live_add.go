package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrGOSTLiveUpdate = errors.New("gost incremental update failed")
var errGOSTAPIUncertain = errors.New("gost API completion unknown")

// Agent-owned control endpoint. It never appears in panel fragments or argv.
// A new secret and loopback port are generated for each adapter incarnation.
type gostLocalAPI struct {
	Addr string `json:"addr"`
	Auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"auth"`
}

func newGOSTLocalAPI() (*gostLocalAPI, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	api := &gostLocalAPI{Addr: addr}
	api.Auth.Username = "hl-agent"
	api.Auth.Password = hex.EncodeToString(secret[:])
	return api, nil
}

type gostAPIRunner interface {
	GOSTAPI(context.Context, *gostLocalAPI, string, string, []byte) ([]byte, error)
}

// Use a fixed loopback destination, no proxies, no redirects, bounded responses
// and generic errors. Do not leak tunnel credentials in engine diagnostics.
func (r *ExecGOSTRunner) GOSTAPI(ctx context.Context, api *gostLocalAPI, method, path string, body []byte) ([]byte, error) {
	host, _, err := net.SplitHostPort(api.Addr)
	if err != nil || host != "127.0.0.1" {
		return nil, ErrGOSTLiveUpdate
	}
	if path != "/config/chains" && path != "/config/services" {
		if method != http.MethodDelete && method != http.MethodGet {
			return nil, ErrGOSTLiveUpdate
		}
		u, err := url.Parse(path)
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, ErrGOSTLiveUpdate
		}
		parts := strings.Split(u.Path, "/")
		if len(parts) != 4 || parts[0] != "" || parts[1] != "config" || (parts[2] != "services" && parts[2] != "chains") || parts[3] == "" || parts[3] == "." || parts[3] == ".." {
			return nil, ErrGOSTLiveUpdate
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+api.Addr+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrGOSTLiveUpdate
	}
	request.SetBasicAuth(api.Auth.Username, api.Auth.Password)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, errGOSTAPIUncertain
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxConfigurationBytes+1))
	if err != nil || len(raw) > MaxConfigurationBytes {
		return nil, errGOSTAPIUncertain
	}
	var result struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil, errGOSTAPIUncertain
	}
	if response.StatusCode != http.StatusOK || result.Code != 0 {
		return nil, ErrGOSTLiveUpdate
	}
	return raw, nil
}

// Only reverse a recorded pure addition. This path is used when a later
// mixed-engine or journal step fails after GOST was already committed.
func (a *GOSTProcessAdapter) tryRollbackGOSTLiveLocked(ctx context.Context, active, previous []byte) (bool, error) {
	runner, ok := a.runner.(gostAPIRunner)
	if !ok || a.api == nil || !a.autoStart || a.process == nil || !a.process.Running() || a.processConfigSHA256 != sha256.Sum256(active) {
		return false, nil
	}
	delta, ok := planGOSTAdditions(previous, active)
	if !ok {
		return false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, part := range []struct {
		kind  string
		items []json.RawMessage
	}{{"services", delta.services}, {"chains", delta.chains}} {
		for i := len(part.items) - 1; i >= 0; i-- {
			if _, err := runner.GOSTAPI(callCtx, a.api, http.MethodDelete, "/config/"+part.kind+"/"+url.PathEscape(gostObjectName(part.items[i])), nil); err != nil {
				return true, errors.Join(ErrGOSTLiveUpdate, a.recoverGOSTLivePreviousLocked(context.WithoutCancel(ctx), previous))
			}
		}
	}
	if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
		a.liveStateUncertain = true
		return true, errors.Join(ErrGOSTLiveUpdate, ErrLiveStateUncertain)
	}
	a.processConfigSHA256 = sha256.Sum256(previous)
	return true, nil
}

type gostAdditions struct{ chains, services []json.RawMessage }

// Preserve every existing named object and every global setting. Edits and
// removals are evaluated by the general live-change planner. Adding a chain
// cannot reroute old services.
func planGOSTAdditions(previous, candidate []byte) (gostAdditions, bool) {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(candidate, &after) != nil {
		return gostAdditions{}, false
	}
	delta := gostAdditions{}
	var ok bool
	delta.chains, ok = gostAddedEntries(before["chains"], after["chains"])
	if !ok {
		return delta, false
	}
	delta.services, ok = gostAddedEntries(before["services"], after["services"])
	if !ok || len(delta.services) == 0 {
		return delta, false
	}
	for _, key := range []string{"chains", "services"} {
		delete(before, key)
		delete(after, key)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	return delta, sameXrayJSON(a, b)
}

func gostAddedEntries(before, after json.RawMessage) ([]json.RawMessage, bool) {
	var old, next []json.RawMessage
	if len(before) > 0 && json.Unmarshal(before, &old) != nil {
		return nil, false
	}
	if len(after) > 0 && json.Unmarshal(after, &next) != nil {
		return nil, false
	}
	existing := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for _, item := range old {
		name := gostObjectName(item)
		if name == "" || existing[name] != nil {
			return nil, false
		}
		existing[name] = item
	}
	var added []json.RawMessage
	for _, item := range next {
		name := gostObjectName(item)
		if name == "" || seen[name] {
			return nil, false
		}
		seen[name] = true
		if prior, ok := existing[name]; ok {
			if !sameXrayJSON(prior, item) {
				return nil, false
			}
		} else {
			added = append(added, item)
		}
	}
	for name := range existing {
		if !seen[name] {
			return nil, false
		}
	}
	return added, true
}
func gostObjectName(raw []byte) string {
	var object struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &object)
	return object.Name
}

func (a *GOSTProcessAdapter) tryAddGOSTLiveLocked(ctx context.Context, previous, candidate []byte) (bool, error) {
	runner, ok := a.runner.(gostAPIRunner)
	if !ok || a.api == nil || !a.autoStart || a.process == nil || !a.process.Running() || a.processConfigSHA256 != sha256.Sum256(previous) {
		return false, nil
	}
	delta, ok := planGOSTAdditions(previous, candidate)
	if !ok {
		return false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Readiness and authentication are checked before any mutation.
	if _, err := runner.GOSTAPI(callCtx, a.api, http.MethodGet, "/config/services", nil); err != nil {
		return true, ErrGOSTLiveUpdate
	}
	type object struct{ kind, name string }
	var created []object
	var applyErr error
	uncertain := false
	fileAttempted := false
	for _, part := range []struct {
		kind  string
		items []json.RawMessage
	}{{"chains", delta.chains}, {"services", delta.services}} {
		for _, raw := range part.items {
			item := object{part.kind, gostObjectName(raw)}
			// Include the failed POST in compensation: its response can be lost.
			created = append(created, item)
			if _, err := runner.GOSTAPI(callCtx, a.api, http.MethodPost, "/config/"+part.kind, raw); err != nil {
				applyErr = err
				uncertain = errors.Is(err, errGOSTAPIUncertain)
				break
			}
			if part.kind == "services" {
				if err := waitGOSTServiceReady(callCtx, runner, a.api, item.name); err != nil {
					applyErr = err
					break
				}
			}
		}
		if applyErr != nil {
			break
		}
	}
	if applyErr == nil && !a.process.Running() {
		applyErr = ErrGOSTNotRunning
	}
	if applyErr == nil {
		fileAttempted = true
		applyErr = writeFileAtomic(a.activePath, candidate, 0o600)
		if applyErr == nil {
			a.processConfigSHA256 = sha256.Sum256(candidate)
			return true, nil
		}
	}
	// An incomplete HTTP mutation might finish after compensation. Preserve the
	// process and block subsequent mutations rather than racing that late write.
	recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer recoveryCancel()
	if !uncertain {
		for i := len(created) - 1; i >= 0; i-- {
			item := created[i]
			_, err := runner.GOSTAPI(recoveryCtx, a.api, http.MethodDelete, "/config/"+item.kind+"/"+url.PathEscape(item.name), nil)
			if err != nil {
				if errors.Is(err, errGOSTAPIUncertain) {
					uncertain = true
					break
				}
				// DELETE of a rejected POST may report missing: confirm absence.
				raw, getErr := runner.GOSTAPI(recoveryCtx, a.api, http.MethodGet, "/config/"+item.kind+"/"+url.PathEscape(item.name), nil)
				var result struct {
					Data json.RawMessage `json:"data"`
				}
				if getErr != nil || json.Unmarshal(raw, &result) != nil || len(result.Data) > 0 && !bytes.Equal(result.Data, []byte("null")) {
					uncertain = true
					break
				}
			}
		}
	}
	if uncertain || !a.process.Running() {
		if err := a.recoverGOSTLivePreviousLocked(recoveryCtx, previous); err != nil {
			return true, errors.Join(ErrGOSTLiveUpdate, err)
		}
	} else if fileAttempted {
		// Atomic replacement may succeed before chmod or directory fsync fails.
		// Restore the complete file as well as compensating the live API.
		if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
			a.liveStateUncertain = true
			return true, errors.Join(ErrGOSTLiveUpdate, ErrLiveStateUncertain)
		}
	}
	return true, ErrGOSTLiveUpdate
}

func waitGOSTServiceReady(ctx context.Context, runner gostAPIRunner, api *gostLocalAPI, name string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		raw, err := runner.GOSTAPI(readyCtx, api, http.MethodGet, "/config/services/"+url.PathEscape(name), nil)
		if err != nil {
			return err
		}
		var result struct {
			Data struct {
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return ErrGOSTLiveUpdate
		}
		switch result.Data.Status.State {
		case "ready":
			return nil
		case "failed", "closed":
			return ErrGOSTLiveUpdate
		}
		select {
		case <-readyCtx.Done():
			return ErrGOSTLiveUpdate
		case <-ticker.C:
		}
	}
}
