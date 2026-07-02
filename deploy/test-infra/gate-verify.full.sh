#!/usr/bin/env bash
# Conductor verify gate for optiway — FULL harness: build + diff-lint (existing) PLUS a
# DB-backed test phase that runs against the shared everva test-infra (pg/redis/emqx/
# rabbitmq/temporal). Drop-in replacement for ~/conductor-agent/recipe/gate-verify.sh.
#
# Runs in the task worktree. Env (DATABASE_URL/REDIS_URL/... → localhost) comes from
# ~/conductor-agent/test-infra.env. The infra MUST be reachable on localhost:<port>:
#   - everva: native (compose publishes 0.0.0.0:<port>)
#   - davinci: via the oinfra-* systemd socket forwards (localhost:2xxxx → everva)
# localhost is REQUIRED: optiway's db:seed:e2e refuses any non-local DATABASE_URL host.
#
# App ports (api/web) are chosen DYNAMICALLY (free ports) so the gate never collides with
# whatever else the host runs on :3000/:3010 (davinci runs its own optiway stack there).
# The test phase is gated on CONDUCTOR_RUN_TESTS=1 so the build-only gate still works if
# the infra is down.
set -uo pipefail

ENV_FILE="${CONDUCTOR_TESTINFRA_ENV:-$HOME/conductor-agent/test-infra.env}"
freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
fail() { echo "verify: FAIL — $*" >&2; exit 1; }

# ─── Phase 1: build + diff-scoped lint (the existing, proven gate) ────────────────────
set -e
corepack disable >/dev/null 2>&1 || true
pnpm install --no-frozen-lockfile

# Pick conflict-free app ports BEFORE the web build (NEXT_PUBLIC_API_URL is baked at build).
API_PORT="${CONDUCTOR_API_PORT:-$(freeport)}"
WEB_PORT="${CONDUCTOR_WEB_PORT:-$(freeport)}"
export NEXT_PUBLIC_API_URL="http://localhost:${API_PORT}"
echo "verify: app ports → api:${API_PORT} web:${WEB_PORT}"
pnpm turbo run build --filter=@optiway/web...

BASE="conductor/optiway"
REF="$(git rev-parse --verify -q "$BASE" || git rev-parse --verify -q "origin/$BASE" || true)"
if [ -n "$REF" ]; then
  for entry in "apps/web|../../packages/eslint-config|" "packages/shared|../eslint-config|--max-warnings 0"; do
    dir="${entry%%|*}"; rest="${entry#*|}"; cfg="${rest%%|*}"; flags="${rest#*|}"
    files="$(git diff --name-only --diff-filter=ACMR "$REF...HEAD" -- "$dir" | grep -E "\.(ts|tsx)$" | sed "s|^$dir/||" || true)"
    if [ -n "$files" ]; then
      echo "verify: diff-scoped lint in $dir:"; echo "$files" | sed "s/^/  - /"
      ( cd "$dir" && pnpm exec eslint --resolve-plugins-relative-to "$cfg" $flags $files )
    fi
  done
fi
echo "verify: build + diff-scoped lint OK"
set +e

# ─── Phase 2: DB-backed tests against the everva test-infra ───────────────────────────
if [ ! -f "$ENV_FILE" ]; then echo "verify: no test-infra env ($ENV_FILE) — build-only gate"; exit 0; fi
set -a; . "$ENV_FILE"; set +a
if [ "${CONDUCTOR_RUN_TESTS:-0}" != "1" ]; then echo "verify: CONDUCTOR_RUN_TESTS!=1 — skipping test phase (build-only gate)"; exit 0; fi

# Reachability preflight (fail fast + clear, never fake-green).
pg_host_port="${DATABASE_URL#*@}"; pg_host_port="${pg_host_port%%/*}"
nc -z -w3 "${pg_host_port%%:*}" "${pg_host_port##*:}" 2>/dev/null || fail "infra DB $pg_host_port unreachable from $(hostname)"

echo "verify: prisma migrate deploy (idempotent)..."
pnpm --filter @optiway/db exec prisma migrate deploy --schema=prisma/schema.prisma || fail "prisma migrate deploy"

echo "verify: db:seed:e2e..."
pnpm --filter @optiway/db db:seed:e2e || fail "db:seed:e2e"

