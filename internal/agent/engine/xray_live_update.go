package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"
)

// Native handlers are independently owned by tag. Replacing one handler may
// close that rule's sessions; it must not replace the process and unrelated
// handlers. Keep the existing pure-add path as the cheapest common operation.
type xrayHandlerChanges struct {
	removed, added []json.RawMessage // replacements occur in both lists
}

type xrayLiveChanges struct {
	inbounds, outbounds               xrayHandlerChanges
	previousRouting, candidateRouting json.RawMessage
}

// Record attempts before each RPC: a confirmed error can follow partial
// application. Later stages must never be inferred from the complete plan.
type xrayMutationProgress struct {
	inboundRemovalAttempted, outboundAdditionAttempted bool
	routingAttempted, inboundAdditionAttempted         bool
	outboundRemovalAttempted                           bool
}

func xrayChangedEntries(before, after json.RawMessage) (xrayHandlerChanges, bool) {
	var previous, candidate []json.RawMessage
	if json.Unmarshal(before, &previous) != nil || json.Unmarshal(after, &candidate) != nil {
		return xrayHandlerChanges{}, false
	}
	index := func(entries []json.RawMessage) (map[string]json.RawMessage, bool) {
		byTag := make(map[string]json.RawMessage, len(entries))
		for _, entry := range entries {
			tag := xrayEntryTag(entry)
			if tag == "" || byTag[tag] != nil {
				return nil, false
			}
			byTag[tag] = entry
		}
		return byTag, true
	}
	old, ok := index(previous)
	if !ok {
		return xrayHandlerChanges{}, false
	}
	next, ok := index(candidate)
	if !ok {
		return xrayHandlerChanges{}, false
	}
	var delta xrayHandlerChanges
	for _, entry := range previous {
		if current := next[xrayEntryTag(entry)]; current == nil || !sameXrayJSON(entry, current) {
			delta.removed = append(delta.removed, entry)
		}
	}
	for _, entry := range candidate {
		if prior := old[xrayEntryTag(entry)]; prior == nil || !sameXrayJSON(prior, entry) {
			delta.added = append(delta.added, entry)
		}
	}
	return delta, true
}

// Accept only managed routing with an explicit fallback for every listener.
// Xray's live handler order can differ from file order (especially after
// removing the first outbound), so no listener may depend on the default.
// Global settings, API handlers and routing strategy must remain unchanged.
func planXrayLiveChanges(previous, candidate []byte) (xrayLiveChanges, bool) {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(candidate, &after) != nil {
		return xrayLiveChanges{}, false
	}
	var api struct {
		Tag      string   `json:"tag"`
		Services []string `json:"services"`
	}
	if json.Unmarshal(before["api"], &api) != nil || api.Tag == "" {
		return xrayLiveChanges{}, false
	}
	services := map[string]bool{}
	for _, service := range api.Services {
		services[service] = true
	}
	if !services["HandlerService"] || !services["RoutingService"] {
		return xrayLiveChanges{}, false
	}
	delta := xrayLiveChanges{previousRouting: before["routing"], candidateRouting: after["routing"]}
	var ok bool
	delta.inbounds, ok = xrayChangedEntries(before["inbounds"], after["inbounds"])
	if !ok {
		return xrayLiveChanges{}, false
	}
	delta.outbounds, ok = xrayChangedEntries(before["outbounds"], after["outbounds"])
	if !ok {
		return xrayLiveChanges{}, false
	}
	// AddHandler cannot replace an existing tag. Removing a same-tag outbound
	// first leaves surviving listeners without an egress. Managed compilers
	// assign content versions; legacy/custom same-tag edits require maintenance.
	removedOut := make(map[string]bool, len(delta.outbounds.removed))
	for _, entry := range delta.outbounds.removed {
		removedOut[xrayEntryTag(entry)] = true
	}
	for _, entry := range delta.outbounds.added {
		if removedOut[xrayEntryTag(entry)] {
			return xrayLiveChanges{}, false
		}
	}
	for _, changes := range []xrayHandlerChanges{delta.inbounds, delta.outbounds} {
		for _, entries := range [][]json.RawMessage{changes.removed, changes.added} {
			for _, entry := range entries {
				if tag := xrayEntryTag(entry); tag == api.Tag || tag == "hl-stats-api-in" {
					return xrayLiveChanges{}, false
				}
			}
		}
	}
	for _, document := range []map[string]json.RawMessage{before, after} {
		var outbounds, rules []json.RawMessage
		var routing map[string]json.RawMessage
		if json.Unmarshal(document["outbounds"], &outbounds) != nil ||
			json.Unmarshal(document["routing"], &routing) != nil ||
			json.Unmarshal(routing["rules"], &rules) != nil ||
			!xrayInboundsHaveFallbacks(document["inbounds"], outbounds, rules) {
			return xrayLiveChanges{}, false
		}
		// The generated rules are restricted to known inbound and outbound tags.
		// Arbitrary global/balancer routes are rejected for a live process.
		var inbounds []json.RawMessage
		_ = json.Unmarshal(document["inbounds"], &inbounds)
		inTags, outTags := map[string]bool{}, map[string]bool{}
		for _, entry := range inbounds {
			inTags[xrayEntryTag(entry)] = true
		}
		for _, entry := range outbounds {
			outTags[xrayEntryTag(entry)] = true
		}
		for _, raw := range rules {
			var rule struct {
				InboundTags []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
				BalancerTag string   `json:"balancerTag"`
			}
			if json.Unmarshal(raw, &rule) != nil || len(rule.InboundTags) == 0 ||
				!outTags[rule.OutboundTag] || rule.BalancerTag != "" {
				return xrayLiveChanges{}, false
			}
			for _, tag := range rule.InboundTags {
				if !inTags[tag] {
					return xrayLiveChanges{}, false
				}
			}
		}
		delete(routing, "rules")
		settings, _ := json.Marshal(routing)
		document["routing"] = settings
		delete(document, "inbounds")
		delete(document, "outbounds")
	}
	oldSettings, _ := json.Marshal(before)
	newSettings, _ := json.Marshal(after)
	return delta, sameXrayJSON(oldSettings, newSettings)
}

