#!/bin/sh
# Build filedrop for a Linux server from any machine with Go.
# Defaults to x86-64; for ARM: GOARCH=arm64 scripts/build-linux.sh
#
# CGO_ENABLED=0 gives a single self-contained binary: the SQLite driver
# (modernc.org/sqlite) is pure Go, so nothing from the server's system
# libraries is needed.
#
# Output: dist/filedrop
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
GOARCH=${GOARCH:-amd64}
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -trimpath -o dist/filedrop ./cmd/filedrop
echo "Built dist/filedrop (linux/$GOARCH)"
