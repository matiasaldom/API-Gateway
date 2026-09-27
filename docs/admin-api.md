# Management API

Manage API keys, plan limits and routes, and read analytics, over HTTP. The `apikey` CLI still works, but you no longer need it.

## Authentication

Every `/admin/*` and `/analytics/*` endpoint requires the admin token:

```sh
export ADMIN_TOKEN=$(openssl rand -hex 32)      # set before starting the gateway; at least 32 characters
curl -H "Authorization: Bearer $ADMIN_TOKEN" http://localhost:8080/admin/plans
```

| Request | Response |
|---|---|
| No `Authorization` header, or not `Bearer <token>` | `401` `unauthorized`, with `WWW-Authenticate: Bearer realm="admin"` |
| Wrong token (including an API key) | `403` `forbidden` |
| `ADMIN_TOKEN` unset | Endpoints aren't served, and the gateway logs a warning at startup |
| `ADMIN_TOKEN` shorter than 32 characters | The gateway doesn't start |

- **Auth comes first:** it's checked before routing, so unauthenticated callers can't discover which endpoints exist.
- **Comparison:** the token is checked in constant time, by comparing SHA-256 digests.
- **Earlier paths:** `/analytics/*` from Phase 5 is still served, with the same authentication, so a wrong token now gets `403` there too.

## Conventions

- **JSON everywhere.** Every response, including errors, 404s and 405s, is `application/json` with `Cache-Control: no-store`.
- **Request bodies** must be `Content-Type: application/json`, a single object, and at most 64 KiB. Unknown fields are rejected.
- **Query strings** accept only the parameters listed for each endpoint. Anything else, such as a typo, is rejected.
- **IDs** in paths must be positive integers.

### Errors

```json
{
  "error": "request validation failed",
  "code": "validation_failed",
  "fields": {"email": "must be a plain email address, e.g. dev@example.com", "application": "required"},
  "request_id": "814fe70cee8f0d8a5d0897e1029c9f51"
}
```

| Status | `code` | When |
|---|---|---|
| 400 | `validation_failed` | A field or query parameter is invalid. `fields` says which, and why. |
| 400 | `invalid_json` | Malformed JSON, unknown field, empty body, or more than one JSON value |
| 401 | `unauthorized` | Missing admin token |
| 403 | `forbidden` | Wrong admin token |
| 404 | `not_found` | Unknown endpoint, or no record with that ID |
| 405 | `method_not_allowed` | The `Allow` header lists the valid methods |
| 413 | `body_too_large` | Body over 64 KiB |
| 415 | `unsupported_media_type` | Body isn't `application/json` |
| 500 | `internal_error` | Details are logged with the `request_id`, never returned |

## API keys

### `POST /admin/api-keys`

```sh
curl -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"email":"dev@example.com","application":"Web App","plan":"pro"}' \
  http://localhost:8080/admin/api-keys
```

```json
{
  "api_key": "gw_ujgHDcSWxI47kjkKl3b7nl2lo5egnvaEATLH5Bep08A",
  "note": "Store this key now; it cannot be retrieved again.",
  "key": {"id": 1, "status": "active", "application_id": 1, "application": "Web App",
          "owner_email": "dev@example.com", "plan": "pro", "created_at": "…", "revoked_at": null}
}
```

Returns `201`. The key works at the gateway immediately. Behaves like `apikey create`:

- **User and application:** if they don't exist yet, they're created.
- **Plan:** only applies to a new application, and defaults to `free`.
- **Showing the key:** `api_key` appears only in this response. It's never stored, listed or logged.

| Field | Rule |
|---|---|
| `email` | Required. A plain address, without a display name. At most 254 characters. |
| `application` | Required. 1–100 characters, no control characters. |
| `plan` | Optional, default `free`. Must be an existing plan name. |

### `GET /admin/api-keys`

`?status=active|revoked`, `?application_id=N`, `?limit=1..1000` (default 100) and `?after=<id>` for pagination.

```json
{"api_keys": [{"id": 1, "status": "active", "application_id": 1, "…": "…"}], "next_after": null}
```

Keys are ordered by ID. When a page is full, `next_after` holds the ID to pass as `?after=` for the next page, and on the last page it is `null`. Key values and hashes are never included.

### `POST /admin/api-keys/{id}/revoke`

Returns the key with `"status": "revoked"`. The next request made with that key gets `403` at the gateway. Revoking an already-revoked key succeeds and changes nothing. An unknown ID returns `404`.

## Plans

### `GET /admin/plans`

