#!/usr/bin/env bash
# Walks through the gateway's features against the docker compose stack, one
# step at a time, for a screen recording. Start the stack first:
#
#   docker compose up -d --build
#   scripts/demo.sh              press Enter between steps
#   NO_PAUSE=1 scripts/demo.sh   run straight through (CI uses this as an end-to-end test)
#
# Every response is checked, so the script exits non-zero if the gateway misbehaves.
set -euo pipefail

BASE=${GATEWAY_URL:-http://localhost:8080}
ADMIN=${ADMIN_TOKEN:-local-demo-admin-token-do-not-use-in-production}
FREE=gw_demo_free_plan_local_docker_key_0000000000A
PRO=gw_demo_pro_plan_local_docker_key_00000000000A
REVOKED=gw_demo_revoked_local_docker_key_000000000000A

step()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
note()  { printf '\033[2m%s\033[0m\n' "$*"; }
fail()  { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
pause() { [ -n "${NO_PAUSE:-}" ] || read -r -p $'\033[2m[Enter]\033[0m ' _; }

# show STATUS CURL_ARGS... prints the curl command, runs it with response headers
# visible, and fails unless the response status is STATUS ("-" skips the check).
last=""
show() {
  local want=$1 cmd="" a
  shift
  for a in "$@"; do case $a in *[[:space:]\"{]*) cmd+=" '$a'" ;; *) cmd+=" $a" ;; esac; done
  printf '\033[33m$ curl%s\033[0m\n' "$cmd"
  last=$(curl -s -i "$@" | tr -d '\r')
  printf '%s\n\n' "$last"
  local got
  got=$(head -1 <<<"$last" | cut -d' ' -f2)
  [ "$want" = - ] || [ "$got" = "$want" ] || fail "expected HTTP $want, got ${got:-no response}"
}
# expect HEADER fails unless the last response has that header line (e.g. "X-Cache: HIT").
expect() { grep -qi "^$1" <<<"$last" || fail "expected response header '$1'"; }
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

step "1. A request routed through the gateway"
note "Free-plan key -> /users -> users service. Note X-Request-Id and the rate-limit headers."
show 200 "$BASE/users/42?expand=albums" -H "Authorization: Bearer $FREE"
expect "X-Request-Id: "
pause

step "2. Rejected credentials"
note "Unknown key -> 401"
show 401 "$BASE/users/42" -H "Authorization: Bearer gw_not_a_real_key"
note "Revoked key -> 403"
show 403 "$BASE/users/42" -H "Authorization: Bearer $REVOKED"
pause

step "3. Exceeding the plan limit"
note "The free plan allows 100 requests per minute. Sending requests until one is refused..."
for i in $(seq 1 250); do
  [ "$(status "$BASE/users/42" -H "Authorization: Bearer $FREE")" = 429 ] && break
done
note "Request $i of that loop got 429: the key had used its 100 for this minute. One more:"
show 429 "$BASE/users/42" -H "Authorization: Bearer $FREE"
expect "Retry-After: "
pause

step "4. Response caching"
note "/albums caches GET responses for 30s, per API key. Same request twice: MISS, then HIT."
note "served_at is stamped by the backend, so an identical value means the backend was skipped."
path="/albums/$RANDOM" # a fresh path, so the first request is always a miss
show 200 "$BASE$path" -H "Authorization: Bearer $PRO"
expect "X-Cache: MISS"
show 200 "$BASE$path" -H "Authorization: Bearer $PRO"
expect "X-Cache: HIT"
note "A client can skip its cached copy with Cache-Control: no-cache -> BYPASS"
show 200 "$BASE$path" -H "Authorization: Bearer $PRO" -H "Cache-Control: no-cache"
expect "X-Cache: BYPASS"
pause

step "5. Dashboard"
note "Open http://localhost:3000 and sign in with the admin token:"
note "  $ADMIN"
note "Overview shows the traffic just generated: requests, errors, cache hit ratio, latency."
pause

step "6. Changing a policy live"
free_plan=$(curl -s "$BASE/admin/plans" -H "Authorization: Bearer $ADMIN" | grep -o '"id":[0-9]*,"name":"free"' | grep -o '[0-9]\+' | head -1)
[ -n "$free_plan" ] || fail "could not find the free plan through the admin API"
note "The free key is still blocked for this minute:"
show - "$BASE/users/42" -H "Authorization: Bearer $FREE"
note "Raise the free plan to 500/min through the admin API (no restart)..."
show 200 -X PATCH "$BASE/admin/plans/$free_plan" -H "Authorization: Bearer $ADMIN" \
  -H "Content-Type: application/json" -d '{"requests_per_minute": 500}'
note "...and the same key is let through on its very next request:"
show 200 "$BASE/users/42" -H "Authorization: Bearer $FREE"
expect "X-Ratelimit-Limit: 500"
pause

note "Restoring the free plan to 100/min."
curl -s -o /dev/null -X PATCH "$BASE/admin/plans/$free_plan" -H "Authorization: Bearer $ADMIN" \
  -H "Content-Type: application/json" -d '{"requests_per_minute": 100}'
step "Done"
