package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"api-gateway/internal/auth"
	"api-gateway/internal/metrics"
	"api-gateway/internal/requestid"
	"api-gateway/internal/routing"
	"api-gateway/internal/storage"
)

const (
	defaultPageSize = 100
	maxPageSize     = 1000
	maxPlanLimit    = 1_000_000
	maxCacheTTL     = 24 * time.Hour
	maxNameLen      = 100
)

type handler struct {
	db        *storage.DB
	routes    *routing.Table
	analytics *metrics.Analytics
	logger    *slog.Logger
	actor     string

	routeMu sync.Mutex // serializes route edits so the live table matches the last write
}

// New returns the management API, protected by token:
//
//	POST  /admin/api-keys                create a key (the raw key is returned once)
//	GET   /admin/api-keys                list keys (?status, ?application_id, ?after, ?limit)
//	POST  /admin/api-keys/{id}/revoke    revoke a key
//	GET   /admin/plans                   list plans
//	PATCH /admin/plans/{id}              change a plan's requests_per_minute
//	GET   /admin/routes                  list routes
//	PATCH /admin/routes/{id}             change a route's upstream and/or cache_ttl (live)
//	GET   /admin/analytics/{summary,routes,accounts}
//	GET   /analytics/{summary,routes,accounts}   (earlier paths, same handlers)
//
// Every response is JSON; every mutation writes an "admin action" audit log line.
func New(db *storage.DB, routes *routing.Table, analytics *metrics.Analytics, token string, logger *slog.Logger) (http.Handler, error) {
	requireToken, err := RequireToken(token, logger)
	if err != nil {
		return nil, err
	}
	h := &handler{db: db, routes: routes, analytics: analytics, logger: logger, actor: "admin:" + fingerprint(token)}

	mux := http.NewServeMux()
	mux.Handle("/admin/api-keys", methods{http.MethodGet: h.listKeys, http.MethodPost: h.createKey})
	mux.Handle("/admin/api-keys/{id}/revoke", methods{http.MethodPost: h.revokeKey})
	mux.Handle("/admin/plans", methods{http.MethodGet: h.listPlans})
	mux.Handle("/admin/plans/{id}", methods{http.MethodPatch: h.updatePlan})
	mux.Handle("/admin/routes", methods{http.MethodGet: h.listRoutes})
	mux.Handle("/admin/routes/{id}", methods{http.MethodPatch: h.updateRoute})
	for _, prefix := range []string{"/admin/analytics", "/analytics"} {
		mux.Handle(prefix+"/summary", methods{http.MethodGet: analytics.Summary})
		mux.Handle(prefix+"/routes", methods{http.MethodGet: analytics.Routes})
		mux.Handle(prefix+"/accounts", methods{http.MethodGet: analytics.Accounts})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "unknown admin endpoint", nil)
	})
	return requireToken(mux), nil
}

// audit writes one structured line per state change. Raw API keys never appear.
func (h *handler) audit(r *http.Request, action string, attrs ...any) {
	base := []any{
		"audit", true,
		"action", action,
		"actor", h.actor,
		"request_id", requestid.FromContext(r.Context()),
		"remote_addr", r.RemoteAddr,
	}
	h.logger.InfoContext(r.Context(), "admin action", append(base, attrs...)...)
}

func (h *handler) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger.ErrorContext(r.Context(), "admin request failed",
		"op", op, "request_id", requestid.FromContext(r.Context()), "error", err.Error())
	writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error", nil)
}

func notFound(w http.ResponseWriter, r *http.Request, what string) {
	writeError(w, r, http.StatusNotFound, "not_found", what+" not found", nil)
}

// ---------- API keys ----------

