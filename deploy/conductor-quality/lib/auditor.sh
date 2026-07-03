#!/usr/bin/env bash
# Independent AUDITOR agent (davinci-side, like autoheal). Periodically spawns a FRESH
# claude (monitor account, no develop competition) to SEMANTICALLY review recently-landed
# screens for the defect classes the per-commit gates miss (fabrication passing as real,
# dead/fake controls, cross-screen contradictions, unresolved blockers, i18n leaks, bloat),
# emits structured findings, and (with --file) converts P0/P1 findings to intake fix-tasks
# (deduped + capped). Read-only: never edits/commits the panel.
#   Usage: auditor.sh <project> <base_branch> [K_landed=4] [--file]
set -uo pipefail
PROJECT="${1:?project}"; BASE="${2:?base}"; K="${3:-4}"; MODE="${4:-report}"
CLONE=/home/davinci/.conductor-agent/clones/$PROJECT
WT=/home/davinci/conductor-agent/auditor/wt-$PROJECT
LOG=/home/davinci/conductor-agent/auditor.log
STATE=/home/davinci/conductor-agent/auditor-filed-$PROJECT.txt
PROMPT=/home/davinci/conductor-agent/lib/audit-prompt.txt
CAP=5                       # max fix-tasks filed per run (flood guard)
export CLAUDE_CONFIG_DIR=/home/davinci/.claude-acct3
ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
touch "$STATE"

cd "$CLONE" || exit 1
git fetch -q origin "$BASE" 2>/dev/null || true
lands=$(git log --format=%H --grep='^conductor: land' "origin/$BASE" 2>/dev/null | head -"$K")
files=$(for c in $lands; do git diff --name-only "${c}~1" "$c" -- src 2>/dev/null; done \
        | sort -u | grep -E '\.tsx?$' | grep -vE '\.test\.|\.spec\.|__tests__|/mock/|/mocks/|demo-data' | head -40)
if [ -z "$files" ]; then echo "$(ts) auditor[$PROJECT]: no recent landed src files" >>"$LOG"; exit 0; fi

# fresh read-only detached worktree at base tip (isolated from the agent's task worktrees)
git worktree remove --force "$WT" 2>/dev/null; rm -rf "$WT"; git worktree prune 2>/dev/null
mkdir -p "$(dirname "$WT")"
git worktree add -q --detach "$WT" "origin/$BASE" 2>/dev/null || { echo "$(ts) auditor[$PROJECT]: worktree add failed" >>"$LOG"; exit 1; }

# assemble prompt safely (placeholder replace via helper)
FILES_FILE=$(mktemp); printf '%s\n' "$files" > "$FILES_FILE"
PROMPT_FILE=$(mktemp)
python3 /home/davinci/conductor-agent/lib/auditor-assemble.py "$PROMPT" "$PROJECT" "$FILES_FILE" "$PROMPT_FILE"
rm -f "$FILES_FILE"

cd "$WT"
RAW=$(timeout 900 claude -p "$(cat "$PROMPT_FILE")" --dangerously-skip-permissions 2>/dev/null)
rm -f "$PROMPT_FILE"
cd "$CLONE"; git worktree remove --force "$WT" 2>/dev/null; rm -rf "$WT"; git worktree prune 2>/dev/null

# extract + process findings (python: parse, log, dedup, optional file)
printf '%s' "$RAW" | python3 /home/davinci/conductor-agent/lib/auditor-process.py "$PROJECT" "$STATE" "$LOG" "$MODE" "$CAP" "$(ts)" "$BASE"