```json
{"plans": [{"id": 1, "name": "free", "requests_per_minute": 100, "applications": 3, "created_at": "…"},
           {"id": 2, "name": "pro",  "requests_per_minute": 1000, "applications": 1, "created_at": "…"}]}
```

### `PATCH /admin/plans/{id}`

```sh
curl -X PATCH -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"requests_per_minute": 250}' http://localhost:8080/admin/plans/1
```

`requests_per_minute` is required and must be a whole number from 1 to 1,000,000.

- **Takes effect immediately:** the limit is read with the API key on every request, so the change applies to each key's next request, on every gateway instance, with no restart.
- **Only the limit is editable:** plan names can't be changed.

## Routes

`gateway.yaml` decides **which** routes exist. The database decides **how** each one behaves: its upstream and cache TTL. See ADR-005.

- **At startup,** the gateway syncs the `routes` table with `gateway.yaml`. New prefixes are added using their YAML values, and prefixes no longer in the YAML are deleted.
- **Existing routes keep their stored settings,** so edits made through this API survive restarts. If a stored value differs from `gateway.yaml`, the gateway logs a warning at startup and uses the stored value.

### `GET /admin/routes`

```json
{"routes": [{"id": 1, "prefix": "/albums", "upstream": "http://localhost:8082",
             "cache_ttl": "30s", "cache_ttl_ms": 30000, "created_at": "…", "updated_at": "…"}]}
```

### `PATCH /admin/routes/{id}`

```sh
curl -X PATCH -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"upstream": "http://users-v2:8081", "cache_ttl": "5m"}' http://localhost:8080/admin/routes/2
```

| Field | Rule |
|---|---|
| `upstream` | An absolute `http`/`https` URL with a host. No query, fragment, or `user:pass@` credentials. |
| `cache_ttl` | A Go duration, `0s` to `24h`, in whole milliseconds. `"0s"` turns caching off. |

- **What's required:** at least one field. The prefix can't be changed.
- **Takes effect on this instance immediately:** the gateway serving the request swaps in the new route settings, and its next request uses them.
- **Other instances:** they pick up the change when they restart. Plan limits, by contrast, apply everywhere at once.
- **Cached responses:** anything already cached for the route stays until its TTL ends.

## Analytics

`GET /admin/analytics/summary`, `/admin/analytics/routes` and `/admin/analytics/accounts`, each with `?window=` (default `1h`, maximum `744h`). The responses are described in [analytics.md](analytics.md).

## Audit log

Every change, and every refused authentication, writes one log line with `"audit": true`:

```json
{"level":"INFO","msg":"admin action","audit":true,"action":"plan.update","actor":"admin:3eb1bd43",
 "request_id":"c1c0…","remote_addr":"10.0.0.7:45558","plan_id":1,"plan":"free",
 "requests_per_minute_before":100,"requests_per_minute_after":250}
```

| `action` | Extra fields |
|---|---|
| `api_key.create` | `api_key_id`, `application_id`, `plan` |
| `api_key.revoke` | `api_key_id`, `application_id`, `previous_status` |
| `plan.update` | `plan_id`, `plan`, `requests_per_minute_before`, `requests_per_minute_after` |
| `route.update` | `route_id`, `prefix`, `upstream_before`, `upstream_after`, `cache_ttl_before`, `cache_ttl_after` |
| *(denied)* `msg: "admin access denied"` | `reason`: `missing_credential` or `invalid_credential`, plus `method`, `path` |

- **`actor`:** `admin:` followed by the first 8 hex characters of the token's SHA-256. It tells tokens apart across rotations without revealing them.
- **Never logged:** raw API keys and the admin token.
- **Finding changes:** filter the logs on `audit=true`. The `request_id` ties each change to its access-log line.

## Tests

| What | Where |
|---|---|
| 401 vs 403, `WWW-Authenticate`, token never logged, denials audited | `internal/admin/auth_test.go` |
| Auth on every endpoint, JSON 404 and 405 | `tests/admin_test.go` `TestAdminAuthentication` |
| Create, list, filter, page and revoke keys; validation errors; keys work or are refused at the gateway immediately; audit lines | `TestAdminManagesAPIKeys` |
| Plan limit change applies to the next request; validation; audit lines | `TestAdminEditsPlanLimits` |
| Route upstream and cache TTL changes apply immediately and survive a re-sync; validation; audit lines | `TestAdminEditsRoutesLive` |
| Startup sync adds and removes prefixes and keeps stored settings | `TestSyncRoutesFollowsGatewayYAML` |
| Analytics through the admin API | `TestAdminAnalyticsSummary` |
