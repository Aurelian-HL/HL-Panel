package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

var ErrXrayLiveUpdate = errors.New("xray incremental update failed")

// An expired command cannot attest whether its RPC was applied. Compensation
// alone is insufficient: a late RPC could otherwise mutate the restored state.
var errXrayAPIUncertain = errors.New("xray API completion unknown")

// This optional runner capability only uses the owned binary and a validated
// loopback API. Native configuration is passed over stdin, never argv or logs.
type xrayAPIRunner interface {
	API(context.Context, string, string, []byte) ([]byte, error)
}

type xrayAdditions struct {
	inbounds, outbounds               []json.RawMessage
	previousRouting, candidateRouting json.RawMessage
}

// Plan only disjoint additions. Edits, deletions and reordered existing rules
// are evaluated by the general live-change planner; global changes on a live
// process are refused until explicit maintenance.
// Every added route must be restricted to newly added inbound tags, so it
// cannot change dispatch for any existing listener. A changed first outbound
// is safe only when every listener has an explicit unconditional route.
func planXrayAdditions(previous, candidate []byte) (xrayAdditions, bool) {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(candidate, &after) != nil {
		return xrayAdditions{}, false
	}
	var api struct {
		Services []string `json:"services"`
	}
	if json.Unmarshal(before["api"], &api) != nil {
		return xrayAdditions{}, false
	}
	services := map[string]bool{}
	for _, service := range api.Services {
		services[service] = true
	}
	if !services["HandlerService"] || !services["RoutingService"] {
		return xrayAdditions{}, false
	}
	delta := xrayAdditions{previousRouting: before["routing"], candidateRouting: after["routing"]}
	_, newIn, ok := xrayAddedEntries(before["inbounds"], after["inbounds"])
	if !ok || len(newIn) == 0 {
		return xrayAdditions{}, false
	}
	_, newOut, ok := xrayAddedEntries(before["outbounds"], after["outbounds"])
	if !ok {
		return xrayAdditions{}, false
	}
	var oldOut, nextOut []json.RawMessage
	if json.Unmarshal(before["outbounds"], &oldOut) != nil || json.Unmarshal(after["outbounds"], &nextOut) != nil ||
		len(oldOut) == 0 || len(nextOut) == 0 {
		return xrayAdditions{}, false
	}
	addedTags := map[string]bool{}
	for _, raw := range newIn {
		addedTags[xrayEntryTag(raw)] = true
	}
	var oldRouting, newRouting map[string]json.RawMessage
	if json.Unmarshal(before["routing"], &oldRouting) != nil || json.Unmarshal(after["routing"], &newRouting) != nil {
		return xrayAdditions{}, false
	}
	var oldRules, newRules []json.RawMessage
	if json.Unmarshal(oldRouting["rules"], &oldRules) != nil || json.Unmarshal(newRouting["rules"], &newRules) != nil {
		return xrayAdditions{}, false
	}
	// Xray's AddOutbound appends handlers. If a newly sorted rule becomes the
	// first outbound in the file, the running process keeps its old default.
	// This is equivalent only when no inbound can ever use that default.
	if !sameXrayJSON(oldOut[0], nextOut[0]) && !xrayInboundsHaveFallbacks(after["inbounds"], nextOut, newRules) {
		return xrayAdditions{}, false
	}
	index := 0
	for _, raw := range newRules {
		if index < len(oldRules) && sameXrayJSON(raw, oldRules[index]) {
			index++
			continue
		}
		var rule struct {
			InboundTags []string `json:"inboundTag"`
		}
		if json.Unmarshal(raw, &rule) != nil || len(rule.InboundTags) == 0 {
			return xrayAdditions{}, false
		}
		for _, tag := range rule.InboundTags {
			if !addedTags[tag] {
				return xrayAdditions{}, false
			}
		}
	}
	if index != len(oldRules) {
		return xrayAdditions{}, false
	}
	delete(oldRouting, "rules")
	delete(newRouting, "rules")
	oldSettings, _ := json.Marshal(oldRouting)
	newSettings, _ := json.Marshal(newRouting)
	if !sameXrayJSON(oldSettings, newSettings) {
		return xrayAdditions{}, false
	}
	for _, key := range []string{"inbounds", "outbounds", "routing"} {
		delete(before, key)
		delete(after, key)
	}
	oldSettings, _ = json.Marshal(before)
	newSettings, _ = json.Marshal(after)
	if !sameXrayJSON(oldSettings, newSettings) {
		return xrayAdditions{}, false
	}
	delta.inbounds, delta.outbounds = newIn, newOut
	return delta, true
}

