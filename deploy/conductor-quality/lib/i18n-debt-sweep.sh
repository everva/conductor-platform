#!/usr/bin/env bash
# One-time-ish PANEL-WIDE i18n-debt sweep. The per-commit i18n-render gate only protects
# NEW lands; screens that landed BEFORE the gate carry hardcoded-string debt. This scans
# the WHOLE panel for i18n leaks, groups by screen, and files CAPPED + DEDUPED remediation
# fix-tasks (A-FIX-i18n-* → picked with remediation priority). Re-run to drain the debt a
# few screens at a time (no flood). Read-only scan; API-mediated filing.
#   Usage: i18n-debt-sweep.sh <project> <base> [--file]
set -uo pipefail
PROJECT="${1:?project}"; BASE="${2:?base}"; MODE="${3:-report}"
CLONE="$HOME/.conductor-agent/clones/$PROJECT"
STATE="$HOME/conductor-agent/i18n-debt-filed-$PROJECT.txt"
LOG="$HOME/conductor-agent/i18n-debt.log"
CAP=4                          # screens filed per run (drain gradually)
touch "$STATE"
cd "$CLONE" 2>/dev/null || { echo "no clone $CLONE"; exit 1; }
git fetch -q origin "$BASE" 2>/dev/null || true

# hardcoded user-facing copy (mirror of i18n-render-check.sh), excl url/email/Intl
PAT_A='\?[[:space:]]*"[A-ZĞÜŞİÖÇ][^"]+"[[:space:]]*:[[:space:]]*"[A-ZĞÜŞİÖÇ][^"]+"'
PAT_B='(placeholder|title|aria-label|label|alt)=("|\{")[A-ZĞÜŞİÖÇa-zğüşıöç][^"}{]{2,}'
PAT_C='"[^"]*[ğüşıöçİĞÜŞÖÇ][^"]*"|>[^<>{}]*[ğüşıöçİĞÜŞÖÇ][^<>{}]*<'
EXCL='import |from "|require\(|//|/\*| \* |className=|data-testid|console\.|z-index|aria-hidden|eslint|https?://|[A-Za-z0-9_.]+@|toLocaleString\(|Intl\.'

files=$(git ls-tree -r --name-only "origin/$BASE" -- src 2>/dev/null \
        | grep -E '\.tsx$' | grep -vE '(\.test\.|\.spec\.|__tests__/|__mocks__/|/test/|/mock/|/types/)')
declare -A screen_hits
for f in $files; do
  hit=$(git show "origin/$BASE:$f" 2>/dev/null | grep -nE "$PAT_A|$PAT_B|$PAT_C" | grep -vE "$EXCL" | head -1)
  [ -z "$hit" ] && continue
  scr=$(echo "$f" | sed -E 's|src/components/([^/]+)/.*|\1|; s|src/app/.*|app|; s|src/(.*)|\1|')
  screen_hits[$scr]="${screen_hits[$scr]:-} $f"
done

nscreen=${#screen_hits[@]}
echo "i18n-debt[$PROJECT]: $nscreen screen(s) with hardcoded-string debt"
echo "$(date -u +%FT%TZ) sweep: $nscreen leaking screens" >> "$LOG"
[ "$nscreen" = 0 ] && { echo "  clean — no i18n debt"; exit 0; }

# order screens, skip already-filed, take CAP fresh
fresh=()
for scr in $(printf '%s\n' "${!screen_hits[@]}" | sort); do
  grep -qxF "$scr" "$STATE" && continue
  fresh+=("$scr")
done
echo "  fresh (unfiled): ${#fresh[@]} → filing up to $CAP"
for scr in "${fresh[@]:0:8}"; do
  nf=$(echo "${screen_hits[$scr]}" | wc -w | tr -d ' ')
  echo "    $scr ($nf file(s))"
done

if [ "$MODE" != "--file" ]; then echo "  (report mode)"; exit 0; fi

# build intake YAML for CAP fresh screens
TOK=$(grep '^CONDUCTOR_AGENT_TOKEN=' "$HOME/conductor-agent/conductor-agent.env" | cut -d= -f2-)
tmp=$(mktemp); docs=0
for scr in "${fresh[@]:0:$CAP}"; do
  flist=$(echo "${screen_hits[$scr]}" | tr ' ' '\n' | grep . | head -8 | tr '\n' ' ')
  tid="A-FIX-i18n-$(echo "$scr" | tr '/.' '--' | tr -cd 'A-Za-z0-9-')"
  tid="${tid:0:58}"
  [ "$docs" -gt 0 ] && printf '\n---\n' >> "$tmp"
  cat >> "$tmp" <<YAML
id: $tid
title: "[i18n] backfill hardcoded strings — $scr"
lane: redesign
tier: T2
deps: []
acceptance:
  - "[i18n FIX] This ALREADY-LANDED screen carries hardcoded user-facing strings (pre-gate debt). Move EVERY label/placeholder/title/aria-label/option/JSX-text to next-intl t(...) with 4-locale parity (tr/en/mt/it). Keep the design 1:1; do not restyle."
  - "Leaking files (non-exhaustive): $flist"
  - "The verify gate HARD-FAILS on i18n-render (no hardcoded strings) + i18n:check parity — make both PASS. Any shared component rendered here must be i18n'd too."
hidden_holdout_ref: "store://holdouts/$PROJECT/$tid"
YAML
  docs=$((docs+1))
done
[ "$docs" = 0 ] && { echo "  nothing fresh to file"; rm -f "$tmp"; exit 0; }
code=$(curl -s -o /tmp/i18n-resp -w "%{http_code}" -X POST -H "Authorization: Bearer $TOK" \
  -H "Content-Type: application/x-yaml" --data-binary @"$tmp" \
  "http://conductor-api:8080/projects/$PROJECT/intake")
if [ "$code" = 200 ]; then
  created=$(python3 -c 'import json;print(",".join(json.load(open("/tmp/i18n-resp")).get("created",[])))' 2>/dev/null)
  echo "  FILED: $created"
  echo "$(date -u +%FT%TZ) FILED $created" >> "$LOG"
  for scr in "${fresh[@]:0:$CAP}"; do echo "$scr" >> "$STATE"; done
else
  echo "  intake HTTP $code: $(cat /tmp/i18n-resp | head -c 160)"
fi
rm -f "$tmp" /tmp/i18n-resp
