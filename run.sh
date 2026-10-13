#!/usr/bin/env bash
# run.sh - build Litegent and run it in place (dev convenience wrapper).
#
# Usage: ./run.sh [litegent flags...]
#
# Always runs `go build` first (Go's own build cache makes this fast
# when nothing changed, so there's no need to hand-roll a staleness
# check) and execs the resulting binary with any arguments forwarded.
#
# For a release-quality, statically-linked binary with the full
# vet/test gate, use ./install.sh instead.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

BINARY="${ROOT_DIR}/bin/litegent"
mkdir -p "${ROOT_DIR}/bin"

go build -o "$BINARY" ./cmd/litegent

exec "$BINARY" "$@"
