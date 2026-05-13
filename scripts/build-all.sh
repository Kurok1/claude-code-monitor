#!/usr/bin/env bash
# Build frontend (Vite) and embed into the Go server binary.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> building frontend"
(cd frontend && npm install --no-audit --no-fund && npm run build)

echo "==> building server binary"
mkdir -p bin
go build -trimpath -o bin/server ./cmd/server

echo "==> done: bin/server"
ls -lh bin/server
