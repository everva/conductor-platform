#!/usr/bin/env bash
# E2E + VISUAL runner (davinci-side). Runs the panel's REAL Playwright suite against the
# real staging backend (real auth + real routing) in an isolated persistent checkout,
# FORCES a screenshot per test (visual verification artifact, kept permanently), and
# converts failures to intake fix-tasks (deduped + capped). No claude → no rate concern.
# Re-runnable manually anytime. Uses the project's own e2e (permanent, in git).
#   Usage: e2e-runner.sh <project> <base> [spec_glob] [--file]
set -uo pipefail
PROJECT="${1:?project}"; BASE="${2:?base}"; GLOB="${3:-e2e/smoke}"; MODE="${4:-report}"
CLONE=/home/davinci/.conductor-agent/clones/$PROJECT
E2E=/home/davinci/conductor-agent/e2e/$PROJECT
ENV=/home/davinci/conductor-agent/$PROJECT-e2e.env
ARTROOT=/home/davinci/conductor-agent/e2e-artifacts/$PROJECT
LOG=/home/davinci/conductor-agent/e2e-runner.log
LOCKHASH=/home/davinci/conductor-agent/e2e/$PROJECT.lockhash
TSFILE=/home/davinci/conductor-agent/e2e/$PROJECT.ts   # stamp dir name (no Date in-band)
ts() { date -u +%Y%m%dT%H%M%SZ; }
log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) e2e[$PROJECT]: $*" >> "$LOG"; }

[ -f "$ENV" ] || { log "no e2e env $ENV"; echo "no e2e env"; exit 1; }
cd "$CLONE"; git fetch -q origin "$BASE" 2>/dev/null || true

# fresh worktree at base tip (isolated from agent task worktrees)
git worktree remove --force "$E2E" 2>/dev/null; rm -rf "$E2E"; git worktree prune 2>/dev/null
mkdir -p "$(dirname "$E2E")"
git worktree add -q --detach "$E2E" "origin/$BASE" 2>/dev/null || { log "worktree add failed"; exit 1; }
cd "$E2E"

# deps: real npm ci each run (reliable — the STORE-symlink cache broke TS-config loading →
# playwright discovered 0 tests → false green. npm's own cache keeps this ~40s).
log "deps: npm ci --prefer-offline"
timeout 600 npm ci --no-audit --no-fund --prefer-offline >/dev/null 2>&1 || log "npm ci nonzero"
PW=./node_modules/.bin/playwright
[ -x "$PW" ] || { log "playwright bin missing"; cd "$CLONE"; git worktree remove --force "$E2E" 2>/dev/null; exit 1; }
timeout 300 "$PW" install chromium >/dev/null 2>&1 || true   # idempotent, cached

# If the project's playwright.config requires an auth storageState but ships NO auth.setup
# (e.g. xirigo-vendor: smoke specs assert the UNAUTHENTICATED /login redirect), provide an
# EMPTY session so playwright doesn't error on the missing file — unauthenticated is exactly
# what those specs assert. Harmless for projects that DO have a real auth.setup (it overwrites).
if grep -q 'e2e/.auth/user.json' playwright.config.ts 2>/dev/null && \
   ! ls e2e/setup/*.setup.ts >/dev/null 2>&1; then
  mkdir -p e2e/.auth
  [ -f e2e/.auth/user.json ] || echo '{"cookies":[],"origins":[]}' > e2e/.auth/user.json
  log "seeded EMPTY auth state (no auth.setup in repo — unauthenticated specs)"
fi

# force a screenshot per test (visual verification) via a wrapper config extending the project one
cat > playwright.audit.config.ts <<'CFG'
import base from "./playwright.config"
const cfg: any = { ...(base as any) }
cfg.use = { ...((base as any).use ?? {}), screenshot: "on", video: "off", trace: "retain-on-failure" }
cfg.retries = 1
export default cfg
CFG

set -a; source "$ENV"; set +a
export CI=1
STAMP=$(ts); ART="$ARTROOT/$STAMP"; mkdir -p "$ART"
log "run start (glob=$GLOB) → artifacts $ART"
timeout 2400 "$PW" test "$GLOB" --config=playwright.audit.config.ts \
  --reporter=json 2>/dev/null > "$ART/results.json" || true

# persist screenshots + report
find test-results -name '*.png' -exec cp -a {} "$ART/" \; 2>/dev/null || true
shots=$(find "$ART" -name '*.png' 2>/dev/null | wc -l)
log "artifacts: $shots screenshot(s) in $ART"

cd "$CLONE"; git worktree remove --force "$E2E" 2>/dev/null; git worktree prune 2>/dev/null

# process results → summary + (—file) fix-tasks
python3 /home/davinci/conductor-agent/lib/e2e-process.py "$PROJECT" "$ART/results.json" "$LOG" "$MODE" "$ART"
