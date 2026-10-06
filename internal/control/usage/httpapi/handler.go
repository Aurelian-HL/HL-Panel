// Package httpapi exposes the usage ledger without coupling it to the main
// control-plane router. Callers must provide both node and administrator
// authentication callbacks before routes can be registered.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const maxReportBytes = 1 << 20

type Service interface {
	Ingest(context.Context, usage.Report) (usage.IngestResult, error)
	Query(context.Context, usage.Query) (usage.QueryResult, error)
	DesiredEnforcement(context.Context, string) (*agentv1.EnforcementCommand, error)
	RecordEnforcementResult(context.Context, string, agentv1.EnforcementResultRequest) (usage.EnforcementDecision, bool, error)
	RequestRevoke(context.Context, string, string, string) (usage.EnforcementDecision, bool, error)
}

// NodeAuthenticator returns the node ID established from credentials. The
// request body is never trusted as the node identity source.
type NodeAuthenticator func(*http.Request) (string, error)
type AdministratorAuthorizer func(*http.Request) (string, error)

type Handler struct {
	service          Service
	authenticateNode NodeAuthenticator
	authorizeAdmin   AdministratorAuthorizer
}

func New(service Service, authenticateNode NodeAuthenticator, authorizeAdmin AdministratorAuthorizer) (*Handler, error) {
	if service == nil || authenticateNode == nil || authorizeAdmin == nil {
		return nil, errors.New("usage HTTP handler requires service and authentication callbacks")
	}
	return &Handler{service: service, authenticateNode: authenticateNode, authorizeAdmin: authorizeAdmin}, nil
}

func (handler *Handler) Register(mux *http.ServeMux) error {
	if handler == nil || handler.service == nil || handler.authenticateNode == nil || handler.authorizeAdmin == nil || mux == nil {
		return errors.New("usage HTTP handler is not fully configured")
	}
	mux.HandleFunc("POST /api/v1/usage/reports", handler.ingest)
	mux.HandleFunc("GET /api/v1/usage", handler.query)
	mux.HandleFunc("GET /api/v1/usage/enforcement/desired", handler.desiredEnforcement)
	mux.HandleFunc("POST /api/v1/usage/enforcement/results", handler.recordEnforcementResult)
	mux.HandleFunc("POST /api/v1/usage/enforcement/{decision_id}/revoke", handler.revokeEnforcement)
	return nil
}

func (handler *Handler) ingest(writer http.ResponseWriter, request *http.Request) {
	nodeID, err := handler.authenticateNode(request)
	if err != nil || nodeID == "" {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
		return
	}
	var report usage.Report
	if err := decodeJSON(writer, request, &report); err != nil {
		writeUsageProblem(writer, err)
		return
	}
	if report.NodeID != "" && report.NodeID != nodeID {
		writeProblem(writer, http.StatusForbidden, "node_identity_mismatch", "report node_id does not match authenticated node")
		return
	}
	report.NodeID = nodeID
	result, err := handler.service.Ingest(request.Context(), report)
	if err != nil {
		writeUsageProblem(writer, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, map[string]any{"acknowledgement": agentv1.UsageAcknowledgement{
		NodeID: result.Event.NodeID, BootID: result.Event.BootID, Sequence: result.Event.Sequence, Replayed: result.Replayed,
	}})
}

func (handler *Handler) query(writer http.ResponseWriter, request *http.Request) {
	if _, err := handler.authorizeAdmin(request); err != nil {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
		return
	}
	query, err := decodeQuery(request)
	if err != nil {
		writeUsageProblem(writer, err)
		return
	}
	result, err := handler.service.Query(request.Context(), query)
	if err != nil {
		writeUsageProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) desiredEnforcement(writer http.ResponseWriter, request *http.Request) {
	nodeID, err := handler.authenticateNode(request)
	if err != nil || nodeID == "" {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
		return
	}
	command, err := handler.service.DesiredEnforcement(request.Context(), nodeID)
	if err != nil {
		writeUsageProblem(writer, err)
		return
	}
	if command == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, command)
}

func (handler *Handler) recordEnforcementResult(writer http.ResponseWriter, request *http.Request) {
	nodeID, err := handler.authenticateNode(request)
	if err != nil || nodeID == "" {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
		return
	}
	var input agentv1.EnforcementResultRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeUsageProblem(writer, err)
		return
	}
	if _, _, err := handler.service.RecordEnforcementResult(request.Context(), nodeID, input); err != nil {
		writeUsageProblem(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) revokeEnforcement(writer http.ResponseWriter, request *http.Request) {
	administratorID, err := handler.authorizeAdmin(request)
	if err != nil || administratorID == "" {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	decision, replayed, err := handler.service.RequestRevoke(request.Context(), administratorID, request.PathValue("decision_id"), idempotencyKey)
	if err != nil {
		writeUsageProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"decision": decision, "replayed": replayed})
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxReportBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON body: %v", faults.ErrValidation, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: body contains more than one JSON value", faults.ErrValidation)
		}
		return fmt.Errorf("%w: invalid trailing JSON", faults.ErrValidation)
	}
	return nil
}

