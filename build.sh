#!/usr/bin/env bash
set -euo pipefail

APP=chainsync
OUT=dist
mkdir -p "$OUT"
export CGO_ENABLED=0

build() {
  local os="$1" arch="$2" suffix="$3" goarm="${4:-}"
  echo "==> ${os}/${arch}${goarm:+ (GOARM=$goarm)}"
  if [ -n "$goarm" ]; then
    GOARM="$goarm" GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w" -o "$OUT/${APP}-${suffix}" .
  else
    GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w" -o "$OUT/${APP}-${suffix}" .
  fi
}

build windows amd64 "windows-amd64.exe"
build linux   amd64 "linux-amd64"
build linux   arm64 "linux-arm64"
build linux   arm    "linux-armv7" 7

echo "完成，产物在 $OUT/："
ls -lh "$OUT"
