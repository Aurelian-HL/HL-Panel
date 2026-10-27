package forwarding

import (
	"bytes"
	"encoding/json"
	"time"
)

// SameNativeRuntime prevents ordinary edits from silently diverging from the
// confidential imported transport. Operational fields remain editable and are
// still authorized/CAS checked by the repository.
func SameNativeRuntime(previous, next Rule) bool {
	if previous.VLESSOutboundMode != VLESSOutboundNative || next.VLESSOutboundMode != VLESSOutboundNative {
		return false
	}
	scrub := func(r Rule) []byte {
		r.OutboundSummary = nil
		r.Name = ""
		r.RuleGroupID = ""
		r.Description = ""
		r.Paused = false
		r.Revision = 0
		r.RuntimeRevision = 0
		r.CreatedAt = time.Time{}
		r.UpdatedAt = time.Time{}
		r.Status = ""
		r.IngressStatus = ""
		r.Deployed = false
		r.ActivationReason = ""
		r.TrafficLimitBytes = 0
		r.TrafficUsedBytes = 0
		r.TrafficQuotaMonthly = false
		r.TrafficUsagePeriod = ""
		if len(r.Targets) == 0 {
			r.Targets = nil
		}
		raw, _ := json.Marshal(r)
		return raw
	}
	return bytes.Equal(scrub(previous), scrub(next))
}
