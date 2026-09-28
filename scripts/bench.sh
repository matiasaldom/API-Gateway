#!/usr/bin/env bash
# Load-tests the gateway in the docker compose stack and prints markdown tables
# (the source of docs/benchmarks.md). Needs only bash, curl, and Docker.
#
#   docker compose up -d --build
#   scripts/bench.sh | tee bench-results.md
#   DURATION=30s scripts/bench.sh            longer runs (default 10s each)
#
# The load generator (oha) runs in a container on the compose network, so every
# comparison goes through the same Docker networking. It shares the machine
# with the gateway; see docs/benchmarks.md for what that means for the numbers.
set -euo pipefail
export MSYS_NO_PATHCONV=1 # Git Bash on Windows: don't rewrite /paths in arguments

DURATION=${DURATION:-10s}
KEY=gw_benchmark_only_local_docker_key_0000000000A # seeded "benchmark" plan, 1e9/min
GW=http://gateway:8080
DIRECT=http://users:8081
compose() { docker compose --progress quiet "$@"; }

gateway_id=$(compose ps -q gateway)
postgres_id=$(compose ps -q postgres)
[ -n "$gateway_id" ] || { echo "start the stack first: docker compose up -d --build" >&2; exit 1; }
compose build bench >/dev/null

# jq program turning oha's JSON into a table row: rps, p50, p95, p99 (ms), non-2xx, errors.
# "aborted due to deadline" is oha cancelling in-flight requests when the run ends.
row='def ms: (. * 100000 | round) / 100;
  [ (.summary.requestsPerSec | round),
    (.latencyPercentiles.p50 | ms), (.latencyPercentiles.p95 | ms), (.latencyPercentiles.p99 | ms),
    ([.statusCodeDistribution | to_entries[] | select(.key | startswith("2") | not) | .value] | add // 0),
    ([.errorDistribution | to_entries[] | select(.key != "aborted due to deadline") | .value] | add // 0)
  ] | map(tostring) | join(" | ")'

# bench LABEL CONCURRENCY URL [oha args...] prints one table row.
bench() {
  local label=$1 c=$2 url=$3
  shift 3
  local stats json
  stats=$(mktemp)
  json=$(mktemp)
  # Sample CPU and memory of the gateway and PostgreSQL while the load runs.
  (while :; do docker stats --no-stream --format '{{.ID}} {{.CPUPerc}} {{.MemUsage}}' "$gateway_id" "$postgres_id" >>"$stats" 2>/dev/null; done) &
  local sampler=$!
  compose run --rm -T bench oha -z "$DURATION" -c "$c" --no-tui --output-format json \
    -H "Authorization: Bearer $KEY" "$@" "$url" >"$json"
  kill "$sampler" 2>/dev/null
  wait "$sampler" 2>/dev/null || true

  local result usage
  result=$(compose run --rm -T bench jq -r "$row" <"$json")
  # Peak CPU (100% = one core) of each container, and the gateway's peak memory in MiB.
  usage=$(awk -v gw="${gateway_id:0:12}" '
    $1 == gw { c = $2 + 0; if (c > gwc) gwc = c
               m = $3 + 0; if ($3 ~ /GiB/) m *= 1024; if ($3 ~ /KiB/) m /= 1024; if (m > gwm) gwm = m; next }
             { c = $2 + 0; if (c > pgc) pgc = c }
    END { if (NR == 0) print "n/a | n/a | n/a"; else printf "%.0f%% | %.0f MiB | %.0f%%", gwc, gwm, pgc }' "$stats")
  rm -f "$stats" "$json"
  echo "| $label | $c | $result | $usage |"
}

header() {
  echo
  echo "### $1"
  echo
  echo "| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |"
  echo "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
}

metric() { curl -s "${GATEWAY_URL:-http://localhost:8080}/metrics" | awk -v m="$1" '$1 == m { print $2 }'; }

echo "## Results ($(date -u +%Y-%m-%d), ${DURATION} per run)"
echo
echo "- Docker: $(docker info --format '{{.OperatingSystem}}, {{.NCPU}} CPUs, {{.MemTotal}} bytes memory')"
echo "- Load generator: $(compose run --rm -T bench oha --version)"
echo "- Rate-limit algorithm: $(grep -o 'rate_limit_algorithm: [a-z_]*' docker/gateway.yaml | cut -d' ' -f2)"
echo "- CPU and memory are the peak of samples taken during each run; 100% CPU = one core."
written_before=$(metric analytics_events_written)
dropped_before=$(metric analytics_events_dropped)

header "1. Concurrency sweep (small JSON, ~180 B)"
for c in 1 10 100 500; do
  bench "Direct to backend" "$c" "$DIRECT/users/1"
  bench "Gateway, no cache" "$c" "$GW/users/1"
  bench "Gateway, cache hit" "$c" "$GW/albums/1"
done

header "2. Payload size (concurrency 50)"
for size in 100 10000 500000; do
  bench "Direct, ${size} B" 50 "$DIRECT/users/1?size=$size"
  bench "Gateway no cache, ${size} B" 50 "$GW/users/1?size=$size"
  bench "Gateway cache hit, ${size} B" 50 "$GW/albums/1?size=$size"
done

header "3. Slow backend: 20 ms per response (concurrency 50)"
bench "Direct to backend" 50 "$DIRECT/users/1?delay=20ms"
bench "Gateway, no cache" 50 "$GW/users/1?delay=20ms"
bench "Gateway, cache hit" 50 "$GW/albums/1?delay=20ms"

header "4. Writes: POST with a 1 KB JSON body (concurrency 50)"
body=$(printf '{"data":"%s"}' "$(head -c 1000 /dev/zero | tr '\0' 'x')")
bench "Direct to backend" 50 "$DIRECT/users" -m POST -T application/json -d "$body"
bench "Gateway" 50 "$GW/users" -m POST -T application/json -d "$body"

sleep 3 # let the analytics worker write its last batch
echo
echo "### Analytics under load"
echo
echo "Analytics events stored: $(( $(metric analytics_events_written) - written_before ))." \
  "Dropped because the buffer was full: $(( $(metric analytics_events_dropped) - dropped_before )) (by design; see ADR-004)."