func decodeQuery(request *http.Request) (usage.Query, error) {
	values := request.URL.Query()
	allowed := map[string]bool{"scope": true, "scope_id": true, "from": true, "to": true, "page": true, "page_size": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return usage.Query{}, fmt.Errorf("%w: unknown or repeated query parameter %q", faults.ErrValidation, key)
		}
	}
	query := usage.Query{Scope: usage.Scope(values.Get("scope")), ScopeID: values.Get("scope_id")}
	var err error
	if raw := values.Get("from"); raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return usage.Query{}, fmt.Errorf("%w: from must be RFC3339", faults.ErrValidation)
		}
		query.From = &value
	}
	if raw := values.Get("to"); raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return usage.Query{}, fmt.Errorf("%w: to must be RFC3339", faults.ErrValidation)
		}
		query.To = &value
	}
	if raw := values.Get("page"); raw != "" {
		query.Page, err = strconv.Atoi(raw)
		if err != nil {
			return usage.Query{}, fmt.Errorf("%w: page must be an integer", faults.ErrValidation)
		}
	}
	if raw := values.Get("page_size"); raw != "" {
		query.PageSize, err = strconv.Atoi(raw)
		if err != nil {
			return usage.Query{}, fmt.Errorf("%w: page_size must be an integer", faults.ErrValidation)
		}
	}
	return query, nil
}

func writeUsageProblem(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, usage.ErrIdempotencyConflict):
		writeProblem(writer, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, usage.ErrSequenceGap):
		writeProblem(writer, http.StatusConflict, "sequence_gap", err.Error())
	case errors.Is(err, usage.ErrSequenceOutOfOrder):
		writeProblem(writer, http.StatusConflict, "sequence_out_of_order", err.Error())
	case errors.Is(err, usage.ErrSequenceOverflow):
		writeProblem(writer, http.StatusConflict, "sequence_overflow", err.Error())
	case errors.Is(err, usage.ErrNegativeBytes):
		writeProblem(writer, http.StatusUnprocessableEntity, "negative_usage_bytes", err.Error())
	case errors.Is(err, usage.ErrInvalidMultiplier):
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_usage_multiplier", err.Error())
	case errors.Is(err, usage.ErrByteOverflow):
		writeProblem(writer, http.StatusUnprocessableEntity, "usage_byte_overflow", err.Error())
	case errors.Is(err, faults.ErrValidation):
		writeProblem(writer, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, faults.ErrNotFound):
		writeProblem(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, faults.ErrConflict):
		writeProblem(writer, http.StatusConflict, "conflict", err.Error())
	default:
		writeProblem(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeProblem(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

var _ Service = (*usage.Service)(nil)