func (a *XrayProcessAdapter) tryUpdateXrayLiveLocked(ctx context.Context, previous, candidate []byte) (bool, error) {
	api, ok := a.runner.(xrayAPIRunner)
	if !ok || !a.autoStart || a.process == nil || !a.process.Running() || a.processConfigSHA256 != sha256.Sum256(previous) {
		return false, nil
	}
	delta, eligible := planXrayLiveChanges(previous, candidate)
	if !eligible {
		return false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Query before the first mutation. A missing/unreachable API must fail the
	// generation without silently restarting a healthy data plane.
	if _, err := xrayListedTags(callCtx, api, a.statsAPIAddress, "lsi", "inbounds"); err != nil {
		return true, ErrXrayLiveUpdate
	}
	var progress xrayMutationProgress
	err := a.applyXrayLiveChanges(callCtx, api, delta, &progress)
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
	// A deadline can leave a late RPC in flight. Block further mutations on
	// that process; confirmed failures restore only touched handlers.
	if errors.Is(err, errXrayAPIUncertain) || !a.process.Running() ||
		a.undoXrayLiveChanges(context.WithoutCancel(ctx), api, delta, progress) != nil {
		if recoveryErr := a.recoverLivePreviousLocked(context.WithoutCancel(ctx), previous); recoveryErr != nil {
			return true, errors.Join(ErrXrayLiveUpdate, recoveryErr)
		}
	} else if fileAttempted {
		// Replacement can succeed before chmod or directory fsync fails.
		if restoreErr := writeFileAtomic(a.activePath, previous, 0o600); restoreErr != nil {
			a.liveStateUncertain = true
			return true, errors.Join(ErrXrayLiveUpdate, ErrLiveStateUncertain)
		}
	}
	return true, ErrXrayLiveUpdate
}

func (a *XrayProcessAdapter) applyXrayLiveChanges(ctx context.Context, api xrayAPIRunner, delta xrayLiveChanges, progress *xrayMutationProgress) error {
	call := func(operation, key string, entries []json.RawMessage, attempted *bool) error {
		if len(entries) == 0 {
			return nil
		}
		*attempted = true
		_, err := api.API(ctx, a.statsAPIAddress, operation, xrayAPIDocument(key, entries))
		return err
	}
	// Close changed/deleted listeners before removing their routing. Other
	// listeners and established sessions retain the same process and handlers.
	if err := call("rmi", "inbounds", delta.inbounds.removed, &progress.inboundRemovalAttempted); err != nil {
		return err
	}
	// Distinct content tags allow make-before-break: both destinations exist
	// while routing switches. The planner rejects same-tag replacements.
	if err := call("ado", "outbounds", delta.outbounds.added, &progress.outboundAdditionAttempted); err != nil {
		return err
	}
	if !sameXrayJSON(delta.previousRouting, delta.candidateRouting) {
		progress.routingAttempted = true
		if _, err := api.API(ctx, a.statsAPIAddress, "adrules", xrayAPIDocument("routing", delta.candidateRouting)); err != nil {
			return err
		}
	}
	if err := call("adi", "inbounds", delta.inbounds.added, &progress.inboundAdditionAttempted); err != nil {
		return err
	}
	return call("rmo", "outbounds", delta.outbounds.removed, &progress.outboundRemovalAttempted)
}

func xrayListedTags(ctx context.Context, api xrayAPIRunner, address, operation, key string) (map[string]bool, error) {
	raw, err := api.API(ctx, address, operation, nil)
	if err != nil {
		return nil, ErrXrayLiveUpdate
	}
	var document map[string][]json.RawMessage
	if json.Unmarshal(raw, &document) != nil || document[key] == nil {
		return nil, ErrXrayLiveUpdate
	}
	tags := map[string]bool{}
	for _, entry := range document[key] {
		tag := xrayEntryTag(entry)
		if tag == "" || tags[tag] {
			return nil, ErrXrayLiveUpdate
		}
		tags[tag] = true
	}
	return tags, nil
}

func (a *XrayProcessAdapter) undoXrayLiveChanges(ctx context.Context, api xrayAPIRunner, delta xrayLiveChanges, progress xrayMutationProgress) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !progress.inboundRemovalAttempted {
		delta.inbounds.removed = nil
	}
	if !progress.inboundAdditionAttempted {
		delta.inbounds.added = nil
	}
	if !progress.outboundRemovalAttempted {
		delta.outbounds.removed = nil
	}
	if !progress.outboundAdditionAttempted {
		delta.outbounds.added = nil
	}
	// Query only attempted handler families. In particular, a rejected rmi
	// must not remove the still-live old handler as a candidate with the same tag.
	var in, out map[string]bool
	var err error
	if len(delta.inbounds.removed)+len(delta.inbounds.added) != 0 {
		in, err = xrayListedTags(ctx, api, a.statsAPIAddress, "lsi", "inbounds")
		if err != nil {
			return err
		}
	}
	if len(delta.outbounds.removed)+len(delta.outbounds.added) != 0 {
		out, err = xrayListedTags(ctx, api, a.statsAPIAddress, "lso", "outbounds")
		if err != nil {
			return err
		}
	}
	remove := func(operation, key string, entries []json.RawMessage, present map[string]bool) error {
		for _, entry := range entries {
			tag := xrayEntryTag(entry)
			if !present[tag] {
				continue
			}
			if _, err := api.API(ctx, a.statsAPIAddress, operation, xrayAPIDocument(key, []json.RawMessage{entry})); err != nil {
				return err
			}
			delete(present, tag)
		}
		return nil
	}
	add := func(operation, key string, entries []json.RawMessage, present map[string]bool) error {
		var missing []json.RawMessage
		for _, entry := range entries {
			if !present[xrayEntryTag(entry)] {
				missing = append(missing, entry)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		_, err := api.API(ctx, a.statsAPIAddress, operation, xrayAPIDocument(key, missing))
		return err
	}
	if err := remove("rmi", "inbounds", delta.inbounds.added, in); err != nil {
		return err
	}
	// Compensation must also make-before-break. Candidate routes may already
	// serve live listeners after partial success: restore old handlers and
	// routing before retiring any candidate outbound.
	if err := add("ado", "outbounds", delta.outbounds.removed, out); err != nil {
		return err
	}
	// Routing that was not changed by this transaction needs no replacement.
	// Avoid exposing unrelated traffic to an unnecessary global router reload.
	if progress.routingAttempted {
		if _, err := api.API(ctx, a.statsAPIAddress, "adrules", xrayAPIDocument("routing", delta.previousRouting)); err != nil {
			return err
		}
	}
	if err := remove("rmo", "outbounds", delta.outbounds.added, out); err != nil {
		return err
	}
	return add("adi", "inbounds", delta.inbounds.removed, in)
}
