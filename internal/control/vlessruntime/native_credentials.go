package vlessruntime

// NativeCredentialsRotation is a local maintenance contract, never a public
// API DTO. Every active material must be explicitly pinned by its revision.
type NativeCredentialsRotation struct {
	AdministratorID   string           `json:"administrator_id"`
	RuleID            string           `json:"rule_id"`
	ExpectedRevision  int64            `json:"expected_revision"`
	MaterialRevisions map[string]int64 `json:"material_revisions"`
	Host              string           `json:"host"`
	Port              int              `json:"port"`
	Username          string           `json:"username"`
	Password          string           `json:"password"`
	IdempotencyKey    string           `json:"idempotency_key"`
}

type NativeCredentialsResult struct {
	RuleID    string `json:"rule_id"`
	Revision  int64  `json:"revision"`
	Materials int    `json:"materials"`
	Replayed  bool   `json:"replayed"`
}
