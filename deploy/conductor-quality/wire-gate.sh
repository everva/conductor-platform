#!/usr/bin/env bash
# Idempotently inject the 3 deterministic quality checks (i18n-render, honesty,
# fabricated-data) into a recipe's gate-verify.sh, right after the i18n:parity step.
# Safe to re-run: does nothing if already wired.
#   Usage: wire-gate.sh <gate-verify.sh path> <base-branch>
set -euo pipefail
GV="$1"; BASE="$2"
grep -q fabrication-data-check "$GV" && { echo "  already wired: $GV"; exit 0; }
python3 - "$GV" "$BASE" <<'PY'
import sys
gv, base = sys.argv[1], sys.argv[2]
lines = open(gv).read().splitlines()
inject = [
    f'echo "verify: [HARD] i18n render (no hardcoded strings)"; bash "$HOME/conductor-agent/lib/i18n-render-check.sh" "{base}" src',
    f'echo "verify: [HARD] honesty (no fabricated/reverse money)"; bash "$HOME/conductor-agent/lib/honesty-grep.sh" "{base}" src',
    f'echo "verify: [HARD] fabricated-data (no Math.random / seeded metrics)"; bash "$HOME/conductor-agent/lib/fabrication-data-check.sh" "{base}" src',
]
out, done = [], False
for ln in lines:
    out.append(ln)
    if not done and "i18n:check" in ln:
        out.extend(inject); done = True
if not done:  # no i18n:check anchor — append before a trailing "verify: PASS" or at end
    idx = next((i for i, l in enumerate(out) if "verify: PASS" in l), len(out))
    out[idx:idx] = inject
open(gv, "w").write("\n".join(out) + "\n")
print(f"  wired 3 checks into {gv} (base {base})")
PY
