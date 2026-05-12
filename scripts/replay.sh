#!/usr/bin/env bash
# Replay captured OTLP .pb files against a running monitor server.
#
# Examples:
#   ./scripts/replay.sh captured/
#   ./scripts/replay.sh -endpoint 127.0.0.1:4317 captured/
set -euo pipefail

cd "$(dirname "$0")/.."
exec env GOPATH="$HOME/go" go run ./scripts/replay-captured "$@"
