#!/usr/bin/env bash
# build-release.sh — build release binaries and (optionally) upload to R2.
#
#   ./build-release.sh                 # build bin/ artifacts
#   ./build-release.sh upload v1.0.0   # build + wrangler r2 put to epicpanel-releases
#
# Output names: <binary>-linux-<amd64|arm64>
set -Eeuo pipefail
cd "$(dirname "$0")"

VERSION="${1:-}"
UPLOAD=0
if [ "$VERSION" = "upload" ]; then
  UPLOAD=1; VERSION="${2:?version required (upload vX.Y.Z)}"
fi

OUT=bin
mkdir -p "$OUT"

for arch in amd64 arm64; do
  GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.Version=${VERSION:-dev}" \
    -o "$OUT/epicpanel-api-linux-$arch" ./cmd/api
  GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.Version=${VERSION:-dev}" \
    -o "$OUT/epicpanel-agent-linux-$arch" ./cmd/agent
done

# Checksums manifest (curl|bash users can verify; installer does not enforce).
( cd "$OUT" && sha256sum ./*-linux-* > SHA256SUMS )

echo "built:"
ls -1 "$OUT"

if [ "$UPLOAD" = 1 ]; then
  command -v wrangler >/dev/null || { echo "wrangler not installed: npm i -g wrangler"; exit 1; }
  for f in "$OUT"/*linux-*; do
    base="$(basename "$f")"
    wrangler r2 object put "epicpanel-releases/releases/$VERSION/$base" --file "$f"
  done
  tmp="$(mktemp)"; printf '{"version":"%s"}\n' "$VERSION" >"$tmp"
  wrangler r2 object put "epicpanel-releases/releases/latest.json" --file "$tmp"
  rm -f "$tmp"
  echo "uploaded $VERSION + latest.json — users get it via epicpanel-update"
fi