type keyView struct {
	ID            int64      `json:"id"`
	Status        string     `json:"status"`
	ApplicationID int64      `json:"application_id"`
	Application   string     `json:"application"`
	OwnerEmail    string     `json:"owner_email"`
	Plan          string     `json:"plan"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
}

func toKeyView(k storage.KeyInfo) keyView {
	return keyView{k.ID, string(k.Status), k.ApplicationID, k.Application, k.OwnerEmail, k.Plan, k.CreatedAt, k.RevokedAt}
}

func (h *handler) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string  `json:"email"`
		Application string  `json:"application"`
		Plan        *string `json:"plan"`
	}
	if !onlyParams(w, r) || !decodeJSON(w, r, &req) {
		return
	}
	email, app, plan := strings.TrimSpace(req.Email), strings.TrimSpace(req.Application), "free"
	if req.Plan != nil {
		plan = strings.TrimSpace(*req.Plan)
	}
	fields := map[string]string{}
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		fields["email"] = "must be a plain email address, e.g. dev@example.com"
	}
	if msg := checkName(app); msg != "" {
		fields["application"] = msg
	}
	if msg := checkName(plan); msg != "" {
		fields["plan"] = msg
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	appID, err := h.db.EnsureApplication(r.Context(), email, app, plan)
	if errors.Is(err, storage.ErrUnknownPlan) {
		validationFailed(w, r, map[string]string{"plan": "unknown plan"})
		return
	}
	if err != nil {
		h.internalError(w, r, "ensure application", err)
		return
	}
	raw, id, err := h.db.CreateAPIKey(r.Context(), appID)
	if err != nil {
		h.internalError(w, r, "create api key", err)
		return
	}
	info, err := h.db.GetAPIKey(r.Context(), id)
	if err != nil {
		h.internalError(w, r, "get api key", err)
		return
	}
	h.audit(r, "api_key.create", "api_key_id", id, "application_id", appID, "plan", info.Plan)
	writeJSON(w, http.StatusCreated, struct {
		APIKey string  `json:"api_key"`
		Note   string  `json:"note"`
		Key    keyView `json:"key"`
	}{raw, "Store this key now; it cannot be retrieved again.", toKeyView(info)})
}

func (h *handler) listKeys(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "status", "application_id", "after", "limit") {
		return
	}
	q := r.URL.Query()
	f := storage.KeyFilter{Limit: defaultPageSize}
	fields := map[string]string{}
	switch s := auth.Status(q.Get("status")); s {
	case "", auth.StatusActive, auth.StatusRevoked:
		f.Status = s
	default:
		fields["status"] = "must be active or revoked"
	}
	if v := q.Get("application_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			f.ApplicationID = n
		} else {
			fields["application_id"] = "must be a positive integer"
		}
	}
	if v := q.Get("after"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			f.AfterID = n
		} else {
			fields["after"] = "must be a non-negative integer"
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxPageSize {
			f.Limit = n
		} else {
			fields["limit"] = "must be between 1 and 1000"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	keys, err := h.db.ListAPIKeys(r.Context(), f)
	if err != nil {
		h.internalError(w, r, "list api keys", err)
		return
	}
	views := make([]keyView, len(keys))
	for i, k := range keys {
		views[i] = toKeyView(k)
	}
	var next *int64 // cursor for the following page, when this one is full
	if len(keys) == f.Limit {
		next = &keys[len(keys)-1].ID
	}
	writeJSON(w, http.StatusOK, struct {
		APIKeys   []keyView `json:"api_keys"`
		NextAfter *int64    `json:"next_after"`
	}{views, next})
}

func (h *handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !onlyParams(w, r) {
		return
	}
	before, err := h.db.GetAPIKey(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		notFound(w, r, "api key")
		return
	}
	if err != nil {
		h.internalError(w, r, "get api key", err)
		return
	}
	if before.Status != auth.StatusRevoked {
		if err := h.db.RevokeAPIKey(r.Context(), id); err != nil {
			h.internalError(w, r, "revoke api key", err)
			return
		}
	}
	after, err := h.db.GetAPIKey(r.Context(), id)
	if err != nil {
		h.internalError(w, r, "get api key", err)
		return
	}
	h.audit(r, "api_key.revoke", "api_key_id", id, "application_id", after.ApplicationID,
		"previous_status", string(before.Status))
	writeJSON(w, http.StatusOK, toKeyView(after))
}

// ---------- plans ----------

type planView struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	RequestsPerMinute int       `json:"requests_per_minute"`
	Applications      int       `json:"applications"`
	CreatedAt         time.Time `json:"created_at"`
}

func toPlanView(p storage.Plan) planView {
	return planView{p.ID, p.Name, p.RequestsPerMinute, p.Applications, p.CreatedAt}
}

func (h *handler) listPlans(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	plans, err := h.db.ListPlans(r.Context())
	if err != nil {
		h.internalError(w, r, "list plans", err)
		return
	}
	views := make([]planView, len(plans))
	for i, p := range plans {
		views[i] = toPlanView(p)
	}
	writeJSON(w, http.StatusOK, struct {
		Plans []planView `json:"plans"`
	}{views})
}

func (h *handler) updatePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !onlyParams(w, r) {
		return
	}
	var req struct {
		RequestsPerMinute *int `json:"requests_per_minute"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RequestsPerMinute == nil {
		validationFailed(w, r, map[string]string{"requests_per_minute": "required"})
		return
	}
	if n := *req.RequestsPerMinute; n < 1 || n > maxPlanLimit {
		validationFailed(w, r, map[string]string{"requests_per_minute": "must be between 1 and 1000000"})
		return
	}

	before, err := h.db.GetPlan(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		notFound(w, r, "plan")
		return
	}
	if err != nil {
		h.internalError(w, r, "get plan", err)
		return
	}
	after, err := h.db.UpdatePlanLimit(r.Context(), id, *req.RequestsPerMinute)
	if err != nil {
		h.internalError(w, r, "update plan", err)
		return
	}
	h.audit(r, "plan.update", "plan_id", id, "plan", after.Name,
		"requests_per_minute_before", before.RequestsPerMinute, "requests_per_minute_after", after.RequestsPerMinute)
	writeJSON(w, http.StatusOK, toPlanView(after))
}

