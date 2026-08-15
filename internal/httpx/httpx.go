// Package httpx holds the JSON response helpers shared by the auth middleware
// and the API handlers, so the error envelope is defined exactly once.
package httpx

import (
	"encoding/json"
	"log"
	"net/http"
)

// Error codes used across every JSON endpoint.
const (
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeInvalidRequest  = "invalid_request"
	CodeAlreadyExists   = "already_exists"
	CodeVersionConflict = "version_conflict"
	CodeInternal        = "internal_error"
)

// ErrorBody is the inner object of every error response.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorEnvelope is the uniform error shape. Current is only populated on a
// version conflict, where the caller needs to see the record as it stands.
type ErrorEnvelope struct {
	Error   ErrorBody `json:"error"`
	Current any       `json:"current,omitempty"`
}

// WriteJSON serialises v as the response body with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("httpx: marshal response: %v", err)
		WriteError(w, http.StatusInternalServerError, CodeInternal, "failed to encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}

// WriteError emits the uniform {"error":{"code","message"}} envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteErrorWithCurrent(w, status, code, message, nil)
}

// WriteErrorWithCurrent emits an error envelope that also carries the record as
// it currently stands, which is what a 409 needs to be actionable.
func WriteErrorWithCurrent(w http.ResponseWriter, status int, code, message string, current any) {
	body, err := json.Marshal(ErrorEnvelope{
		Error:   ErrorBody{Code: code, Message: message},
		Current: current,
	})
	if err != nil {
		http.Error(w, `{"error":{"code":"internal_error","message":"failed to encode error"}}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}
