package forwarding

import "time"

const (
	MaxImportRules = 500
	ExportSchemaV1 = "nyvp.forwarding-rules/v1"
)

type ImportFormat string
type ImportOperation string
type ImportAction string

const (
	ImportFormatAuto   ImportFormat = "auto"
	ImportFormatNYText ImportFormat = "ny_text"
	ImportFormatJSONV1 ImportFormat = "json_v1"

	ImportCreate ImportOperation = "create"
	ImportUpdate ImportOperation = "update"
	ImportUpsert ImportOperation = "upsert"

	ImportActionCreate ImportAction = "create"
	ImportActionUpdate ImportAction = "update"
)

// ImportMapping supplies the fields absent from NY's legacy four-column text
// and optionally remaps resource identifiers in a versioned JSON document.
type ImportMapping struct {
	CustomerID      string          `json:"customer_id"`
	RuleGroupID     string          `json:"rule_group_id"`
	EntryGroupID    string          `json:"entry_group_id"`
	EgressMode      EgressMode      `json:"egress_mode"`
	ExitGroupID     string          `json:"exit_group_id"`
	IngressProtocol IngressProtocol `json:"ingress_protocol"`
	Protocol        Protocol        `json:"protocol"`
	SelectionPolicy SelectionPolicy `json:"selection_policy"`
}

type TransferRequest struct {
	Format  ImportFormat  `json:"format"`
	Content string        `json:"content"`
	Mapping ImportMapping `json:"mapping"`
}

type ImportIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ImportRow struct {
	Line       int             `json:"line"`
	Operation  ImportOperation `json:"operation"`
	Action     ImportAction    `json:"action,omitempty"`
	SourceID   string          `json:"source_id,omitempty"`
	Request    Request         `json:"request"`
	ListenPort int             `json:"resolved_listen_port,omitempty"`
	Issues     []ImportIssue   `json:"issues"`
}

type ImportPreview struct {
	Format       ImportFormat  `json:"format"`
	Total        int           `json:"total"`
	Valid        int           `json:"valid"`
	Invalid      int           `json:"invalid"`
	Rows         []ImportRow   `json:"rows"`
	GlobalIssues []ImportIssue `json:"global_issues"`
}

func (p ImportPreview) CanCommit() bool {
	return p.Total > 0 && p.Total <= MaxImportRules && p.Invalid == 0 && len(p.GlobalIssues) == 0
}

type ImportCandidate struct {
	Line             int
	Operation        ImportOperation
	Rule             Rule
	ExpectedRevision int64
}

type ImportEvaluation struct {
	Line   int
	Action ImportAction
	Rule   Rule
	Issue  *ImportIssue
}

type ImportInput struct {
	Candidates     []ImportCandidate
	IdempotencyKey string
	RequestSHA256  string
	CreatedBy      string
}

type ImportResult struct {
	Created int    `json:"created"`
	Updated int    `json:"updated"`
	Items   []Rule `json:"items"`
}

type ExportRule struct {
	Operation ImportOperation `json:"operation"`
	ID        string          `json:"id"`
	Rule      Request         `json:"rule"`
}

type ExportDocument struct {
	SchemaVersion string       `json:"schema_version"`
	ExportedAt    time.Time    `json:"exported_at"`
	Rules         []ExportRule `json:"rules"`
}
