package forwarding

import (
	"math"
	"time"
)

// ImportedTraffic records a source cumulative observation, not dated ledger
// events. Its direction totals have no inferred historical billing multiplier.
type ImportedTraffic struct {
	SourceID      string    `json:"source_id,omitempty"`
	BatchID       string    `json:"batch_id,omitempty"`
	LineID        int64     `json:"line_id,omitempty"`
	ObservedAt    time.Time `json:"observed_at,omitempty"`
	UploadBytes   int64     `json:"upload_bytes"`
	DownloadBytes int64     `json:"download_bytes"`
}

func (b ImportedTraffic) Valid() bool {
	if b == (ImportedTraffic{}) {
		return true
	}
	return validReference(b.SourceID) && validReference(b.BatchID) && b.LineID > 0 && !b.ObservedAt.IsZero() && b.UploadBytes >= 0 && b.DownloadBytes >= 0 && b.UploadBytes <= math.MaxInt64-b.DownloadBytes
}

// AddTraffic saturates rather than overflowing a quota into a negative value.
func AddTraffic(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func (b ImportedTraffic) Total() int64 { return AddTraffic(b.UploadBytes, b.DownloadBytes) }
