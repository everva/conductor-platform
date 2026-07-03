#!/usr/bin/env bash
# DETERMINISTIC fabricated-DATA gate (shared). honesty-grep catches fabricated MONEY
# (tax/€/₺). This catches the OTHER fabrication class the audit found that regex-money
# missed: RANDOM data in render + seeded metric LITERALS fed to live UI
# (A-30 `{cat:"Moda", gmv:542140}` drill overlay, Math.random counts). High-signal,
# low-FP: only production .tsx/.ts, excludes test/mock/fixture/types. HARD — exit 1.
#   Usage: fabrication-data-check.sh <base_branch> [src_path]
set -uo pipefail
BASE="${1:-develop}"; SRC="${2:-src}"
git fetch -q origin "$BASE" >/dev/null 2>&1 || true
FILES=$(git diff --name-only "origin/${BASE}...HEAD" -- "$SRC" 2>/dev/null | grep -E '\.tsx?$' || true)
[ -z "$FILES" ] && FILES=$(git diff --name-only HEAD~1 -- "$SRC" 2>/dev/null | grep -E '\.tsx?$' || true)
# Exclude tests/mocks/fixtures AND the type-contract / mock-data layer (legit literals live there).
FILES=$(printf '%s\n' "$FILES" | grep -vE '(\.test\.|\.spec\.|__tests__/|__mocks__/|/test/|/tests/|/e2e/|/mock/|/mocks/|demo-data|\.fixture|/types/|\.d\.ts$)' || true)
[ -z "$FILES" ] && { echo "fabrication-data: no changed src (excl tests/mock/types)"; exit 0; }

# (1) Math.random in a render/component file = fabricated live data (near-zero FP).
PAT_RANDOM='Math\.random\('
# (2) Seeded METRIC literal: a metric-looking key with a hardcoded multi-digit number,
#     i.e. a fake business figure baked into a component (A-30 gmv:542140). Small
#     defaults (count: 10) are allowed by requiring >=4 digits.
PAT_METRIC='(gmv|revenue|sales|turnover|volume|traffic|views|impressions|clicks|conversions?|growth|churn|mrr|arr|ltv|gross|earnings|payout|balance)[A-Za-z]*[[:space:]]*:[[:space:]]*[0-9]{4,}'
FAIL=0
for f in $FILES; do
  [ -f "$f" ] || continue
  # strip pure-comment lines
  body=$(grep -nvE '^[[:space:]]*(//|\*|/\*|\*/)' "$f" 2>/dev/null || true)
  r=$(printf '%s\n' "$body" | grep -nE "$PAT_RANDOM" | grep -vE 'crypto|uuid|nanoid|test|seed\(' | head -3 || true)
  m=$(printf '%s\n' "$body" | grep -nEi "$PAT_METRIC" | head -3 || true)
  if [ -n "$r" ] || [ -n "$m" ]; then
    echo "fabrication-data: FAIL $f"
    [ -n "$r" ] && printf '%s\n' "$r" | sed 's/^/    [random] /'
    [ -n "$m" ] && printf '%s\n' "$m" | sed 's/^/    [seeded-metric] /'
    FAIL=1
  fi
done
if [ $FAIL -eq 1 ]; then
  echo "  Fix: NEVER fabricate live data. Math.random / hardcoded metric → real endpoint field or '—'."
  echo "  Seeded demo literals belong in src/lib/mock or fixtures, NEVER in a rendered component."
  exit 1
fi
echo "fabrication-data: PASS"
