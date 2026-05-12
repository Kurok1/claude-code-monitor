#!/usr/bin/env bash
# Build and start the monitor with capture enabled.
# Usage: ./scripts/run-capture.sh
#
# In another terminal: `source scripts/claude-env.sh && claude`
set -euo pipefail

cd "$(dirname "$0")/.."

CONFIG=${CONFIG:-config.capture.yaml}

mkdir -p data captured

if [ ! -f "$CONFIG" ]; then
    cat > "$CONFIG" <<'EOF'
server:
  grpc_listen: "127.0.0.1:4317"
storage:
  duckdb_path: "./data/monitor.duckdb"
capture:
  enabled: true
  dir: "./captured"
logging:
  level: "info"
  format: "text"
EOF
    echo "wrote default $CONFIG"
fi

echo "building bin/server ..."
GOPATH="$HOME/go" go build -o bin/server ./cmd/server

cat <<EOF

============================================================
monitor listening on 127.0.0.1:4317
config:    $CONFIG
captures:  ./captured/

next: open ANOTHER terminal and run

    source scripts/claude-env.sh
    claude

press Ctrl+C to stop the monitor.
============================================================

EOF

exec ./bin/server -config "$CONFIG"
