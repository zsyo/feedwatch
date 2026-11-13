#!/bin/sh
# Build the SPA into internal/webui/embedded so `go:embed all:embedded` picks it
# up. Docker's Node stage calls the same script with OUT_DIR=/web/out and then
# COPY --from=assets that directory into the Go build stage.
#
# Never empty the whole output directory: stub.html and .gitkeep are versioned
# placeholders that must survive a local build.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT/internal/webui/embedded"}

# Remove only generated files; never touch the versioned placeholders.
rm -rf "$OUT_DIR/assets"
rm -f "$OUT_DIR/index.html" "$OUT_DIR/BUILD_ID"

cd "$ROOT/web"
npm ci
npm run build -- --outDir "$OUT_DIR"

# BUILD_ID is exposed by /api/meta so an operator can tell which frontend build
# is baked into the running binary. git may be absent in the Docker context.
BUILD_ID="$(date -u +%Y%m%dT%H%M%SZ)-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
printf '%s\n' "$BUILD_ID" > "$OUT_DIR/BUILD_ID"
echo "webui assets built into $OUT_DIR ($BUILD_ID)"
