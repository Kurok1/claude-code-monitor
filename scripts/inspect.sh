#!/usr/bin/env bash
# Decode captured OTLP .pb files into a human-readable form.
# Wraps `go run ./scripts/inspect-capture` and handles GOPATH override.
#
# Examples:
#   ./scripts/inspect.sh captured/metrics/<file>.pb
#   ./scripts/inspect.sh captured/logs/
#   ./scripts/inspect.sh -aggregate captured/
#   ./scripts/inspect.sh -format json captured/metrics/<file>.pb
set -euo pipefail

cd "$(dirname "$0")/.."
exec env GOPATH="$HOME/go" go run ./scripts/inspect-capture "$@"