func xrayInboundsHaveFallbacks(inbounds json.RawMessage, outbounds, rules []json.RawMessage) bool {
	var entries []json.RawMessage
	if json.Unmarshal(inbounds, &entries) != nil || len(entries) == 0 {
		return false
	}
	missing := make(map[string]bool, len(entries))
	for _, entry := range entries {
		tag := xrayEntryTag(entry)
		if tag == "" {
			return false
		}
		missing[tag] = true
	}
	outTags := make(map[string]bool, len(outbounds))
	for _, entry := range outbounds {
		tag := xrayEntryTag(entry)
		if tag == "" {
			return false
		}
		outTags[tag] = true
	}
	for _, raw := range rules {
		var rule map[string]json.RawMessage
		if json.Unmarshal(raw, &rule) != nil || len(rule) != 3 {
			continue
		}
		var kind, outbound string
		var inboundTags []string
		if json.Unmarshal(rule["type"], &kind) != nil || kind != "field" ||
			json.Unmarshal(rule["outboundTag"], &outbound) != nil || !outTags[outbound] ||
			json.Unmarshal(rule["inboundTag"], &inboundTags) != nil || len(inboundTags) == 0 {
			continue
		}
		for _, tag := range inboundTags {
			delete(missing, tag)
		}
	}
	return len(missing) == 0
}

func xrayAddedEntries(previous, candidate json.RawMessage) ([]json.RawMessage, []json.RawMessage, bool) {
	var old, next []json.RawMessage
	if json.Unmarshal(previous, &old) != nil || json.Unmarshal(candidate, &next) != nil {
		return nil, nil, false
	}
	byTag := map[string]json.RawMessage{}
	for _, raw := range old {
		tag := xrayEntryTag(raw)
		if tag == "" || byTag[tag] != nil {
			return nil, nil, false
		}
		byTag[tag] = raw
	}
	added := []json.RawMessage{}
	seen := map[string]bool{}
	for _, raw := range next {
		tag := xrayEntryTag(raw)
		if tag == "" || seen[tag] {
			return nil, nil, false
		}
		seen[tag] = true
		if prior, exists := byTag[tag]; exists {
			if !sameXrayJSON(prior, raw) {
				return nil, nil, false
			}
		} else {
			added = append(added, raw)
		}
	}
	for tag := range byTag {
		if !seen[tag] {
			return nil, nil, false
		}
	}
	return old, added, true
}

func xrayEntryTag(raw json.RawMessage) string {
	var entry struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(raw, &entry)
	return entry.Tag
}

func sameXrayJSON(left, right []byte) bool {
	l, le := canonicalJSON(left)
	r, re := canonicalJSON(right)
	return le == nil && re == nil && bytes.Equal(l, r)
}

func xrayAPIDocument(key string, value any) []byte {
	data, _ := json.Marshal(map[string]any{key: value})
	return data
}

