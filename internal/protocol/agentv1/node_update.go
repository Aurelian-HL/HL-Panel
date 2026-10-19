package agentv1

type NodeUpdateTask struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archive_sha256"`
	Lease         string `json:"lease"`
}
type NodeUpdateResult struct {
	ID                 string `json:"id"`
	Lease              string `json:"lease"`
	State              string `json:"state"`
	Phase              string `json:"phase"`
	BackupDirectory    string `json:"backup_directory,omitempty"`
	VerifiedGeneration int64  `json:"verified_generation"`
	VerifiedSHA256     string `json:"verified_sha256,omitempty"`
}
