// Package admin serves the management API: API keys, plans, routes, and analytics.
package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"api-gateway/internal/requestid"
)

// MinTokenLen is the shortest accepted ADMIN_TOKEN (`openssl rand -hex 32` gives 64).
const MinTokenLen = 32

// RequireToken returns middleware admitting only requests with "Authorization: Bearer <token>":
//
//	missing or non-Bearer Authorization → 401
//	wrong token                          → 403
//
// Comparison is constant time over SHA-256 digests, so neither content nor length leaks.
func RequireToken(token string, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if len(token) < MinTokenLen {
		return nil, fmt.Errorf("admin token must be at least %d characters", MinTokenLen)
	}
	want := sha256.Sum256([]byte(token))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, presented, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || presented == "" {
				deny(w, r, logger, http.StatusUnauthorized, "unauthorized", "admin token required", "missing_credential")
				return
			}
			got := sha256.Sum256([]byte(presented))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				deny(w, r, logger, http.StatusForbidden, "forbidden", "invalid admin token", "invalid_credential")
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

func deny(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, code, msg, reason string) {
	logger.WarnContext(r.Context(), "admin access denied",
		"audit", true,
		"reason", reason,
		"request_id", requestid.FromContext(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
	)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
	}
	writeError(w, r, status, code, msg, nil)
}

// fingerprint identifies which admin token was used without revealing it,
// so audit logs can tell tokens apart across rotations.
func fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:4])
}
