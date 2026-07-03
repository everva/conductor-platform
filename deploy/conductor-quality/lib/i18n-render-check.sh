#!/usr/bin/env bash
# DETERMINISTIC i18n RENDER gate (shared). The existing i18n:check only proves key PARITY; it does not
# catch a screen that renders HARDCODED strings (the audit's #1 recurring defect: A-04/15/17/25 landed
# with no useTranslations). This flags, in every changed .tsx, hardcoded user-facing copy — high-signal
# patterns + a "renders text but never imports a translation hook" heuristic. HARD — exit 1 on any hit.
#   Usage: i18n-render-check.sh <base_branch> [src_path]
set -uo pipefail
BASE="${1:-develop}"; SRC="${2:-src}"
git fetch -q origin "$BASE" >/dev/null 2>&1 || true
FILES=$(git diff --name-only "origin/${BASE}...HEAD" -- "$SRC" 2>/dev/null | grep -E '\.tsx$' || true)
[ -z "$FILES" ] && FILES=$(git diff --name-only HEAD~1 -- "$SRC" 2>/dev/null | grep -E '\.tsx$' || true)
# Exclude test/spec/mock files: their strings are FIXTURES and ASSERTIONS (e.g. mock `name: "Cilt Bakımı"`
# or `findByRole({name:"…"})`), NOT user-facing render surfaces. Flagging them is a false positive that
# would block a correct task or push the agent to weaken its tests. Only production .tsx renders copy.
FILES=$(printf '%s\n' "$FILES" | grep -vE '(\.test\.|\.spec\.|__tests__/|__mocks__/|/test/|/tests/|/e2e/)' || true)
[ -z "$FILES" ] && { echo "i18n-render: no changed tsx (excl tests)"; exit 0; }

# High-signal hardcoded-copy patterns:
#  (a) ternary UI strings:        ? "Kaydediliyor…" : "Kaydet"
#  (b) literal key attributes:    placeholder=|title=|aria-label=|label= "SomeText"
#  (c) Turkish-char string/text:  a literal or JSX text containing ğ ü ş ı ö ç İ Ğ Ü Ş Ö Ç
PAT_A='\?[[:space:]]*"[A-ZĞÜŞİÖÇ][^"]+"[[:space:]]*:[[:space:]]*"[A-ZĞÜŞİÖÇ][^"]+"'
PAT_B='(placeholder|title|aria-label|label|alt)=("|\{")[A-ZĞÜŞİÖÇa-zğüşıöç][^"}{]{2,}'
PAT_C='"[^"]*[ğüşıöçİĞÜŞÖÇ][^"]*"|>[^<>{}]*[ğüşıöçİĞÜŞÖÇ][^<>{}]*<'
FAIL=0
for f in $FILES; do
  [ -f "$f" ] || continue
  hits=$(grep -nE "$PAT_A|$PAT_B|$PAT_C" "$f" 2>/dev/null \
          | grep -vE 'import |from "|require\(|//|/\*| \* |className=|data-testid|console\.|z-index|aria-hidden|eslint|https?://|[A-Za-z0-9_.]+@|toLocaleString\(|Intl\.' \
          | head -8 || true)
  noi18n=""
  if grep -qE '</[A-Za-z]' "$f" \
     && grep -qE '>[[:space:]]*[A-ZĞÜŞİÖÇ][A-Za-zğüşıöçĞÜŞİÖÇ]+([[:space:]]+[A-Za-zğüşıöçĞÜŞİÖÇ]+)+[[:space:]]*<' "$f" \
     && ! grep -qE 'useTranslations|getTranslations|useFormatter' "$f"; then
     noi18n="renders multi-word text but imports no translation hook (useTranslations/getTranslations)"
  fi
  if [ -n "$hits" ] || [ -n "$noi18n" ]; then
    echo "i18n-render: FAIL $f"
    [ -n "$hits" ] && printf '%s\n' "$hits" | sed 's/^/    /'
    [ -n "$noi18n" ] && echo "    → $noi18n"
    FAIL=1
  fi
done
if [ $FAIL -eq 1 ]; then
  echo "  Fix: move ALL user-facing copy to next-intl t(...) with 4-locale key parity (tr/en/mt/it)."
  echo "  Also fix any un-i18n'd SHARED component this screen renders (don't reuse it hardcoded)."
  exit 1
fi
echo "i18n-render: PASS"
