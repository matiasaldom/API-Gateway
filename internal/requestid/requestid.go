// Package requestid assigns every request an ID that is logged, forwarded upstream, and echoed to the client.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const Header = "X-Request-ID"

type ctxKey struct{}

// FromContext returns the request ID set by Middleware, or "".
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Middleware reuses a well-formed incoming X-Request-ID or generates a new one,
// stores it in the request context, forwards it upstream, and echoes it in the response.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !valid(id) {
			id = newID()
		}
		r.Header.Set(Header, id)
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// valid accepts short IDs of URL-safe characters so client-supplied
// values can't bloat or corrupt logs and upstream headers.
func valid(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b[:])
}
