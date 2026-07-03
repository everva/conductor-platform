#!/usr/bin/env bash
# Drain the panel-wide i18n hardcoded-string debt a few screens per run (capped in the
# sweep) into remediation-priority fix-tasks, per configured project.
export PATH=/usr/bin:/bin:/usr/local/bin
LIB="$HOME/conductor-agent/lib"
grep -vE "^\s*#|^\s*$" "$LIB/projects.conf" 2>/dev/null | while read -r proj base glob _; do
  [ -n "$proj" ] || continue
  bash "$LIB/i18n-debt-sweep.sh" "$proj" "$base" --file
done
