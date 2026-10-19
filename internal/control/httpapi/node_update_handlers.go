package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"net/http"
)

func WithNodeUpdates(s *nodeupdates.Service) Option { return func(api *API) { api.nodeUpdates = s } }
func (api *API) nodeUpdateStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	status, err := api.nodeUpdates.Status(r.Context(), r.PathValue("node_id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, status)
}
func (api *API) startNodeUpdate(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var in struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeProblem(w, r, err)
		return
	}
	task, err := api.nodeUpdates.Start(r.Context(), session.AdminID, r.PathValue("node_id"), r.Header.Get("Idempotency-Key"), in.Version)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, 202, task)
}
func (api *API) claimNodeUpdate(w http.ResponseWriter, r *http.Request, n nodes.Node) {
	task, err := api.nodeUpdates.Claim(r.Context(), n.ID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if task == nil {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, task)
}
func (api *API) cancelNodeUpdate(w http.ResponseWriter, r *http.Request, session auth.Session) {
	task, err := api.nodeUpdates.Cancel(r.Context(), session.AdminID, r.PathValue("node_id"), r.PathValue("task_id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, 200, task)
}
func (api *API) reportNodeUpdate(w http.ResponseWriter, r *http.Request, n nodes.Node) {
	var in agentv1.NodeUpdateResult
	if err := decodeJSON(w, r, &in); err != nil {
		writeProblem(w, r, err)
		return
	}
	if err := api.nodeUpdates.Report(r.Context(), n.ID, in); err != nil {
		writeProblem(w, r, err)
		return
	}
	w.WriteHeader(204)
}
