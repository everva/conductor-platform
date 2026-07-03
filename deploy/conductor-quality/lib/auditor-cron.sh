#!/usr/bin/env bash
# Semantic auditor over each configured panel's recent lands → deduped/capped fix-tasks.
export PATH=/usr/bin:/bin:/usr/local/bin
LIB="$HOME/conductor-agent/lib"
CONF="$LIB/projects.conf"
grep -vE '^\s*#|^\s*$' "$CONF" 2>/dev/null | while read -r proj base glob _; do
  [ -n "$proj" ] || continue
  bash "$LIB/auditor.sh" "$proj" "$base" 5 --file
done
