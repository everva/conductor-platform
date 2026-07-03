#!/usr/bin/env bash
# Periodic independent-auditor for both panels. Runs the semantic auditor (monitor
# account) over the last-N landed screens and files deduped, capped fix-tasks for the
# defect classes the per-commit gates miss. Safe: read-only review, cap+dedup guards.
export PATH=/usr/bin:/bin:/usr/local/bin
LIB=/home/davinci/conductor-agent/lib
bash "$LIB/auditor.sh" xirigo-admin  conductor/admin-redesign  5 --file
bash "$LIB/auditor.sh" xirigo-vendor conductor/vendor-redesign 5 --file
