#!/usr/bin/env bash
# Real-backend E2E + visual verification over each configured panel → screenshots + fix-tasks.
export PATH=/usr/bin:/bin:/usr/local/bin
LIB="$HOME/conductor-agent/lib"
CONF="$LIB/projects.conf"
grep -vE '^\s*#|^\s*$' "$CONF" 2>/dev/null | while read -r proj base glob _; do
  [ -n "$proj" ] || continue
  bash "$LIB/e2e-runner.sh" "$proj" "$base" "$glob" --file
done
