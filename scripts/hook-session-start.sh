#!/usr/bin/env bash
# Claude Code SessionStart / SessionResume hook entry.
#
# Idempotent: starts the monitor in the background if no instance is currently
# listening on the configured gRPC port. The server itself does a preflight
# check, so repeated hook firings are no-ops (~14ms each).
#
# Designed to be called with no arguments. Repo root is resolved from the
# script's own path so it works regardless of the hook's cwd.
#
# Optional overrides:
#   MONITOR_CONFIG  Path to config.yaml (default: <repo>/config.yaml)
#   MONITOR_LOG     Log file (default: /tmp/claude-code-monitor.log)
set -uo pipefail

REPO="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$REPO/bin/server"
CONFIG="${MONITOR_CONFIG:-$REPO/config.yaml}"
LOG="${MONITOR_LOG:-/tmp/claude-code-monitor.log}"

# Self-bootstrap: build the binary if it's missing (fresh clone / pulled new
# code). Silently no-op when go is unavailable so the hook never breaks the
# session.
if [ ! -x "$BIN" ]; then
    if command -v go >/dev/null 2>&1; then
        (cd "$REPO" && GOPATH="$HOME/go" go build -o bin/server ./cmd/server) || exit 0
    else
        exit 0
    fi
fi

# Seed config from the example if absent (still no-op if neither exists).
if [ ! -f "$CONFIG" ] && [ -f "$REPO/config.example.yaml" ]; then
    cp "$REPO/config.example.yaml" "$CONFIG"
fi
if [ ! -f "$CONFIG" ]; then
    exit 0
fi

# Spawn detached. The server's own preflight (alreadyListening) short-circuits
# if something is already bound on grpc_listen, so we don't probe here.
nohup "$BIN" -config "$CONFIG" >> "$LOG" 2>&1 &
disown
exit 0
