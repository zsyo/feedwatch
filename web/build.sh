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

# ---- toolchain preflight ----------------------------------------------------
# A `command -v node` check is not enough: tool-version managers (mise, asdf)
# leave a shim on PATH even when the version it resolves to has been removed, and
# the shim only fails once it actually runs. Catch that here instead of letting
# it surface as "npm failed" halfway through the build.
if ! node -e '' || ! npm --version >/dev/null 2>&1; then
  {
    echo "error: node/npm on PATH is not runnable, so the SPA cannot be built."
    echo
    echo "  A version-manager shim (mise/asdf) stays on PATH after the version it"
    echo "  points at is gone, so it resolves but cannot execute. Install what the"
    echo "  config asks for, or bypass the shim by putting the real directory first:"
    echo "      mise install"
    echo "      export PATH=\"\$(cygpath -u \"\$(mise where node)\"):\$PATH\""
    echo "      make build-webui"
    echo
    echo "  node -> $(command -v node || echo '<not found>')"
    echo "  npm  -> $(command -v npm || echo '<not found>')"
  } >&2
  exit 1
fi

# ---- dependencies -----------------------------------------------------------
# `npm ci` deletes node_modules before reinstalling it. That costs more time than
# the build itself, and on Windows the deleted files pile up in the Recycle Bin,
# so it only runs when there is nothing left to preserve.
#
# The fingerprint covers both manifests plus the Node platform/ABI: this same
# directory is sometimes used from Windows and from WSL, and a node_modules
# installed by one cannot be loaded by the other, because the per-platform
# binary packages differ (@rollup/rollup-win32-x64-msvc vs. -linux-x64-gnu,
# @typescript/typescript-win32-x64 vs. -linux-x64).
#
#   FW_SKIP_DEPS=1   never install (the caller already prepared node_modules)
#   FW_FORCE_DEPS=1  always run a clean `npm ci` (wipes node_modules)
STAMP="$ROOT/web/node_modules/.cache/feedwatch-deps"

fingerprint() {
  node -e '
    const fs = require("fs"), crypto = require("crypto");
    const hash = crypto.createHash("sha256");
    for (const f of ["package.json", "package-lock.json"]) {
      hash.update(f + "\0");
      hash.update(fs.existsSync(f) ? fs.readFileSync(f) : "");
    }
    hash.update(process.platform + "-" + process.arch + "-" + process.versions.modules);
    process.stdout.write(hash.digest("hex"));
  '
}

install=""
if [ "${FW_SKIP_DEPS:-0}" = "1" ]; then
  echo "deps: skipped (FW_SKIP_DEPS=1)"
elif [ "${FW_FORCE_DEPS:-0}" = "1" ]; then
  install="ci"
elif [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$(fingerprint)" ]; then
  echo "deps: up to date, keeping node_modules"
elif [ -d node_modules ]; then
  # Incremental: adds and removes only what the manifests require, so the
  # existing tree survives instead of being rebuilt from scratch.
  install="install"
else
  install="ci"
fi

if [ "$install" = "ci" ]; then
  echo "deps: clean install, node_modules will be rewritten (npm ci)"
  npm ci --no-audit --no-fund
elif [ "$install" = "install" ]; then
  echo "deps: manifests changed, installing incrementally"
  npm install --no-audit --no-fund --prefer-offline
fi

if [ -n "$install" ]; then
  mkdir -p "$(dirname "$STAMP")"
  fingerprint > "$STAMP"
fi

npm run build -- --outDir "$OUT_DIR"

# BUILD_ID is exposed by /api/meta so an operator can tell which frontend build
# is baked into the running binary. Format: MMDDHHMM in the builder's local time
# plus the git short hash, e.g. "11142242-2f04086". Seconds are omitted on
# purpose: two builds made in the same minute are interchangeable to an
# operator. git may be absent in the Docker context (image builds run in UTC).
BUILD_ID="$(date +%m%d%H%M)-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
printf '%s\n' "$BUILD_ID" > "$OUT_DIR/BUILD_ID"
echo "webui assets built into $OUT_DIR ($BUILD_ID)"
