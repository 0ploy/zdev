#!/usr/bin/env bash
# Filesystem write benchmark: bind mount (= no Mutagen) vs named volume (= Mutagen runtime).
# Isolates pure FS cost: a warm pnpm store (populated once, no network during timed runs)
# is copied into node_modules on each target filesystem. package-import-method=copy makes
# both scenarios do identical work except for the destination filesystem.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)/bench"
APP="$ROOT/app"
IMAGE="node:22-slim"
CTX="$(docker context show)"
RUNS=2

echo "================================================================"
echo " FS benchmark  |  docker context: $CTX  |  $(docker info --format '{{.OperatingSystem}} / Server {{.ServerVersion}}')"
echo "================================================================"

# --- fresh app source with a realistic dependency tree ---------------------
rm -rf "$ROOT"; mkdir -p "$APP"
cat > "$APP/package.json" <<'JSON'
{
  "name": "fsbench", "version": "1.0.0", "private": true,
  "dependencies": {
    "react": "^18.3.1", "react-dom": "^18.3.1", "react-router-dom": "^6.26.0",
    "axios": "^1.7.0", "zod": "^3.23.0", "date-fns": "^3.6.0", "lodash": "^4.17.21"
  },
  "devDependencies": {
    "vite": "^5.4.0", "@vitejs/plugin-react": "^4.3.0", "typescript": "^5.5.0",
    "eslint": "^9.9.0", "prettier": "^3.3.0", "vitest": "^2.0.0",
    "@testing-library/react": "^16.0.0", "tailwindcss": "^3.4.0",
    "postcss": "^8.4.0", "autoprefixer": "^10.4.0",
    "@types/react": "^18.3.0", "@types/react-dom": "^18.3.0", "@types/lodash": "^4.17.0"
  }
}
JSON
cat > "$APP/.npmrc" <<'NPMRC'
store-dir=/store
package-import-method=copy
NPMRC

# --- clean up any prior run's docker objects -------------------------------
docker volume rm bench_store bench_vol >/dev/null 2>&1 || true
docker volume create bench_store >/dev/null
docker pull -q "$IMAGE" >/dev/null

PNPM='corepack enable >/dev/null 2>&1 && corepack prepare pnpm@9.7.0 --activate >/dev/null 2>&1'

# --- warm the store (network happens here, ONCE, untimed) ------------------
echo; echo "[warm] populating shared pnpm store + generating lockfile (network, untimed)..."
docker run --rm -v "$APP":/app -v bench_store:/store -w /app "$IMAGE" \
  bash -c "$PNPM && pnpm install --silent && du -sh node_modules && find node_modules | wc -l && rm -rf node_modules" \
  | sed 's/^/[warm] /'

run_scenario () {  # $1 = label, $2 = mount-spec for /app
  local label="$1" mount="$2"
  echo; echo "----- scenario: $label -----"
  for i in $(seq 1 "$RUNS"); do
    docker run --rm $mount -v bench_store:/store -w /app "$IMAGE" bash -c "
      $PNPM
      rm -rf node_modules
      S=\$(node -e 'console.log(Date.now())')
      pnpm install --offline --prefer-offline --silent; rc=\$?
      E=\$(node -e 'console.log(Date.now())')
      N=\$(find node_modules -mindepth 1 2>/dev/null | wc -l)
      if [ \$rc -ne 0 ] || [ \$N -lt 1000 ]; then echo \"RESULT $label run$i FAILED rc=\$rc files=\$N\"; else echo \"RESULT $label run$i ms=\$((E-S)) files=\$N\"; fi
    " 2>&1 | grep -E 'RESULT|ERR_' || true
  done
}

# BIND: node_modules written to a host bind mount (crosses the VM boundary) = NO MUTAGEN
run_scenario "bind-mount " "-v $APP:/app"

# VOLUME: node_modules written to a native-ext4 named volume = MUTAGEN runtime
docker volume create bench_vol >/dev/null
docker run --rm -v "$APP":/src -v bench_vol:/app "$IMAGE" bash -c 'cp /src/package.json /src/.npmrc /src/pnpm-lock.yaml /app/' >/dev/null
run_scenario "named-vol  " "-v bench_vol:/app"

echo; echo "[cleanup] removing benchmark volumes"
docker volume rm bench_store bench_vol >/dev/null 2>&1 || true
echo "DONE ($CTX)"
