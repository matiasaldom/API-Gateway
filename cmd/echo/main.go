// Command echo is a demo upstream service for local runs and benchmarks. It
// answers every request with JSON describing what it received.
//
//	echo -addr :8081 -service users
//
// Query parameters shape the response, so benchmarks can vary the workload:
//
//	size=N     pad the response with N bytes of data (max 1 MiB)
//	delay=D    wait D (a Go duration such as 20ms, max 5s) before answering
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxSize  = 1 << 20
	maxDelay = 5 * time.Second
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	service := flag.String("service", "echo", "service name reported in responses")
	flag.Parse()

	srv := &http.Server{Addr: *addr, Handler: handler(*service), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("echo service %q listening on %s", *service, *addr)
	log.Fatal(srv.ListenAndServe())
}

func handler(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		size, _ := strconv.Atoi(q.Get("size"))
		size = min(max(size, 0), maxSize)
		delay, _ := time.ParseDuration(q.Get("delay"))
		delay = min(max(delay, 0), maxDelay)

		body, _ := io.ReadAll(io.LimitReader(r.Body, maxSize))
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":    service,
			"method":     r.Method,
			"path":       r.URL.Path,
			"query":      r.URL.RawQuery,
			"request_id": r.Header.Get("X-Request-Id"),
			"body_bytes": len(body),
			"served_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"data":       strings.Repeat("x", size),
		})
	})
}
