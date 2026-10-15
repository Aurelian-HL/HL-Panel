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
}

func TrafficState(rule Rule) RuleTrafficState {
	return RuleTrafficState{rule.ID, rule.Revision, rule.TrafficLimitBytes,
		rule.TrafficUsedBytes, rule.TrafficQuotaMonthly, rule.TrafficUsagePeriod}
}
