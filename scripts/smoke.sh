#!/usr/bin/env bash
# End-to-end smoke test: start the monitor with a temp config, send a fixed
# OTLP payload, stop, then query DuckDB to verify rows were persisted.
#
#   ./scripts/smoke.sh
#
# Exits 0 on success, 1 on any assertion failure.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v duckdb >/dev/null 2>&1; then
    echo "smoke: requires the 'duckdb' CLI on PATH" >&2
    exit 1
fi

TMP="$(mktemp -d -t cc-monitor-smoke.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

CONFIG="$TMP/config.yaml"
DBFILE="$TMP/monitor.duckdb"
LOG="$TMP/server.log"

# Random-ish port to avoid clashing with a running real server.
PORT=$((40000 + RANDOM % 5000))

cat > "$CONFIG" <<EOF
server:
  grpc_listen: "127.0.0.1:${PORT}"
storage:
  duckdb_path: "${DBFILE}"
ingest:
  batch_size: 4
  flush_interval: "500ms"
  buffer_hard_limit: 1000
capture:
  enabled: false
stats:
  listen: ""
logging:
  level: "info"
  format: "text"
EOF

echo "[smoke] building bin/server"
GOPATH="$HOME/go" go build -o bin/server ./cmd/server

echo "[smoke] starting server on :$PORT"
./bin/server -config "$CONFIG" > "$LOG" 2>&1 &
SERVER_PID=$!
trap 'kill -TERM "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT

# Wait for gRPC listen line
for _ in $(seq 1 50); do
    if grep -q "grpc server listening" "$LOG" 2>/dev/null; then break; fi
    sleep 0.1
done
if ! grep -q "grpc server listening" "$LOG"; then
    echo "[smoke] server did not start within 5s" >&2
    cat "$LOG" >&2
    exit 1
fi

echo "[smoke] sending synthetic OTLP payload"
GOPATH="$HOME/go" go run ./scripts/smoke -endpoint "127.0.0.1:${PORT}"

echo "[smoke] waiting flush_interval + buffer"
sleep 2

echo "[smoke] graceful stop"
kill -TERM "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true

echo "[smoke] querying duckdb"
QUERY=$(cat <<'SQL'
SELECT name || '=' || cnt
FROM (
    SELECT 'session_count' AS name, COUNT(*) AS cnt FROM metric_session_count
    UNION ALL SELECT 'token_input',  COUNT(*) FROM metric_token_usage WHERE type = 'input'
    UNION ALL SELECT 'token_output', COUNT(*) FROM metric_token_usage WHERE type = 'output'
    UNION ALL SELECT 'cost_usage',   COUNT(*) FROM metric_cost_usage
    UNION ALL SELECT 'user_prompt',  COUNT(*) FROM event_user_prompt
    UNION ALL SELECT 'api_request',  COUNT(*) FROM event_api_request
    UNION ALL SELECT 'tool_result',  COUNT(*) FROM event_tool_result
) ORDER BY name;
SQL
)
RESULTS=$(duckdb "$DBFILE" -noheader -list -c "$QUERY")
echo "$RESULTS"

FAIL=0
while IFS= read -r line; do
    cnt=${line#*=}
    if [ "$cnt" -lt 1 ]; then
        echo "[smoke] FAIL: $line (expected >= 1)" >&2
        FAIL=1
    fi
done <<< "$RESULTS"

if [ "$FAIL" -ne 0 ]; then
    echo "[smoke] last 30 server log lines:" >&2
    tail -n 30 "$LOG" >&2
    exit 1
fi

echo "[smoke] PASS"
