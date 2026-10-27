package forwarding

import (
	"reflect"
	"time"
)

// EffectiveRuntimeRevision preserves the identity of bundles and protocol
// observations produced before runtime and management revisions were separated.
func (r Rule) EffectiveRuntimeRevision() int64 {
	if r.RuntimeRevision != 0 {
		return r.RuntimeRevision
	}
	return r.Revision
}

// SameRuntimeInputs deliberately defaults new fields to runtime-affecting.
// Only known display, accounting and derived fields are ignored. Quota state
// and private credentials must also be compared by the owning repository.
func SameRuntimeInputs(previous, next Rule) bool {
	scrub := func(r Rule) Rule {
		r.Name, r.Description, r.RuleGroupID = "", "", ""
		r.Revision, r.RuntimeRevision = 0, 0
		r.CreatedAt, r.UpdatedAt = time.Time{}, time.Time{}
		r.Status, r.IngressStatus, r.ActivationReason = "", "", ""
		r.Deployed = false
		r.Connection, r.OutboundSummary = nil, nil
		r.TrafficLimitBytes, r.TrafficUsedBytes = 0, 0
		r.TrafficQuotaMonthly, r.TrafficUsagePeriod = false, ""
		r.ImportedTraffic = ImportedTraffic{}
		r.IngressProtocol = r.EffectiveIngressProtocol()
		if r.IngressProtocol == IngressVLESSReality {
			r.VLESSFlow = r.EffectiveVLESSFlow()
			r.VLESSFlowExplicit = true
		}
		if len(r.Targets) == 0 {
			r.Targets = nil
		}
		return r
	}
	return reflect.DeepEqual(scrub(previous), scrub(next))
}
