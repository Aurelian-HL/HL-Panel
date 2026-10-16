package gatewaymembership

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sort"
)

const maxBatchPools = 512

// serveBatch requires the independent credential for every requested pool.
// Credentials occur only in a bounded HTTPS request body, never in URLs.
func (h *Handler) serveBatch(w http.ResponseWriter, r *http.Request) {
	var tokens map[string]string
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 192<<10))
	if decoder.Decode(&tokens) != nil || len(tokens) == 0 || len(tokens) > maxBatchPools || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid membership request", http.StatusBadRequest)
		return
	}
	ids := make([]string, 0, len(tokens))
	for id, token := range tokens {
		want, configured := h.hashes[id]
		if !configured || subtle.ConstantTimeCompare(want[:], hashToken([]string{"Bearer", token})) != 1 {
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	snapshots, err := h.service.Snapshots(r.Context(), ids)
	if err != nil {
		http.Error(w, "membership unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snapshots)
}