# Boot api + web on the chosen free ports; always tear them down.
API_PID=""; WEB_PID=""
teardown() { [ -n "$WEB_PID" ] && kill "$WEB_PID" 2>/dev/null; [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null; }
trap teardown EXIT

echo "verify: building api..."
pnpm --filter @optiway/api build || fail "api build"
# NODE_ENV=test ONLY on the api process (build already done) — mounts the _test/* routes
# (db/reset etc.) the e2e helpers need + skips the auth throttle for sequential specs.
# IMPORTANT: optiway api reads API_PORT (NOT PORT) and has a kill-port routine — binding a
# free dynamic API_PORT makes it bind free AND its lsof-kill finds nothing (never touches
# the host's other services on :3000).
( cd apps/api && NODE_ENV=test API_PORT="${API_PORT}" PORT="${API_PORT}" node dist/main >/tmp/gate-api.log 2>&1 ) & API_PID=$!

# Next 16 web uses output:standalone — `next start` mis-serves static assets. Run the
# standalone server, copying static + public into it (standard standalone deploy step).
echo "verify: starting web (standalone) on ${WEB_PORT}..."
SA="apps/web/.next/standalone"
if [ -d "$SA/apps/web" ]; then
  cp -r apps/web/.next/static "$SA/apps/web/.next/static" 2>/dev/null || true
  [ -d apps/web/public ] && cp -r apps/web/public "$SA/apps/web/public" 2>/dev/null || true
  ( cd "$SA/apps/web" && PORT="${WEB_PORT}" HOSTNAME=127.0.0.1 node server.js >/tmp/gate-web.log 2>&1 ) & WEB_PID=$!
else
  ( cd apps/web && PORT="${WEB_PORT}" pnpm exec next start -p "${WEB_PORT}" >/tmp/gate-web.log 2>&1 ) & WEB_PID=$!
fi

wait_tcp() { local hp="$1" n=0; until nc -z -w2 "${hp%%:*}" "${hp##*:}" 2>/dev/null; do n=$((n+1)); [ "$n" -ge 90 ] && return 1; sleep 2; done; }
echo "verify: waiting for api :${API_PORT}..."
wait_tcp "localhost:${API_PORT}" || { tail -25 /tmp/gate-api.log; fail "api :${API_PORT} did not open"; }
# A real login proves the api serves SEEDED data from the infra (not just that a port is open).
code="$(curl -fsS -m5 -o /dev/null -w "%{http_code}" -X POST "http://localhost:${API_PORT}/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d '{"email":"'"${E2E_ADMIN_EMAIL}"'","password":"'"${E2E_ADMIN_PASSWORD}"'"}' 2>/dev/null)"
echo "$code" | grep -qE '^(200|201)' || { tail -25 /tmp/gate-api.log; fail "api login (infra) HTTP $code — not serving seeded data"; }
wait_tcp "localhost:${WEB_PORT}" || { tail -15 /tmp/gate-web.log; fail "web :${WEB_PORT} did not come up"; }

# optiway's e2e suite is NOT CI-maintained (playwright.config: "manuel çalıştırma odaklı")
# and carries broken/drifted specs (dangling imports, factory-vs-schema drift). So the gate
# runs a CURATED smoke proven to pass against the infra — not the whole suite. Expand via
# CONDUCTOR_E2E_ARGS as specs are repaired.
E2E_ARGS="${CONDUCTOR_E2E_ARGS:-tests/e2e/01-template-setup/S1.2-employee-bulk-upload.spec.ts}"

# Generate a gate playwright config that points baseURL at the chosen web port (the base
# config hardcodes :3010). global-setup + helpers read the api URL from NEXT_PUBLIC_API_URL.
cat > apps/web/playwright.gate.config.ts <<TS
import base from './playwright.config';
import { defineConfig } from '@playwright/test';
export default defineConfig({ ...base, use: { ...base.use, baseURL: 'http://localhost:${WEB_PORT}' } });
TS

echo "verify: playwright e2e against the infra-backed app ($E2E_ARGS)..."
( cd apps/web && pnpm exec playwright install chromium >/dev/null 2>&1 || true; \
  NEXT_PUBLIC_API_URL="http://localhost:${API_PORT}" pnpm exec playwright test --config=playwright.gate.config.ts $E2E_ARGS ) \
  || fail "playwright e2e"

echo "verify: FULL gate (build + lint + db-backed tests + e2e) OK"
