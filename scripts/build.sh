#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
npm ci --prefix web --no-audit --no-fund
npm run build --prefix web
go test -race ./...
go vet ./...
mkdir -p bin
CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o bin/backupd ./cmd/backupd
