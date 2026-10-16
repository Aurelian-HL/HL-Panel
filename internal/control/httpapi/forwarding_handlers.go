package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func (api *API) listForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	items, err := api.forwarding.ListForAdministrator(r.Context(), session.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getForwardingRule(w http.ResponseWriter, r *http.Request, session auth.Session) {
	item, err := api.forwarding.GetForAdministrator(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": item})
}

func (api *API) saveForwardingRule(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	var item forwarding.Rule
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.forwarding.CreateForAdministrator(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	} else {
		item, replayed, err = api.forwarding.UpdateForAdministrator(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if api.vlessSetup != nil {
		// The rule transaction is already committed. Endpoint and identity setup
		// can involve several snapshot reads and must not hold the operator's
		// HTTP request open. Run it in the existing reconciliation model and
		// return the committed pending projection immediately. Ensure is
		// idempotent, so a process restart or the 15-second reconciler can safely
		// retry an interrupted attempt.
		api.vlessSetupOnce.Do(func() { api.vlessSetupSlots = make(chan struct{}, 2) })
		select {
		case api.vlessSetupSlots <- struct{}{}:
			go func() {
				defer func() { <-api.vlessSetupSlots }()
				api.reconcileSavedVLESSRule(session.AdminID, item)
			}()
		default:
			// The periodic reconciler picks up excess work. Never create an
			// unbounded goroutine backlog while the database is saturated.
		}
	}
	writeBusinessResult(w, r, "rule", item, replayed)
}

func (api *API) reconcileSavedVLESSRule(administratorID string, item forwarding.Rule) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := api.vlessSetup.Ensure(ctx, administratorID, item); err != nil {
		// The committed rule remains visible as pending activation. The periodic
		// reconciler will retry transient database, endpoint, or node failures.
		api.logger.Warn("vless forwarding rule remains pending activation", "rule_id", item.ID, "admin_id", administratorID, "error", err)
	}
}

func (api *API) listGroupNetworks(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.groupNetworks.List(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getGroupNetwork(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	item, err := api.groupNetworks.Get(r.Context(), r.PathValue("group_id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"network": item})
}

func (api *API) saveGroupNetwork(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input groupconfig.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	item, replayed, err := api.groupNetworks.Update(r.Context(), session.AdminID, r.PathValue("group_id"), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "network", item, replayed)
}
