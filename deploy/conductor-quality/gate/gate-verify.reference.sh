#!/usr/bin/env bash
export PATH="/usr/bin:/bin:/usr/local/bin:/snap/bin:$HOME/.local/bin:$PATH"
export NODE_OPTIONS="--max-old-space-size=4096"
export NEXT_TELEMETRY_DISABLED=1
set -euo pipefail
command -v node >/dev/null || { echo "verify: FAIL node not found"; exit 127; }
echo "verify: node $(node -v) / npm $(npm -v)"
echo "verify: [1/5 HARD] npm ci";      npm ci --no-audit --no-fund --prefer-offline
echo "verify: [2/5 HARD] typecheck";   npm run typecheck
echo "verify: [3/5 HARD] i18n parity"; npm run i18n:check
echo "verify: [HARD] i18n render (no hardcoded strings)"; bash "$HOME/conductor-agent/lib/i18n-render-check.sh" "conductor/admin-redesign" src
echo "verify: [HARD] honesty (no fabricated/reverse money)"; bash "$HOME/conductor-agent/lib/honesty-grep.sh" "conductor/admin-redesign" src
echo "verify: [HARD] fabricated-data (no Math.random / seeded metrics)"; bash "$HOME/conductor-agent/lib/fabrication-data-check.sh" "conductor/admin-redesign" src
echo "verify: [advisory] lint";        npm run lint >/tmp/gate-lint.log 2>&1 && echo "  lint clean" || echo "  lint findings (advisory; baseline-red)"
echo "verify: [advisory] vitest";      npm test     >/tmp/gate-test.log 2>&1 && echo "  vitest green" || echo "  vitest failures (advisory; baseline has 4)"
echo "verify: [4/5 HARD] build";       npm run build
echo "verify: [audit] fabrication + dead-control audit (shared)"
bash "$HOME/conductor-agent/lib/fabrication-audit.sh" "conductor/admin-redesign" frontend || exit 1
echo "verify: PASS"
