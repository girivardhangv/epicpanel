#!/usr/bin/env bash
# build-release.sh — build release binaries and (optionally) publish to GitHub Releases.
#
#   ./build-release.sh                 # build bin/ artifacts
#   ./build-release.sh upload v1.0.0   # build + gh release create v1.0.0
#
# Output names: <binary>-linux-<amd64|arm64>
set -Eeuo pipefail
# Never die silently (same discipline as install.sh): a silent abort after the
# build looks like success while nothing was uploaded.
trap 'echo "build-release.sh aborted at line $LINENO" >&2; exit 1' ERR
cd "$(dirname "$0")"

VERSION="${1:-}"
UPLOAD=0
if [ "$VERSION" = "upload" ]; then
  UPLOAD=1; VERSION="${2:?version required (upload vX.Y.Z)}"
elif [ "${2:-}" = "upload" ]; then
  # Makefile form: ./build-release.sh vX.Y.Z upload vX.Y.Z
  UPLOAD=1
fi

OUT=bin
mkdir -p "$OUT"

for arch in amd64 arm64; do
  GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.Version=${VERSION:-dev}" \
    -o "$OUT/epicpanel-api-linux-$arch" ./cmd/api
  GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.Version=${VERSION:-dev}" \
    -o "$OUT/epicpanel-agent-linux-$arch" ./cmd/agent
done

# Panel web UI bundle (frontend/dist) — served by the API from EPICPANEL_WEB_DIR.
if [ -f ../frontend/dist/index.html ]; then
  tar -czf "$OUT/web-dist.tar.gz" -C ../frontend/dist .
  echo "web bundle: $OUT/web-dist.tar.gz ($(du -h "$OUT/web-dist.tar.gz" | cut -f1))"
else
  echo "WARNING: ../frontend/dist missing — run 'cd frontend && npm run build' first"
fi

# Checksums manifest (curl|bash users can verify; installer does not enforce).
( cd "$OUT" && { sha256sum ./*-linux-*; [ -f web-dist.tar.gz ] && sha256sum ./web-dist.tar.gz; } > SHA256SUMS )

echo "built:"
ls -1 "$OUT"

if [ "$UPLOAD" = 1 ]; then
  command -v gh >/dev/null || { echo "gh not installed: install GitHub CLI"; exit 1; }

  # Ensure the tag exists or will be created by this command
  gh release view "$VERSION" >/dev/null 2>&1 || gh release create "$VERSION" --title "$VERSION" --notes "Release $VERSION"

  for f in "$OUT"/*linux-* "$OUT"/web-dist.tar.gz "$OUT"/SHA256SUMS; do
    [ -f "$f" ] || continue
    gh release upload "$VERSION" "$f" --clobber
  done
  echo "uploaded $VERSION — users get it via epicpanel-update"
fi
