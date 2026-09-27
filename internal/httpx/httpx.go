// Package httpx writes the gateway's JSON responses.
package httpx

import (
	"encoding/json"
	"net/http"

	"api-gateway/internal/requestid"
)

// Error writes the gateway's standard error body: {"error": msg, "request_id": ...}.
func Error(w http.ResponseWriter, r *http.Request, status int, msg string) {
	JSON(w, status, map[string]string{
		"error":      msg,
		"request_id": requestid.FromContext(r.Context()),
	})
}

func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
