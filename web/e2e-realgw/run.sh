#!/usr/bin/env bash
# Real-gateway cockpit e2e harness (M3) — the "real DB, real data" answer to the user's
# strict rule: the cockpit is driven against a REAL, freshly-built conductor-api (NOT the
# page.route mocks the hermetic CI suite uses), seeded over its real REST API, so the
# board's status truth is verified end-to-end through the gateway the live davinci host uses.
#
# WHICH DB / WHICH DATA (answering "hangi veritabanına bağlanıyor hangi veriyle"):
#   * DB:   an EPHEMERAL in-memory store (gateway booted with no -dsn) — never PROD, never a
#           real Postgres, never the live conductor/optiway data. Nothing here can touch prod.
#   * DATA: deterministic + seeded HERE via real REST calls — onboard project everva/e2e-demo
#           → intake e2e-realgw/seed.yaml (task E2E-RUN) → lease it (flips status→running, M1).
#
# It is OPT-IN (npm run e2e:realgw), NOT part of CI: CI's web job is intentionally hermetic
# (no gateway/PG/claude). This mirrors the CP_REAL_CLAUDE / electron-smoke opt-in pattern.
#
# Usage:  cd web && npm run e2e:realgw      (or: bash e2e-realgw/run.sh)
set -euo pipefail

WEB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$WEB_DIR/.." && pwd)"
TMP_DIR="$WEB_DIR/.tmp-realgw"
GW_PORT=8899
WEB_PORT=4273
GW_URL="http://localhost:${GW_PORT}"
# 16+ char non-placeholder token (gateway refuses shorter / the template placeholder).
TOKEN="e2e-realgw-local-token-$(printf '%04d' $((RANDOM % 10000)))"

mkdir -p "$TMP_DIR"
GW_PID=""
WEB_PID=""

cleanup() {
  [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true
  [ -n "$WEB_PID" ] && kill "$WEB_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

wait_for() { # url, label
  for _ in $(seq 1 60); do
    if curl -fsS "$1" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  echo "timed out waiting for $2 ($1)" >&2
  return 1
}

echo "==> [1/5] building conductor-api (real gateway binary)"
( cd "$REPO_DIR" && go build -o "$TMP_DIR/conductor-api" ./cmd/conductor-api )

echo "==> [2/5] booting gateway (ephemeral memory store, :${GW_PORT})"
CONDUCTOR_API_TOKEN="$TOKEN" CONDUCTOR_API_ADDR=":${GW_PORT}" "$TMP_DIR/conductor-api" >"$TMP_DIR/gateway.log" 2>&1 &
GW_PID=$!
wait_for "${GW_URL}/healthz" "gateway"

echo "==> [3/5] seeding real data over REST (onboard → intake → lease)"
auth=(-H "Authorization: Bearer ${TOKEN}")
curl -fsS "${auth[@]}" -X POST "${GW_URL}/projects" \
  -H 'Content-Type: application/json' \
  -d '{"repo":"everva/e2e-demo","base_branch":"develop"}' >/dev/null
# Intake the seed scenario YAML (text/yaml body) → creates task E2E-RUN.
curl -fsS "${auth[@]}" -X POST "${GW_URL}/projects/e2e-demo/intake" \
  --data-binary @"$WEB_DIR/e2e-realgw/seed.yaml" >/dev/null
# Lease it → the gateway flips the task to running (M1) and records the host on the lease.
curl -fsS "${auth[@]}" -X POST "${GW_URL}/projects/e2e-demo/agent/lease" \
  -H 'Content-Type: application/json' \
  -d '{"host_id":"e2e-host","capabilities":["backend"]}' >/dev/null
# Fail fast + loudly if the seed didn't take (never a false-green run).
if ! curl -fsS "${auth[@]}" "${GW_URL}/projects/e2e-demo/tasks" | grep -q '"status":"running"'; then
  echo "seed FAILED: E2E-RUN is not running after lease — see $TMP_DIR/gateway.log" >&2
  exit 1
fi
echo "    seeded: everva/e2e-demo · task E2E-RUN leased+running"

echo "==> [4/5] building + serving web cockpit against the real gateway (:${WEB_PORT})"
# Build SAME-ORIGIN (empty VITE_API_BASE → ApiClient base "") and let `vite preview`'s proxy
# (VITE_API_TARGET) forward the gateway paths — so the browser is CORS-free and the gateway
# (which 403s cross-origin WS) sees a same-origin handshake. Mirrors the prod single-origin ingress.
( cd "$WEB_DIR" && VITE_API_BASE="" npm run build >"$TMP_DIR/web-build.log" 2>&1 )
( cd "$WEB_DIR" && VITE_API_TARGET="$GW_URL" npm run preview -- --port "$WEB_PORT" --strictPort >"$TMP_DIR/web-preview.log" 2>&1 ) &
WEB_PID=$!
wait_for "http://localhost:${WEB_PORT}" "web preview"

echo "==> [5/5] running Playwright against the real gateway"
cd "$WEB_DIR"
REALGW_TOKEN="$TOKEN" REALGW_WEB_PORT="$WEB_PORT" \
  npx playwright test -c playwright.realgw.config.ts "$@"
