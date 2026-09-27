// Package tests holds integration tests that run the gateway against real HTTP upstreams.
package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-gateway/internal/config"
	"api-gateway/internal/proxy"
	"api-gateway/internal/requestid"
)

// echo is what the upstream test server reports back about the request it received.
type echo struct {
	Service string      `json:"service"`
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Query   string      `json:"query"`
	Header  http.Header `json:"header"`
	Body    string      `json:"body"`
}

func newEchoUpstream(t *testing.T, service string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(echoHandler(service))
	t.Cleanup(srv.Close)
	return srv
}

// echoHandler reports the request it received back as JSON (201 for POST).
func echoHandler(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", service)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(echo{
			Service: service,
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Header:  r.Header,
			Body:    string(body),
		})
	}
}

func newGateway(t *testing.T, timeout time.Duration, logs io.Writer, routes ...config.Route) http.Handler {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	h, err := proxy.NewHandler(&config.Config{UpstreamTimeout: timeout, Routes: routes}, slog.New(slog.NewJSONHandler(logs, nil)), noAuth)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// startGateway runs the gateway as a real HTTP server in front of the users and albums upstreams.
func startGateway(t *testing.T) *httptest.Server {
	t.Helper()
	users := newEchoUpstream(t, "users")
	albums := newEchoUpstream(t, "albums")
	gw := httptest.NewServer(newGateway(t, 5*time.Second, nil,
		config.Route{Prefix: "/users", Upstream: users.URL},
		config.Route{Prefix: "/albums", Upstream: albums.URL},
	))
	t.Cleanup(gw.Close)
	return gw
}

func decodeEcho(t *testing.T, resp *http.Response) echo {
	t.Helper()
	defer resp.Body.Close()
	var e echo
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode upstream echo: %v", err)
	}
	return e
}

func TestProxyGET(t *testing.T) {
	gw := startGateway(t)

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/users/42?expand=albums&tag=a&tag=b", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("X-Custom", "value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") != "users" {
		t.Errorf("upstream response header not forwarded: %v", resp.Header)
	}
	e := decodeEcho(t, resp)

	if e.Service != "users" || e.Method != http.MethodGet || e.Path != "/users/42" {
		t.Errorf("got service=%s method=%s path=%s", e.Service, e.Method, e.Path)
	}
	if e.Query != "expand=albums&tag=a&tag=b" {
		t.Errorf("query = %q, want preserved verbatim", e.Query)
	}
	if e.Header.Get("Authorization") != "Bearer token" || e.Header.Get("X-Custom") != "value" {
		t.Errorf("request headers not forwarded: %v", e.Header)
	}
	if e.Header.Get("X-Forwarded-For") == "" {
		t.Error("X-Forwarded-For not set")
	}
}

func TestProxyPOST(t *testing.T) {
	gw := startGateway(t)

	payload := `{"title":"Blue Train"}`
	resp, err := http.Post(gw.URL+"/albums?draft=true", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	e := decodeEcho(t, resp)

	if e.Service != "albums" || e.Method != http.MethodPost || e.Path != "/albums" || e.Query != "draft=true" {
		t.Errorf("got service=%s method=%s path=%s query=%s", e.Service, e.Method, e.Path, e.Query)
	}
	if e.Body != payload {
		t.Errorf("body = %q, want %q", e.Body, payload)
	}
	if e.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", e.Header.Get("Content-Type"))
	}
}

func TestRequestIDPropagation(t *testing.T) {
	gw := startGateway(t)

	t.Run("generated", func(t *testing.T) {
		resp, err := http.Get(gw.URL + "/users")
		if err != nil {
			t.Fatal(err)
		}
		id := resp.Header.Get(requestid.Header)
		e := decodeEcho(t, resp)
		if len(id) != 32 {
			t.Fatalf("response request ID = %q, want 32 hex chars", id)
		}
		if got := e.Header.Get(requestid.Header); got != id {
			t.Errorf("upstream saw request ID %q, response has %q", got, id)
		}
	})

	t.Run("client supplied", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, gw.URL+"/users", nil)
		req.Header.Set(requestid.Header, "client-abc-123")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		e := decodeEcho(t, resp)
		if resp.Header.Get(requestid.Header) != "client-abc-123" || e.Header.Get(requestid.Header) != "client-abc-123" {
			t.Errorf("client request ID not preserved: response=%q upstream=%q",
				resp.Header.Get(requestid.Header), e.Header.Get(requestid.Header))
		}
	})

	t.Run("malformed client value replaced", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, gw.URL+"/users", nil)
		req.Header.Set(requestid.Header, "bad id with spaces")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if id := resp.Header.Get(requestid.Header); len(id) != 32 {
			t.Errorf("malformed request ID not replaced, got %q", id)
		}
	})
}

func TestRequestIDInLogs(t *testing.T) {
	upstream := newEchoUpstream(t, "users")
	var logs bytes.Buffer
	h := newGateway(t, 5*time.Second, &logs, config.Route{Prefix: "/users", Upstream: upstream.URL})

	// Served in-process so the access log is fully written when ServeHTTP returns.
	req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	req.Header.Set(requestid.Header, "log-test-id")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("access log is not a single JSON line: %v\n%s", err, logs.String())
	}
	if entry["request_id"] != "log-test-id" || entry["path"] != "/users/1" || entry["status"] != float64(200) {
		t.Errorf("unexpected access log entry: %v", entry)
	}
}

func TestUpstreamTimeoutReturns504(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)

	var logs bytes.Buffer
	h := newGateway(t, 100*time.Millisecond, &logs, config.Route{Prefix: "/users", Upstream: slow.URL})
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set(requestid.Header, "timeout-id")
	rec := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(rec, req)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout not enforced, took %s", elapsed)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "timeout-id") {
		t.Errorf("error body missing request ID: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), `"msg":"upstream request failed"`) || !strings.Contains(logs.String(), `"request_id":"timeout-id"`) {
		t.Errorf("upstream failure not logged with request ID:\n%s", logs.String())
	}
}

func TestUnreachableUpstreamReturns502(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close() // grab a free port, then stop listening

	gw := httptest.NewServer(newGateway(t, 5*time.Second, nil, config.Route{Prefix: "/users", Upstream: dead.URL}))
	t.Cleanup(gw.Close)

	resp, err := http.Get(gw.URL + "/users")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHealthAndUnknownRoute(t *testing.T) {
	gw := startGateway(t)

	resp, err := http.Get(gw.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("health: status=%d body=%s", resp.StatusCode, body)
	}

	resp, err = http.Get(gw.URL + "/photos")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown route: status = %d, want 404", resp.StatusCode)
	}
}

// noAuth lets proxy tests exercise forwarding without credentials; auth is covered in auth_test.go.
func noAuth(next http.Handler) http.Handler { return next }