// ---------- routes ----------

type routeView struct {
	ID         int64     `json:"id"`
	Prefix     string    `json:"prefix"`
	Upstream   string    `json:"upstream"`
	CacheTTL   string    `json:"cache_ttl"`
	CacheTTLMS int64     `json:"cache_ttl_ms"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toRouteView(rt storage.RouteRecord) routeView {
	return routeView{rt.ID, rt.Prefix, rt.Upstream, rt.CacheTTL.String(), rt.CacheTTL.Milliseconds(), rt.CreatedAt, rt.UpdatedAt}
}

func (h *handler) listRoutes(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	routes, err := h.db.ListRoutes(r.Context())
	if err != nil {
		h.internalError(w, r, "list routes", err)
		return
	}
	views := make([]routeView, len(routes))
	for i, rt := range routes {
		views[i] = toRouteView(rt)
	}
	writeJSON(w, http.StatusOK, struct {
		Routes []routeView `json:"routes"`
	}{views})
}

// updateRoute changes a route's upstream and/or cache TTL. The prefix is fixed
// (routes are declared in gateway.yaml). The change is stored, then the live
// route table is rebuilt from the database, so it applies to the next request.
func (h *handler) updateRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !onlyParams(w, r) {
		return
	}
	var req struct {
		Upstream *string `json:"upstream"`
		CacheTTL *string `json:"cache_ttl"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if req.Upstream == nil && req.CacheTTL == nil {
		fields["upstream"] = "provide upstream and/or cache_ttl"
	}
	var upstream string
	if req.Upstream != nil {
		upstream = strings.TrimSpace(*req.Upstream)
		if err := routing.ValidateUpstream(upstream); err != nil {
			fields["upstream"] = "must be an absolute http(s) URL with a host and no query, fragment, or credentials"
		}
	}
	var ttl time.Duration
	if req.CacheTTL != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*req.CacheTTL))
		switch {
		case err != nil:
			fields["cache_ttl"] = `must be a duration such as "0s", "500ms", "30s", or "5m"`
		case d < 0 || d > maxCacheTTL:
			fields["cache_ttl"] = "must be between 0s and 24h"
		case d%time.Millisecond != 0:
			fields["cache_ttl"] = "must be a whole number of milliseconds"
		}
		ttl = d
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	h.routeMu.Lock()
	defer h.routeMu.Unlock()

	before, err := h.db.GetRoute(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		notFound(w, r, "route")
		return
	}
	if err != nil {
		h.internalError(w, r, "get route", err)
		return
	}
	if req.Upstream == nil {
		upstream = before.Upstream
	}
	if req.CacheTTL == nil {
		ttl = before.CacheTTL
	}
	after, err := h.db.UpdateRoute(r.Context(), id, upstream, ttl)
	if err != nil {
		h.internalError(w, r, "update route", err)
		return
	}
	if err := h.reloadRoutes(r); err != nil {
		h.internalError(w, r, "reload routes", err)
		return
	}
	h.audit(r, "route.update", "route_id", id, "prefix", after.Prefix,
		"upstream_before", before.Upstream, "upstream_after", after.Upstream,
		"cache_ttl_before", before.CacheTTL.String(), "cache_ttl_after", after.CacheTTL.String())
	writeJSON(w, http.StatusOK, toRouteView(after))
}

// reloadRoutes rebuilds the live route table from the database. Caller holds routeMu.
func (h *handler) reloadRoutes(r *http.Request) error {
	records, err := h.db.ListRoutes(r.Context())
	if err != nil {
		return err
	}
	router, err := routing.New(storage.RouteConfigs(records))
	if err != nil {
		return err
	}
	h.routes.Replace(router)
	return nil
}

// checkName validates a human-readable name: required, bounded, printable.
func checkName(s string) string {
	switch {
	case s == "":
		return "required"
	case utf8.RuneCountInString(s) > maxNameLen:
		return "must be at most 100 characters"
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "must not contain control characters"
	}
	return ""
}
