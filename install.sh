#!/usr/bin/env bash
# install.sh - build Litegent into a single, statically-linked binary.
#
# Usage:
#   ./install.sh                         Build for the host OS/arch into ./bin/litegent
#   GOOS=linux GOARCH=arm64 ./install.sh Cross-compile for another platform
#   ./install.sh --install               Build, then install onto PATH
#   PREFIX=/opt/bin ./install.sh --install   Install to a custom prefix
#   SKIP_TESTS=1 ./install.sh            Skip the pre-build test/vet gate
#
# The resulting binary has CGO disabled and is built with -trimpath and
# a stripped symbol table (-s -w), producing a self-contained static
# executable with no runtime dependency on the Go toolchain or libc.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

BINARY_NAME="litegent"
OUT_DIR="${OUT_DIR:-${ROOT_DIR}/bin}"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go toolchain not found on PATH" >&2
  exit 1
fi

if [[ "${SKIP_TESTS:-0}" != "1" ]]; then
  echo "==> Running go vet ./..."
  go vet ./...

  echo "==> Running go test ./... (all packages must pass before shipping a binary)"
  go test ./...
else
  echo "==> SKIP_TESTS=1 set; skipping go vet/go test gate"
fi

mkdir -p "$OUT_DIR"

echo "==> Building ${BINARY_NAME} ${VERSION} for ${GOOS}/${GOARCH} (static, CGO disabled)"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build \
  -trimpath \
  -ldflags "-s -w -X litegent/internal/version.Version=${VERSION}" \
  -o "${OUT_DIR}/${BINARY_NAME}" \
  ./cmd/litegent

echo "==> Built: ${OUT_DIR}/${BINARY_NAME}"
if command -v file >/dev/null 2>&1; then
  file "${OUT_DIR}/${BINARY_NAME}"
fi

if [[ "${1:-}" == "--install" ]]; then
  PREFIX="${PREFIX:-/usr/local/bin}"
  echo "==> Installing to ${PREFIX}/${BINARY_NAME}"
  install -m 0755 "${OUT_DIR}/${BINARY_NAME}" "${PREFIX}/${BINARY_NAME}"
  echo "==> Installed. Run '${BINARY_NAME} --version' to verify."
else
  echo "==> Run with --install to also copy the binary onto your PATH (default: /usr/local/bin)."
fi
