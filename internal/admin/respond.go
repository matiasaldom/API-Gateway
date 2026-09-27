package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"api-gateway/internal/requestid"
)

const maxBodyBytes = 64 << 10

// apiError is the structured error body for every admin failure. It extends the
// gateway's {"error", "request_id"} shape with a machine-readable code and, for
// validation failures, per-field messages.
type apiError struct {
	Error     string            `json:"error"`
	Code      string            `json:"code"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store") // responses can include one-time secrets
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, apiError{Error: msg, Code: code, Fields: fields, RequestID: requestid.FromContext(r.Context())})
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	writeError(w, r, http.StatusBadRequest, "validation_failed", "request validation failed", fields)
}

// decodeJSON reads a single JSON object into dst, rejecting non-JSON content
// types, oversized bodies, unknown fields, and trailing data. On failure it has
// already written the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("body must contain a single JSON object")
	}
	if err == nil {
		return true
	}

	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("body exceeds %d bytes", maxBodyBytes), nil)
	case errors.As(err, &typeErr) && typeErr.Field != "":
		validationFailed(w, r, map[string]string{typeErr.Field: "must be a " + jsonType(typeErr.Type.Kind().String())})
	case errors.Is(err, io.EOF):
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body is empty", nil)
	default:
		// Decoder messages describe the client's own input (syntax position, unknown field name).
		writeError(w, r, http.StatusBadRequest, "invalid_json", strings.TrimPrefix(err.Error(), "json: "), nil)
	}
	return false
}

func jsonType(goKind string) string {
	switch {
	case strings.HasPrefix(goKind, "int"), strings.HasPrefix(goKind, "uint"):
		return "whole number"
	case goKind == "string":
		return "string"
	default:
		return goKind
	}
}

// pathID parses the {id} path segment as a positive integer.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		validationFailed(w, r, map[string]string{"id": "must be a positive integer"})
		return 0, false
	}
	return id, true
}

// onlyParams rejects query parameters outside allowed, so typos fail loudly.
func onlyParams(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	fields := map[string]string{}
	for name := range r.URL.Query() {
		if !slices.Contains(allowed, name) {
			fields[name] = "unknown query parameter"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return false
	}
	return true
}

// methods dispatches by HTTP method and answers anything else with a JSON 405.
type methods map[string]http.HandlerFunc

func (m methods) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := m[r.Method]; ok {
		h(w, r)
		return
	}
	allowed := make([]string, 0, len(m))
	for method := range m {
		allowed = append(allowed, method)
	}
	slices.Sort(allowed)
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
}