// tryAddLiveLocked never silently falls back to a restart on an API failure.
// An unsuccessful operation is compensated while the previous process lives;
// Uncertain RPC completion or failed compensation blocks subsequent mutations
// while preserving the process. Only an already dead process may be recovered.
func (a *XrayProcessAdapter) tryAddLiveLocked(ctx context.Context, previous, candidate []byte) (bool, error) {
	api, ok := a.runner.(xrayAPIRunner)
	if !ok || !a.autoStart || a.process == nil || !a.process.Running() || a.processConfigSHA256 != sha256.Sum256(previous) {
		return false, nil
	}
	delta, ok := planXrayAdditions(previous, candidate)
	if !ok {
		return false, nil
	}
	callContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var err error
	var progress xrayMutationProgress
	if len(delta.outbounds) != 0 {
		progress.outboundAdditionAttempted = true
		_, err = api.API(callContext, a.statsAPIAddress, "ado", xrayAPIDocument("outbounds", delta.outbounds))
	}
	if err == nil {
		progress.routingAttempted = true
		_, err = api.API(callContext, a.statsAPIAddress, "adrules", xrayAPIDocument("routing", delta.candidateRouting))
	}
	if err == nil {
		progress.inboundAdditionAttempted = true
		_, err = api.API(callContext, a.statsAPIAddress, "adi", xrayAPIDocument("inbounds", delta.inbounds))
	}
	if err == nil && !a.process.Running() {
		err = ErrXrayNotRunning
	}
	fileAttempted := err == nil
	if fileAttempted {
		err = writeFileAtomic(a.activePath, candidate, 0o600)
	}
	if err == nil {
		a.processConfigSHA256 = sha256.Sum256(candidate)
		return true, nil
	}
	if errors.Is(err, errXrayAPIUncertain) || !a.process.Running() ||
		a.undoXrayAdditions(context.WithoutCancel(ctx), api, delta, progress) != nil {
		if recoveryErr := a.recoverLivePreviousLocked(context.WithoutCancel(ctx), previous); recoveryErr != nil {
			return true, errors.Join(ErrXrayLiveUpdate, recoveryErr)
		}
	} else if fileAttempted {
		if restoreErr := writeFileAtomic(a.activePath, previous, 0o600); restoreErr != nil {
			a.liveStateUncertain = true
			return true, errors.Join(ErrXrayLiveUpdate, ErrLiveStateUncertain)
		}
	}
	return true, ErrXrayLiveUpdate
}

// Use the same bounded compensation as edits, including actual tag queries
// for partial additions. A failed early stage cannot reload untouched routing.
func (a *XrayProcessAdapter) undoXrayAdditions(ctx context.Context, api xrayAPIRunner, delta xrayAdditions, progress xrayMutationProgress) error {
	return a.undoXrayLiveChanges(ctx, api, xrayLiveChanges{
		inbounds:        xrayHandlerChanges{added: delta.inbounds},
		outbounds:       xrayHandlerChanges{added: delta.outbounds},
		previousRouting: delta.previousRouting, candidateRouting: delta.candidateRouting,
	}, progress)
}

func (a *XrayProcessAdapter) recoverLivePreviousLocked(ctx context.Context, previous []byte) error {
	if a.process != nil && a.process.Running() {
		a.liveStateUncertain = true
		// Keep a durable safe boot target, but never claim it reflects the
		// live process after a timed-out mutation or failed compensation.
		_ = writeFileAtomic(a.activePath, previous, 0o600)
		return ErrLiveStateUncertain
	}
	a.process = nil
	if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
		return err
	}
	return a.restartPrevious(ctx, true)
}

func (r *ExecXrayRunner) API(ctx context.Context, address, operation string, document []byte) ([]byte, error) {
	if _, _, err := xrayStatsListener(address); err != nil {
		return nil, ErrXrayLiveUpdate
	}
	switch operation {
	case "adi", "ado", "adrules", "rmi", "rmo", "lsi", "lso":
	default:
		return nil, ErrXrayLiveUpdate
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// The process deadline expires before the CLI's RPC deadline, letting us
	// distinguish an uncertain RPC outcome and block further live mutations.
	args := []string{"api", operation, "--server=" + address, "--timeout=6"}
	if operation == "lsi" {
		args = append(args, "--isOnlyTags=true")
	}
	command := exec.CommandContext(ctx, r.binaryPath, args...)
	command.Stdin = bytes.NewReader(document)
	command.Stderr = io.Discard
	if operation == "lsi" || operation == "lso" {
		output, err := command.Output()
		if err != nil || len(output) > MaxConfigurationBytes {
			return nil, ErrXrayLiveUpdate
		}
		return output, nil
	}
	command.Stdout = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errXrayAPIUncertain
		}
		return nil, ErrXrayLiveUpdate
	}
	return nil, nil
}
