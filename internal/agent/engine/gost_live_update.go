package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

type gostObjectChanges struct{ removed, added []json.RawMessage }
type gostLiveChanges struct{ chains, services gostObjectChanges }

func gostChangedObjects(before, after json.RawMessage) (gostObjectChanges, bool) {
	index := func(raw json.RawMessage) ([]json.RawMessage, map[string]json.RawMessage, bool) {
		items := []json.RawMessage{}
		if len(raw) > 0 && json.Unmarshal(raw, &items) != nil {
			return nil, nil, false
		}
		byName := map[string]json.RawMessage{}
		for _, item := range items {
			name := gostObjectName(item)
			if name == "" || byName[name] != nil {
				return nil, nil, false
			}
			byName[name] = item
		}
		return items, byName, true
	}
	previous, old, ok := index(before)
	if !ok {
		return gostObjectChanges{}, false
	}
	candidate, next, ok := index(after)
	if !ok {
		return gostObjectChanges{}, false
	}
	delta := gostObjectChanges{}
	for _, item := range previous {
		if n := next[gostObjectName(item)]; n == nil || !sameXrayJSON(item, n) {
			delta.removed = append(delta.removed, item)
		}
	}
	for _, item := range candidate {
		if p := old[gostObjectName(item)]; p == nil || !sameXrayJSON(item, p) {
			delta.added = append(delta.added, item)
		}
	}
	return delta, true
}

