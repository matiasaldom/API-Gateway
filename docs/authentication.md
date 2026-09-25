# Authentication

Every request through the gateway, except `GET /health`, needs an API key:

```
Authorization: Bearer <api-key>
```

Keys look like `gw_` followed by 43 base64url characters (256 random bits). The gateway stores only the key's SHA-256 hash. The raw key is shown once, when it is created, and is never stored or logged.

## Setup

The gateway and the `apikey` tool both read the database from `DATABASE_URL`:

```sh
export DATABASE_URL="postgres://gateway:secret@localhost:5432/gateway?sslmode=disable"
```

Connection pool settings go in the URL's query string, for example `&pool_max_conns=20`.

Apply the migrations once, using either tool:

```sh
psql "$DATABASE_URL" -f migrations/000001_identity.up.sql
# or, with golang-migrate:
migrate -path migrations -database "$DATABASE_URL" up
```

If the database can't be reached within 5 seconds, the gateway exits at startup with status 1 and logs `database unavailable`.

## Managing keys

Use `apikey` for all key management. Run it with `go run ./cmd/apikey`, or build it with `go build ./cmd/apikey`.

### Create a key

```sh
go run ./cmd/apikey create -email dev@example.com -app "My App"
```

```
Created API key 1 for application "My App" (id 1, owner dev@example.com).
Copy it now; it cannot be shown again:

gw_3q2-7wJb9kTzYHc0VxLr8sPpN4mEaUdKfGiO1hBnXyQ
```

- The key is always on the last line of the output, so a script can take it with `| tail -1`.
- If the user (matched by email, ignoring case) or the application (matched by owner and name) doesn't exist yet, it is created.
- `-plan NAME` sets the plan for a new application. It defaults to `free`. An existing application keeps its current plan.
- Running `create` again for the same application adds another key. This is how you rotate keys: create the new key, move your clients to it, then revoke the old one.

### List keys

```sh
go run ./cmd/apikey list
```

```
ID  STATUS   APP_ID  APPLICATION  OWNER            PLAN  CREATED               REVOKED
1   active   1       My App       dev@example.com  free  2026-09-25T01:59:17Z  -
2   revoked  1       My App       dev@example.com  free  2026-09-25T01:59:17Z  2026-09-25T01:59:17Z
```

The list never includes key values or hashes. Use the `ID` column to refer to a key.

### Revoke a key

```sh
go run ./cmd/apikey revoke -id 2
```

Revocation takes effect on the next request, because keys aren't cached. Revoking a key that is already revoked does nothing.

## Calling the gateway

```sh
curl -i "http://localhost:8080/users/42?expand=albums" \
  -H "Authorization: Bearer gw_3q2-7wJb9kTzYHc0VxLr8sPpN4mEaUdKfGiO1hBnXyQ"
```

The scheme name `Bearer` is case-insensitive. The request must contain exactly one space after it and exactly one `Authorization` header.

For an authenticated request, the upstream receives:

- the original method, path, query string, body and headers, **except `Authorization`**, which the gateway removes so backends never see gateway keys;
- `X-Request-ID`, plus `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto`.

Inside the gateway, the key's metadata is in the request context:

```go
key, ok := auth.FromContext(r.Context())
// key.ID, key.ApplicationID, key.UserID, key.PlanID, key.Status
```

## Error responses

Every error is JSON with the same shape. Its `request_id` matches the `X-Request-ID` response header and the gateway's log lines:

```json
{"error":"invalid api key","request_id":"4fb5e388ceef04df48efdb637823ffae"}
```

| Situation | Status | `error` | `WWW-Authenticate` |
|---|---|---|---|
| No `Authorization` header | `401` | `missing api key` | `Bearer realm="api-gateway"` |
| Wrong scheme, malformed key, or more than one `Authorization` header | `401` | `invalid api key` | `Bearer realm="api-gateway", error="invalid_token"` |
| Well-formed key that doesn't exist | `401` | `invalid api key` | `Bearer realm="api-gateway", error="invalid_token"` |
| Revoked key | `403` | `api key revoked` | — |
| Valid key, but no route matches the path | `404` | `no route for path` | — |
| Database unreachable during key lookup (lookups time out after 3s) | `503` | `authentication unavailable` | — |

Authentication runs before routing. A request without a valid key gets `401` even for a path that has no route.

## Logging

Each rejected request produces one `WARN` line. The line includes the request ID and a `reason`, and never the key:

```json
{"level":"WARN","msg":"authentication failed","request_id":"7929a1d1…","method":"GET","path":"/users/42","remote_addr":"[::1]:53854","reason":"revoked_key","api_key_id":2}
```

| `reason` | Meaning |
|---|---|
| `missing_credential` | No `Authorization` header |
| `malformed_credential` | Header present, but the value isn't `Bearer <well-formed key>` |
| `unknown_key` | Well-formed key with no matching hash |
| `revoked_key` | Key exists but is revoked; `api_key_id` is included |

A successful authentication is logged at `DEBUG` as `authenticated`, with `api_key_id`, `application_id` and `plan_id`. Keys are identified in logs only by their database ID.

## Tests

The unit tests need no database. The PostgreSQL integration tests run only when `TEST_DATABASE_URL` is set. Each test creates its own schema and drops it afterwards, so any database you can create schemas in will work:

```sh
TEST_DATABASE_URL="postgres://postgres@localhost:5432/postgres?sslmode=disable" go test ./...
```

| What is verified | Where |
|---|---|
| Missing key → 401, invalid key → 401, revoked key → 403, valid key → forwarded | `tests/auth_test.go` `TestAuthenticationFlow` |
| Only SHA-256 hashes are stored, checked against Postgres's own `sha256()` | `TestOnlyKeyHashesAreStored` |
| Raw keys never appear in logs | `TestAuthenticationFlow/raw_keys_never_appear_in_logs`, `internal/auth` unit tests |
| Key metadata is in the request context | `TestAPIKeyMetadataInRequestContext` |
| `apikey` create, list and revoke | `cmd/apikey/main_test.go` |
