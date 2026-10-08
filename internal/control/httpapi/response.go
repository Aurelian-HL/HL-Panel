package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

const maxRequestBytes = 2 << 20

type problemEnvelope struct {
	Error problem `json:"error"`
}

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON body: %v", faults.ErrValidation, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: body contains more than one JSON value", faults.ErrValidation)
		}
		return fmt.Errorf("%w: invalid trailing JSON: %v", faults.ErrValidation, err)
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-HL-Server-Time", time.Now().UTC().Format(time.RFC3339Nano))
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, _ *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "internal server error"
	switch {
	case errors.Is(err, faults.ErrValidation):
		status, code, message = http.StatusBadRequest, "validation_error", err.Error()
	case errors.Is(err, faults.ErrUnauthorized), errors.Is(err, faults.ErrExpired), errors.Is(err, faults.ErrAlreadyUsed):
		status, code, message = http.StatusUnauthorized, "unauthorized", "authentication failed"
	case errors.Is(err, faults.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, faults.ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", err.Error()
	case errors.Is(err, faults.ErrConflict):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	}
	writeJSON(writer, status, problemEnvelope{Error: problem{Code: code, Message: message}})
}

func writeInternalProblem(writer http.ResponseWriter) {
	writeJSON(writer, http.StatusInternalServerError, problemEnvelope{Error: problem{Code: "internal_error", Message: "internal server error"}})
}
