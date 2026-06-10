// Package respond provides small helpers for writing JSON HTTP responses.
package respond

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as a JSON body with the given status code.
// A nil v writes only the status code (useful for 204 No Content).
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error body: {"error": "..."}.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
