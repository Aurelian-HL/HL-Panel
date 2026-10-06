package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
)

type createEndpointPoolRequest struct {
	Name            string `json:"name"`
	GroupID         string `json:"group_id"`
	RuleID          string `json:"rule_id"`
	Mode            string `json:"mode"`
	Protocol        string `json:"protocol"`
	Hostname        string `json:"hostname"`
	Port            int    `json:"port"`
	SelectionPolicy string `json:"selection_policy"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type addEndpointPoolMemberRequest struct {
	NodeID         string `json:"node_id"`
	Weight         int    `json:"weight"`
	Priority       int    `json:"priority"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (api *API) createEndpointPool(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input createEndpointPoolRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	pool, replayed, err := api.endpoints.CreateBound(
		request.Context(), session.AdminID, input.Name, input.GroupID, input.RuleID,
		endpoints.Mode(input.Mode), input.Protocol, input.Hostname, input.Port,
		endpoints.SelectionPolicy(input.SelectionPolicy), input.IdempotencyKey,
	)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, struct {
		Pool     endpoints.EndpointPool `json:"pool"`
		Replayed bool                   `json:"replayed"`
	}{Pool: pool, Replayed: replayed})
}

func (api *API) listEndpointPools(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	items, err := api.endpoints.ListPoolsForAdministrator(request.Context(), session.AdminID)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []endpoints.EndpointPool `json:"items"`
	}{Items: items})
}

func (api *API) deleteEndpointPool(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	replayed, err := api.endpoints.Delete(request.Context(), session.AdminID, request.PathValue("pool_id"), request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Replayed bool `json:"replayed"`
	}{Replayed: replayed})
}

func (api *API) addEndpointPoolMember(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input addEndpointPoolMemberRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	member, replayed, err := api.endpoints.AddMember(
		request.Context(), session.AdminID, request.PathValue("pool_id"), input.NodeID,
		input.Weight, input.Priority, input.IdempotencyKey,
	)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Member   endpoints.EndpointPoolMember `json:"member"`
		Replayed bool                         `json:"replayed"`
	}{Member: member, Replayed: replayed})
}
