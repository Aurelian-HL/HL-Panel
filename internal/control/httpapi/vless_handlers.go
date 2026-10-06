package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

type provisionVLESSIdentityRequest struct {
	CustomerID       string `json:"customer_id"`
	ForwardingRuleID string `json:"forwarding_rule_id"`
	EndpointPoolID   string `json:"endpoint_pool_id"`
	Revision         int64  `json:"revision"`
}

type vlessRevisionRequest struct {
	Revision int64 `json:"revision"`
}

type bindVLESSRuntimeMaterialRequest struct {
	BindingID         string `json:"binding_id"`
	NodeID            string `json:"node_id"`
	RealityPrivateKey string `json:"reality_private_key"`
	Revision          int64  `json:"revision"`
}

func (api *API) listVLESSIdentities(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	items, err := api.vlessIdentity.List(request.Context(), session.AdminID)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (api *API) provisionVLESSIdentity(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input provisionVLESSIdentityRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	item, replayed, err := api.vlessIdentity.Provision(request.Context(), session.AdminID, vlessidentity.ProvisionRequest{
		CustomerID: input.CustomerID, ForwardingRuleID: input.ForwardingRuleID,
		EndpointPoolID: input.EndpointPoolID, Revision: input.Revision,
	}, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "identity", item, replayed)
}

func (api *API) rotateVLESSIdentity(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input vlessRevisionRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	item, replayed, err := api.vlessIdentity.Rotate(request.Context(), session.AdminID, request.PathValue("id"), vlessidentity.MutationRequest{Revision: input.Revision}, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "identity", item, replayed)
}

func (api *API) revokeVLESSIdentity(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input vlessRevisionRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	item, replayed, err := api.vlessIdentity.Revoke(request.Context(), session.AdminID, request.PathValue("id"), vlessidentity.MutationRequest{Revision: input.Revision}, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "identity", item, replayed)
}

func (api *API) listVLESSRuntimeMaterials(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	items, err := api.vlessRuntime.List(request.Context(), session.AdminID)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (api *API) bindVLESSRuntimeMaterial(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input bindVLESSRuntimeMaterialRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	item, replayed, err := api.vlessRuntime.Bind(request.Context(), session.AdminID, input.BindingID, input.NodeID, input.RealityPrivateKey, input.Revision, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "material", item, replayed)
}

func (api *API) revokeVLESSRuntimeMaterial(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input vlessRevisionRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	item, replayed, err := api.vlessRuntime.Revoke(request.Context(), session.AdminID, request.PathValue("id"), input.Revision, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "material", item, replayed)
}
