package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// planKey issues a key for a fresh application on the named plan.
func (e authEnv) planKey(t *testing.T, plan string) string {
	t.Helper()
	ctx := context.Background()
	appID, err := e.db.EnsureApplication(ctx, plan+"@example.com", plan+" app", plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := e.db.CreateAPIKey(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// exhaust sends limit requests that must all succeed, then checks that request limit+1 is rejected.
func exhaust(t *testing.T, env authEnv, key string, limit int) {
	t.Helper()
	authz := "Bearer " + key
	for i := 1; i <= limit; i++ {
		resp := env.request(t, http.MethodGet, "/users", authz, "")
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, resp.StatusCode)
		}
		if got, want := resp.Header.Get("X-RateLimit-Remaining"), strconv.Itoa(limit-i); got != want {
			t.Fatalf("request %d: X-RateLimit-Remaining = %q, want %q", i, got, want)
		}
		if resp.Header.Get("X-RateLimit-Limit") != strconv.Itoa(limit) {
			t.Fatalf("request %d: X-RateLimit-Limit = %q", i, resp.Header.Get("X-RateLimit-Limit"))
		}
	}

	resp := env.request(t, http.MethodGet, "/users", authz, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request %d: status %d, want 429", limit+1, resp.StatusCode)
	}
	h := resp.Header
	// The env's limiter clock is frozen at hh:mm:30, so the window resets in 30s.
	if h.Get("X-RateLimit-Limit") != strconv.Itoa(limit) || h.Get("X-RateLimit-Remaining") != "0" || h.Get("Retry-After") != "30" {
		t.Errorf("429 headers: Limit=%q Remaining=%q Retry-After=%q",
			h.Get("X-RateLimit-Limit"), h.Get("X-RateLimit-Remaining"), h.Get("Retry-After"))
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error != "rate limit exceeded" || body.RequestID == "" {
		t.Errorf("429 body = %+v (err %v)", body, err)
	}
}

func TestFreePlan101stRequestIsRateLimited(t *testing.T) {
	env := newAuthEnv(t)
	exhaust(t, env, env.planKey(t, "free"), 100)
}

func TestProPlan1001stRequestIsRateLimited(t *testing.T) {
	env := newAuthEnv(t)
	exhaust(t, env, env.planKey(t, "pro"), 1000)
}

func TestRateLimitsArePerKey(t *testing.T) {
	env := newAuthEnv(t)
	first, second := env.planKey(t, "free"), env.activeKey // both on the free plan
	exhaust(t, env, first, 100)

	resp := env.request(t, http.MethodGet, "/users", "Bearer "+second, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-RateLimit-Remaining") != "99" {
		t.Errorf("other key: status %d remaining %q, want its own full quota",
			resp.StatusCode, resp.Header.Get("X-RateLimit-Remaining"))
	}
	if n := strings.Count(env.logs.String(), `"msg":"rate limit exceeded"`); n != 1 {
		t.Errorf("rate limit warnings logged = %d, want 1", n)
	}
}

func TestConcurrentRequestsRespectLimit(t *testing.T) {
	env := newAuthEnv(t)
	authz := "Bearer " + env.planKey(t, "free")

	// 50 concurrent clients (within the OS listen backlog), 5 requests each.
	const workers, perWorker = 50, 5
	const total = workers * perWorker
	var ok, limited atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWorker {
				req, _ := http.NewRequest(http.MethodGet, env.gatewayURL+"/users", nil)
				req.Header.Set("Authorization", authz)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err) // not Fatal: this is not the test goroutine
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					ok.Add(1)
				case http.StatusTooManyRequests:
					limited.Add(1)
				default:
					t.Errorf("unexpected status %d", resp.StatusCode)
				}
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 100 || limited.Load() != total-100 {
		t.Errorf("got %d allowed and %d limited, want exactly 100 and %d", ok.Load(), limited.Load(), total-100)
	}
}
