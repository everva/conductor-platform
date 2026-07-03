#!/usr/bin/env bash
# SHARED, project-agnostic fabrication + dead-control audit. Each project's gate-verify.sh calls this
# as the final HARD gate. The conductor's own diff-review misses semantic fabrication (hardcoded data
# rendered as real) + enabled dead controls; this independent LLM audit catches them so the task
# self-corrects via the re-develop loop (gate fail -> GATE.md -> re-develop) with NO human help.
#
# Usage:  fabrication-audit.sh <base_branch> <mode: frontend|backend> [src_path]
# Exit:   1 if the auditor reports a CLEAR violation (gate fails); 0 on PASS, no-diff, or unavailable.
#
# Maintained in ONE place; wired into every project. Frontend => R1 (no fabrication) + R2 (no dead
# control). Backend/Go => R1 only (no fabricated/mock data returned).
set -uo pipefail
BASE="${1:-develop}"; MODE="${2:-frontend}"; SRC="${3:-src}"
export PATH="/usr/bin:/bin:/usr/local/bin:/snap/bin:$HOME/.local/bin:$PATH"

# claude token: prefer the inherited env, else fetch from the gateway credential store (same kind the
# agent uses). CONDUCTOR_AGENT_TOKEN + CONDUCTOR_GATEWAY come from the per-project agent env.
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -n "${CONDUCTOR_AGENT_TOKEN:-}" ] && [ -n "${CONDUCTOR_GATEWAY:-}" ]; then
  export CLAUDE_CODE_OAUTH_TOKEN="$(curl -sS -H "Authorization: Bearer $CONDUCTOR_AGENT_TOKEN" "$CONDUCTOR_GATEWAY/agent/credentials/CLAUDE_CODE_OAUTH_TOKEN" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null || true)"
fi
command -v claude >/dev/null 2>&1 || { echo "audit: SKIP (no claude binary)"; exit 0; }
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] || { echo "audit: SKIP (no claude token)"; exit 0; }

git fetch -q origin "$BASE" >/dev/null 2>&1 || true
git diff "origin/${BASE}...HEAD" -- "$SRC" > /tmp/audit-diff.txt 2>/dev/null || git diff HEAD~1 -- "$SRC" > /tmp/audit-diff.txt 2>/dev/null || true
[ -s /tmp/audit-diff.txt ] || { echo "audit: no diff to audit"; exit 0; }

R2=""
[ "$MODE" = "frontend" ] && R2='R2 NO DEAD CONTROL: flag any <button> or <a> the diff renders ENABLED with no onClick, no href, AND no disabled attribute. An honestly disabled control (disabled + title) is NOT a violation.'
PROMPT="Read the file /tmp/audit-diff.txt — a git diff for ONE screen/module. Enforce these rules and nothing else. R1 NO FABRICATION / NO MOCK: flag any hardcoded/literal value the diff RENDERS or RETURNS to the user as if it were real data (fabricated names, metrics, scores, ratings, addresses, counts, percentages, reverse-computed money, mock/demo data arrays) that has no real hook/prop/db/backend source. An honest \"—\"/placeholder, a genuine static label, an i18n key like t(\"...\"), icon names, CSS, and ordinary code constants are NOT violations. IMPORTANT: design prototypes / fixtures themselves often contain fabricated demo data — do NOT treat a value as acceptable just because a prototype has it. $R2 Output EXACTLY the single line \"AUDIT: PASS\" if there are ZERO clear violations. Otherwise output \"AUDIT: FAIL\" then one line per violation formatted \"path:line - R1|R2 - short description\". Be conservative: flag only a CLEAR violation, never a maybe."

set +e
# bound the audit so a hung claude can never stall the verify gate (timeout => rc 124 => skip)
TO=""; command -v timeout >/dev/null 2>&1 && TO="timeout 300"
AUDIT="$($TO claude -p "$PROMPT" --dangerously-skip-permissions 2>/dev/null)"; rc=$?
set -e 2>/dev/null || true
echo "$AUDIT" | head -30
[ $rc -ne 0 ] && { echo "audit: tool errored (rc=$rc) — skipped; deterministic gate still applied"; exit 0; }
printf '%s\n' "$AUDIT" | head -1 | grep -q 'AUDIT: PASS' && { echo "audit: PASS"; exit 0; }
echo "audit: FAIL — fix the violations above (honest '—' or real data; wire/gate/omit controls). NEVER fabricate."
exit 1
