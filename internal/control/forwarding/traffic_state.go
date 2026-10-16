package forwarding

// RuleTrafficState is accounting input, not a deployment or health projection.
// Keeping it separate prevents quota reconciliation from parsing engine bundles.
type RuleTrafficState struct {
	ID                  string
	Revision            int64
	TrafficLimitBytes   int64
	TrafficUsedBytes    int64
	TrafficQuotaMonthly bool
	TrafficUsagePeriod  string
	ImportedTraffic     ImportedTraffic
	CustomerID          string
	EntryGroupID        string
	ExitGroupID         string
}

func TrafficState(rule Rule) RuleTrafficState {
	return RuleTrafficState{ID: rule.ID, Revision: rule.Revision, TrafficLimitBytes: rule.TrafficLimitBytes,
		TrafficUsedBytes: rule.TrafficUsedBytes, TrafficQuotaMonthly: rule.TrafficQuotaMonthly,
		TrafficUsagePeriod: rule.TrafficUsagePeriod, ImportedTraffic: rule.ImportedTraffic,
		CustomerID: rule.CustomerID, EntryGroupID: rule.EntryGroupID, ExitGroupID: rule.ExitGroupID}
}
