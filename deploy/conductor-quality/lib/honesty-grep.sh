#!/usr/bin/env bash
# DETERMINISTIC honesty gate (shared). Complements the LLM fabrication-audit with a zero-false-negative
# grep for the fabrication classes the LLM/diff review keeps missing: hardcoded tax rates, fabricated /
# reverse-computed money. Scans the FULL content of every changed src file (not just added lines), so a
# task that TOUCHES a violating file must clean it. HARD — exit 1 on any hit.
#   Usage: honesty-grep.sh <base_branch> [src_path]
set -uo pipefail
BASE="${1:-develop}"; SRC="${2:-src}"
git fetch -q origin "$BASE" >/dev/null 2>&1 || true
FILES=$(git diff --name-only "origin/${BASE}...HEAD" -- "$SRC" 2>/dev/null | grep -E '\.(ts|tsx)$' || true)
[ -z "$FILES" ] && FILES=$(git diff --name-only HEAD~1 -- "$SRC" 2>/dev/null | grep -E '\.(ts|tsx)$' || true)
# Exclude test/spec/mock files: a test computing expected tax from a rate (e.g. `subtotal * 0.19` to build
# an expected value) is legitimate test math, not production fabrication. The gate's purpose is honest
# PRODUCTION money/tax. Only production src is scanned.
FILES=$(printf '%s\n' "$FILES" | grep -vE '(\.test\.|\.spec\.|__tests__/|__mocks__/|/test/|/tests/|/e2e/|/mock/|/mocks/|demo-data|\.fixture)' || true)
[ -z "$FILES" ] && { echo "honesty: no changed src files (excl tests/mock)"; exit 0; }

# Fabricated / reverse-computed money + hardcoded tax rates + hardcoded CURRENCY AMOUNTS. Curated for
# high precision. Classes the audit found: PRINT_TAX_RATE=0.19, subtotal*0.19, total/1.19 (reverse money),
# and a literal currency amount in source (shipping-ops "€4.50 same-hour, €2.40 standard") — a fabricated
# price. Real money MUST come from an endpoint; a `€|₺` immediately followed by a digit is a hardcoded
# amount (template forms `€{amount}` / `€ {x}` have no digit after the symbol, so they pass).
PAT='(TAX_RATE|VAT_RATE|KDV_RATE|VATRATE)[[:space:]]*[:=][[:space:]]*0\.[0-9]|(subtotal|total|amount|price|grand|net|gross)[A-Za-z]*[[:space:]]*\*[[:space:]]*0\.[0-9]|\*[[:space:]]*0\.(19|18|20)\b|/[[:space:]]*1\.(18|19|20)\b|(€|₺)[[:space:]]?[0-9]'
HITS=""
for f in $FILES; do
  [ -f "$f" ] || continue
  # Exclude comment lines: a doc-comment illustrating a money FORMAT (`* European money €462.312,99`,
  # `// Free for a €0 plan`) is documentation, not a fabricated rendered value. Drop pure-comment lines.
  m=$(grep -nEi "$PAT" "$f" 2>/dev/null | grep -vE '^[0-9]+:[[:space:]]*(//|\*|/\*|\*/)' || true)
  [ -n "$m" ] && HITS+="  $f"$'\n'"$(printf '%s\n' "$m" | sed 's/^/    /')"$'\n'
done
if [ -n "$HITS" ]; then
  echo "honesty: FAIL — fabricated / reverse-computed money or hardcoded tax rate:"
  printf '%s' "$HITS"
  echo "  Fix: take money/tax from the real endpoint (e.g. /tax-breakdown); render '—' when absent."
  echo "  NEVER hardcode a rate, reverse-compute (subtotal*0.19, total/1.19), or a literal price (€4.50)."
  exit 1
fi
echo "honesty: PASS"
