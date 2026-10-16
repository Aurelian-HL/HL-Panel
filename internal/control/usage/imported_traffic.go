package usage

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func (s *Service) importedTrafficTotals(ctx context.Context, q Query) (*ImportedTrafficTotals, error) {
	if s.ruleTraffic == nil || q.From != nil || q.To != nil {
		return nil, nil
	}
	var rules []forwarding.RuleTrafficState
	if reader, ok := s.ruleTraffic.(RuleTrafficStateReader); ok {
		if q.Scope == ScopeRule {
			r, err := reader.RuleTrafficState(ctx, q.ScopeID)
			if errors.Is(err, faults.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			rules = []forwarding.RuleTrafficState{r}
		} else {
			var err error
			rules, err = reader.ListRuleTrafficStates(ctx)
			if err != nil {
				return nil, err
			}
		}
	} else {
		full, err := s.ruleTraffic.ListForwardingRules(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range full {
			rules = append(rules, forwarding.TrafficState(r))
		}
	}
	var out ImportedTrafficTotals
	for _, r := range rules {
		matches := q.Scope == ScopeSite || q.Scope == ScopeRule && r.ID == q.ScopeID || q.Scope == ScopeCustomer && r.CustomerID == q.ScopeID || q.Scope == ScopeDeviceGroup && (r.EntryGroupID == q.ScopeID || r.ExitGroupID == q.ScopeID)
		if !matches || r.ImportedTraffic.SourceID == "" {
			continue
		}
		out.RuleCount++
		out.UploadBytes = forwarding.AddTraffic(out.UploadBytes, r.ImportedTraffic.UploadBytes)
		out.DownloadBytes = forwarding.AddTraffic(out.DownloadBytes, r.ImportedTraffic.DownloadBytes)
		if r.ImportedTraffic.ObservedAt.After(out.LatestObservedAt) {
			out.LatestObservedAt = r.ImportedTraffic.ObservedAt
		}
	}
	if out.RuleCount == 0 {
		return nil, nil
	}
	out.TotalBytes = forwarding.AddTraffic(out.UploadBytes, out.DownloadBytes)
	return &out, nil
}
