package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

// lookupTimeout bounds the key lookup so a stalled database fails the request instead of hanging it.
const lookupTimeout = 3 * time.Second

// KeyStore finds an API key by the SHA-256 hash of its raw value.
// It returns ErrKeyNotFound when no key matches.
type KeyStore interface {
	LookupAPIKey(ctx context.Context, hash []byte) (APIKey, error)
}

type ctxKey struct{}

// NewContext returns ctx carrying key's metadata, as Middleware does for authenticated requests.
func NewContext(ctx context.Context, key APIKey) context.Context {
	return context.WithValue(ctx, ctxKey{}, key)
}

// FromContext returns the authenticated key's metadata set by Middleware.
func FromContext(ctx context.Context) (APIKey, bool) {
	k, ok := ctx.Value(ctxKey{}).(APIKey)
	return k, ok
}

// Middleware requires "Authorization: Bearer <api-key>".
//
//	missing or malformed header, unknown key → 401
//	revoked key                              → 403
//	store failure                            → 503 (fail closed)
//	active key                               → next, with APIKey in context
//
// The Authorization header is removed before forwarding so upstreams never see gateway credentials.
// Raw keys are never logged; logs carry only the key's database ID.
func Middleware(store KeyStore, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log := logger.With(
				"request_id", requestid.FromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)

			raw, reason := bearerToken(r.Header)
			if reason != "" {
				log.WarnContext(r.Context(), "authentication failed", "reason", reason)
				unauthorized(w, r, reason != "missing_credential")
				return
			}

			hash := HashKey(raw)
			ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
			key, err := store.LookupAPIKey(ctx, hash)
			cancel()
			switch {
			case errors.Is(err, ErrKeyNotFound):
				log.WarnContext(r.Context(), "authentication failed", "reason", "unknown_key")
				unauthorized(w, r, true)
				return
			case err != nil:
				log.ErrorContext(r.Context(), "api key lookup failed", "error", err.Error())
				httpx.Error(w, r, http.StatusServiceUnavailable, "authentication unavailable")
				return
			}
			// Defense in depth: the store matched on the hash; confirm it without a timing side channel.
			if subtle.ConstantTimeCompare(key.Hash, hash) != 1 {
				log.WarnContext(r.Context(), "authentication failed", "reason", "unknown_key")
				unauthorized(w, r, true)
				return
			}
			if key.Status != StatusActive {
				log.WarnContext(r.Context(), "authentication failed", "reason", "revoked_key", "api_key_id", key.ID)
				httpx.Error(w, r, http.StatusForbidden, "api key revoked")
				return
			}

			log.DebugContext(r.Context(), "authenticated",
				"api_key_id", key.ID, "application_id", key.ApplicationID, "plan_id", key.PlanID)
			r.Header.Del("Authorization")
			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), key)))
		})
	}
}

// bearerToken extracts a well-formed API key, or returns a log-safe failure reason.
func bearerToken(h http.Header) (key, reason string) {
	values := h.Values("Authorization")
	switch {
	case len(values) == 0:
		return "", "missing_credential"
	case len(values) > 1:
		return "", "malformed_credential"
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !wellFormed(token) {
		return "", "malformed_credential"
	}
	return token, ""
}

// unauthorized writes a 401 with an RFC 6750 challenge.
func unauthorized(w http.ResponseWriter, r *http.Request, invalidToken bool) {
	challenge := `Bearer realm="api-gateway"`
	msg := "missing api key"
	if invalidToken {
		challenge += `, error="invalid_token"`
		msg = "invalid api key"
	}
	w.Header().Set("WWW-Authenticate", challenge)
	httpx.Error(w, r, http.StatusUnauthorized, msg)
}
