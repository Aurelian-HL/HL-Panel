package gatewaymembership

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Handler uses one independent bearer credential per pool. Administrators and
// enrolled nodes cannot use their normal sessions to fetch gateway membership.
type Handler struct {
	service *Service
	hashes  map[string][sha256.Size]byte
}

func NewHandler(service *Service, poolTokens map[string]string) (*Handler, error) {
	if service == nil || service.repository == nil || len(poolTokens) == 0 {
		return nil, errors.New("gateway membership requires a service and pool credentials")
	}
	hashes := make(map[string][sha256.Size]byte, len(poolTokens))
	for poolID, token := range poolTokens {
		if strings.TrimSpace(poolID) != poolID || poolID == "" || len(token) < 32 || len(token) > 256 ||
			strings.ContainsAny(token, " \t\r\n") {
			return nil, errors.New("invalid gateway pool credential")
		}
		hashes[poolID] = sha256.Sum256([]byte(token))
	}
	return &Handler{service: service, hashes: hashes}, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/gateway/pools/{pool_id}/membership", h.serveSnapshot)
}

func (h *Handler) serveSnapshot(writer http.ResponseWriter, request *http.Request) {
	poolID := request.PathValue("pool_id")
	want, configured := h.hashes[poolID]
	parts := strings.Fields(request.Header.Get("Authorization"))
	if !configured || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") ||
		subtle.ConstantTimeCompare(want[:], hashToken(parts)) != 1 {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(writer, "authentication failed", http.StatusUnauthorized)
		return
	}
	snapshot, err := h.service.Snapshot(request.Context(), poolID)
	if err != nil {
		http.Error(writer, "membership unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(snapshot)
}

func hashToken(parts []string) []byte {
	if len(parts) != 2 {
		return nil
	}
	digest := sha256.Sum256([]byte(parts[1]))
	return digest[:]
}
