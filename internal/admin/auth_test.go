package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "0123456789abcdef0123456789abcdef"

func TestRequireTokenRejectsShortToken(t *testing.T) {
	if _, err := RequireToken(strings.Repeat("x", MinTokenLen-1), slog.New(slog.DiscardHandler)); err == nil {
		t.Error("expected an error for a token shorter than 32 characters")
	}
}

func TestRequireToken(t *testing.T) {
	var logs bytes.Buffer
	mw, err := RequireToken(token, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	tests := []struct {
		name, authorization string
		wantStatus          int
		wantCode            string
	}{
		{"missing header", "", 401, "unauthorized"},
		{"basic scheme", "Basic " + token, 401, "unauthorized"},
		{"empty bearer", "Bearer ", 401, "unauthorized"},
		{"scheme only", "Bearer", 401, "unauthorized"},
		{"wrong token", "Bearer " + strings.Repeat("z", 32), 403, "forbidden"},
		{"token prefix", "Bearer " + token[:31], 403, "forbidden"},
		{"token plus suffix", "Bearer " + token + "x", 403, "forbidden"},
		{"valid", "Bearer " + token, 204, ""},
		{"valid, lowercase scheme", "bearer " + token, 204, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/plans", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCode == "" {
				return
			}
			var body apiError
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != tt.wantCode {
				t.Errorf("body = %s, want code %q", rec.Body, tt.wantCode)
			}
			if tt.wantStatus == 401 && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
	if strings.Contains(logs.String(), token) {
		t.Error("admin token written to logs")
	}
	if !strings.Contains(logs.String(), `"reason":"invalid_credential"`) || !strings.Contains(logs.String(), `"reason":"missing_credential"`) {
		t.Errorf("denials not audited:\n%s", logs.String())
	}
}
