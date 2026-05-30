// Package httpx holds small HTTP helpers shared across the server and gateway —
// JSON responses in the api/v1 Error shape. No business logic lives here.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an error body matching the api/v1 Error schema
// ({code, message[, details]}).
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{"code": code, "message": message})
}

// WriteErrorDetails is WriteError with an extra structured details object.
func WriteErrorDetails(w http.ResponseWriter, status int, code, message string, details any) {
	WriteJSON(w, status, map[string]any{"code": code, "message": message, "details": details})
}
