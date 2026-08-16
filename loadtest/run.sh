#!/usr/bin/env bash
# Builds confezy and runs each k6 workload against its own fresh database.
#
#   ./loadtest/run.sh [output-dir]
#
# Every workload gets a freshly seeded database and a restarted server. That
# isolation matters: the write workload inserts hundreds of thousands of
# configs, and reusing its database for the next run would measure a dataset no
# real environment has.
#
# The server's request log goes to a file, so these numbers include the cost of
# logging every request.

set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${1:-loadtest/results}"
PORT="${PORT:-8299}"
VUS="${VUS:-50}"
DURATION="${DURATION:-20s}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="loadtest-password"

mkdir -p "$OUT"
BIN="$OUT/confezy"
DB="$OUT/loadtest.db"
COOKIES="$OUT/cookies.txt"

SERVER_PID=""
READ_KEY=""
WRITE_KEY=""

stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
}
trap stop_server EXIT

start_fresh_server() { # $1: log suffix
  stop_server
  rm -f "$DB" "$DB-wal" "$DB-shm" "$COOKIES"

  CONFEZY_ADMIN_USERNAME=admin \
  CONFEZY_ADMIN_PASSWORD="$ADMIN_PASS" \
  CONFEZY_SEED_DATA=1 \
    "$BIN" serve -port "$PORT" -db "$DB" > "$OUT/server-$1.log" 2>&1 &
  SERVER_PID=$!

  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null; then break; fi
    sleep 0.25
  done
  curl -fsS -o /dev/null "$BASE/healthz"

  curl -fsS -c "$COOKIES" -o /dev/null -d "username=admin&password=$ADMIN_PASS" "$BASE/ui/login"
  READ_KEY="$(mint read)"
  WRITE_KEY="$(mint write)"
  [ -n "$READ_KEY" ] && [ -n "$WRITE_KEY" ] || { echo "could not mint keys" >&2; exit 1; }
}

mint() { # $1: scope -> plaintext key
  curl -fsS -b "$COOKIES" -H 'HX-Request: true' -d "scope=$1&label=k6" \
    "$BASE/ui/p/checkout/prod/keys" | grep -oE "ff_$1_prod_[a-z0-9]+" | head -1
}

run_k6() { # $1: scenario, $2: tag for output files
  k6 run \
    -e "BASE_URL=$BASE" -e "READ_KEY=$READ_KEY" -e "WRITE_KEY=$WRITE_KEY" \
    -e "SCENARIO=$1" -e "VUS=$VUS" -e "DURATION=$DURATION" \
    --summary-export "$OUT/$2.json" \
    --quiet \
    loadtest/confezy.js > "$OUT/$2.txt" 2>&1 || true
  grep -E "http_req_duration|http_reqs|http_req_failed|checks_succ" "$OUT/$2.txt" || true
}

preload_configs() { # $1: how many configs to add
  seq 1 "$1" | xargs -P 8 -I{} curl -fsS -o /dev/null -X POST \
    -H "X-App-Key: $WRITE_KEY" -H 'Content-Type: application/json' \
    -d '{"key":"preload_{}","value":{"n":{},"note":"payload scaling"}}' \
    "$BASE/v1/manage/configs"
}

echo "==> building"
CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BIN" .

echo "==> ${VUS} VUs, ${DURATION} per workload, fresh database each time"
for scenario in poll snapshot flags write mixed; do
  echo
  echo "--- $scenario ---"
  start_fresh_server "$scenario"
  run_k6 "$scenario" "$scenario"
  if [ "$scenario" = "write" ]; then
    echo "database after write workload: $(du -h "$DB" | cut -f1)"
  fi
done

# How snapshot cost scales with the size of the document, which is the number
# that decides whether polling stays cheap as an environment grows.
for n in 100 1000; do
  echo
  echo "--- snapshot with ~$n configs ---"
  start_fresh_server "scale$n"
  preload_configs "$n"
  SIZE=$(curl -fsS -o /dev/null -w '%{size_download}' -H "X-App-Key: $READ_KEY" "$BASE/v1/snapshot")
  echo "snapshot payload: ${SIZE} bytes"
  run_k6 snapshot "scale$n"
done

echo
echo "==> results in $OUT"
