#!/bin/sh
# Build filedrop for the server (Linux, x86-64) from any machine with Go.
#
# CGO_ENABLED=0 gives a single self-contained binary: the SQLite driver
# (modernc.org/sqlite) is pure Go, so nothing from the server's system
# libraries is needed.
#
# Output: dist/filedrop
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/filedrop ./cmd/filedrop
echo "Built dist/filedrop (linux/amd64)"