func planGOSTLiveChanges(previous, candidate []byte) (gostLiveChanges, bool) {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(candidate, &after) != nil {
		return gostLiveChanges{}, false
	}
	delta := gostLiveChanges{}
	var ok bool
	delta.chains, ok = gostChangedObjects(before["chains"], after["chains"])
	if !ok {
		return delta, false
	}
	delta.services, ok = gostChangedObjects(before["services"], after["services"])
	if !ok {
		return delta, false
	}
	// A chain replacement must also recreate its dependent services. Never
	// leave old handlers holding a removed chain or restart unrelated services.
	changed := map[string]bool{}
	for _, item := range delta.chains.removed {
		changed[gostObjectName(item)] = true
	}
	for _, side := range []struct {
		doc     map[string]json.RawMessage
		entries *[]json.RawMessage
	}{{before, &delta.services.removed}, {after, &delta.services.added}} {
		seen := map[string]bool{}
		for _, item := range *side.entries {
			seen[gostObjectName(item)] = true
		}
		var services []json.RawMessage
		_ = json.Unmarshal(side.doc["services"], &services)
		for _, item := range services {
			var service struct{ Handler, Listener struct{ Chain string } }
			if json.Unmarshal(item, &service) != nil {
				return delta, false
			}
			if !seen[gostObjectName(item)] && (changed[service.Handler.Chain] || changed[service.Listener.Chain]) {
				*side.entries = append(*side.entries, item)
			}
		}
	}
	for _, doc := range []map[string]json.RawMessage{before, after} {
		delete(doc, "services")
		delete(doc, "chains")
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	return delta, sameXrayJSON(a, b)
}

func (a *GOSTProcessAdapter) tryUpdateGOSTLiveLocked(ctx context.Context, previous, candidate []byte) (bool, error) {
	runner, ok := a.runner.(gostAPIRunner)
	if !ok || a.api == nil || !a.autoStart || a.process == nil || !a.process.Running() || a.processConfigSHA256 != sha256.Sum256(previous) {
		return false, nil
	}
	delta, ok := planGOSTLiveChanges(previous, candidate)
	if !ok {
		return false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := runner.GOSTAPI(callCtx, a.api, http.MethodGet, "/config/services", nil); err != nil {
		return true, ErrGOSTLiveUpdate
	}
	err := a.applyGOSTLiveChanges(callCtx, runner, delta)
	if err == nil && !a.process.Running() {
		err = ErrGOSTNotRunning
	}
	fileAttempted := err == nil
	if fileAttempted {
		err = writeFileAtomic(a.activePath, candidate, 0o600)
	}
	if err == nil {
		a.processConfigSHA256 = sha256.Sum256(candidate)
		return true, nil
	}
	if errors.Is(err, errGOSTAPIUncertain) || !a.process.Running() || a.undoGOSTLiveChanges(context.WithoutCancel(ctx), runner, delta) != nil {
		if restoreErr := a.recoverGOSTLivePreviousLocked(context.WithoutCancel(ctx), previous); restoreErr != nil {
			return true, errors.Join(ErrGOSTLiveUpdate, restoreErr)
		}
	} else if fileAttempted {
		if restoreErr := writeFileAtomic(a.activePath, previous, 0o600); restoreErr != nil {
			a.liveStateUncertain = true
			return true, errors.Join(ErrGOSTLiveUpdate, ErrLiveStateUncertain)
		}
	}
	return true, ErrGOSTLiveUpdate
}

func (a *GOSTProcessAdapter) applyGOSTLiveChanges(ctx context.Context, runner gostAPIRunner, delta gostLiveChanges) error {
	for _, part := range []struct {
		kind  string
		items []json.RawMessage
	}{{"services", delta.services.removed}, {"chains", delta.chains.removed}} {
		for _, item := range part.items {
			if _, err := runner.GOSTAPI(ctx, a.api, http.MethodDelete, "/config/"+part.kind+"/"+url.PathEscape(gostObjectName(item)), nil); err != nil {
				return err
			}
		}
	}
	for _, part := range []struct {
		kind  string
		items []json.RawMessage
	}{{"chains", delta.chains.added}, {"services", delta.services.added}} {
		for _, item := range part.items {
			if _, err := runner.GOSTAPI(ctx, a.api, http.MethodPost, "/config/"+part.kind, item); err != nil {
				return err
			}
			if part.kind == "services" {
				if err := waitGOSTServiceReady(ctx, runner, a.api, gostObjectName(item)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *GOSTProcessAdapter) undoGOSTLiveChanges(ctx context.Context, runner gostAPIRunner, delta gostLiveChanges) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	present := func(kind string, item json.RawMessage) (bool, error) {
		raw, err := runner.GOSTAPI(ctx, a.api, http.MethodGet, "/config/"+kind+"/"+url.PathEscape(gostObjectName(item)), nil)
		if err != nil {
			return false, err
		}
		var result struct{ Data json.RawMessage }
		if json.Unmarshal(raw, &result) != nil || len(result.Data) == 0 {
			return false, ErrGOSTLiveUpdate
		}
		return !bytes.Equal(result.Data, []byte("null")), nil
	}
	// Include both old and candidate names: a DELETE/POST can apply partially.
	// Recreate only objects in the delta; untouched listeners retain sessions.
	for _, part := range []struct {
		kind    string
		changes gostObjectChanges
	}{{"services", delta.services}, {"chains", delta.chains}} {
		seen := map[string]bool{}
		for _, item := range append(append([]json.RawMessage{}, part.changes.added...), part.changes.removed...) {
			name := gostObjectName(item)
			if seen[name] {
				continue
			}
			seen[name] = true
			exists, err := present(part.kind, item)
			if err != nil {
				return err
			}
			if exists {
				if _, err := runner.GOSTAPI(ctx, a.api, http.MethodDelete, "/config/"+part.kind+"/"+url.PathEscape(name), nil); err != nil {
					return err
				}
			}
		}
	}
	for _, part := range []struct {
		kind  string
		items []json.RawMessage
	}{{"chains", delta.chains.removed}, {"services", delta.services.removed}} {
		for _, item := range part.items {
			if _, err := runner.GOSTAPI(ctx, a.api, http.MethodPost, "/config/"+part.kind, item); err != nil {
				return err
			}
			if part.kind == "services" {
				if err := waitGOSTServiceReady(ctx, runner, a.api, gostObjectName(item)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *GOSTProcessAdapter) recoverGOSTLivePreviousLocked(ctx context.Context, previous []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if a.process != nil && a.process.Running() {
		a.liveStateUncertain = true
		_ = writeFileAtomic(a.activePath, previous, 0o600)
		return ErrLiveStateUncertain
	}
	a.process = nil
	if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
		return err
	}
	return a.restartPrevious(ctx, true)
}
