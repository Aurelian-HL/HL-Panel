package agentv1

// ApplyResultsRequest groups the durable success receipts for one local apply.
// Each phase remains a separate piece of evidence and an audit event.
type ApplyResultsRequest struct {
	Results []ApplyResultRequest `json:"results"`
}

// IsSuccessfulApplyPair deliberately accepts only commit followed by verify for
// the same non-legacy attempt. It does not replace server-side field validation.
func IsSuccessfulApplyPair(results []ApplyResultRequest) bool {
	if len(results) != 2 {
		return false
	}
	a, b := results[0], results[1]
	return a.Generation > 0 && a.AttemptID != "" && a.ConfigSHA256 != "" &&
		a.Phase == ApplyPhaseCommit && b.Phase == ApplyPhaseVerify &&
		a.Status == ApplyStatusSucceeded && b.Status == ApplyStatusSucceeded &&
		a.Generation == b.Generation && a.AttemptID == b.AttemptID &&
		a.ConfigSHA256 == b.ConfigSHA256 && a.EngineMode == b.EngineMode
}
