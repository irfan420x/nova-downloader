#!/bin/sh
# build.sh — build NOVA Downloader for all supported platforms.
# Output goes to dist/. Requires Go 1.27+.
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
VERSION="${VERSION:-1.2.0}"
build() {
  GOOS="$1" GOARCH="$2" SUFFIX="$3"
  OUT="dist/nova-downloader-$VERSION-$SUFFIX"
  echo "building $OUT ..."
  GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o "$OUT" ./cmd/nova-downloader
}
build linux amd64 linux-amd64
build linux arm64 linux-arm64
build windows amd64 windows-amd64.exe
build darwin arm64 macos-arm64
build darwin amd64 macos-amd64
ls -la dist/
