package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"api-gateway/internal/httpx"
	"api-gateway/internal/requestid"
)

// AccessLog logs one structured line per request. Must run inside requestid.Middleware.
func AccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := httpx.NewStatusRecorder(w)
		next.ServeHTTP(rec, r)
		logger.InfoContext(r.Context(), "request",
			"request_id", requestid.FromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.Status,
			"bytes", rec.Bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}
