package memoryrepo

import "github.com/hongle/hl-panel/internal/control/forwarding"

// setForwardingRuntimeRevision keeps management CAS independent of executable
// state. A new runtime uses the new management revision, so old snapshots and
// receipts keep their original identity without a mass recompilation.
func setForwardingRuntimeRevision(previous forwarding.Rule, next *forwarding.Rule, materialChanged bool) bool {
	quotaExhausted := func(r forwarding.Rule) bool {
		return r.TrafficLimitBytes > 0 && r.TrafficUsedBytes >= r.TrafficLimitBytes
	}
	if materialChanged || !forwarding.SameRuntimeInputs(previous, *next) || quotaExhausted(previous) != quotaExhausted(*next) {
		next.RuntimeRevision = next.Revision
		return true
	}
	next.RuntimeRevision = previous.EffectiveRuntimeRevision()
	next.Deployed, next.Status = previous.Deployed, previous.Status
	next.IngressStatus, next.ActivationReason = previous.IngressStatus, previous.ActivationReason
	return false
}
