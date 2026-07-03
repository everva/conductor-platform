#!/usr/bin/env bash
# Periodic real-backend E2E + visual verification for both panels. Real staging auth
# (admin: authed suite; vendor: unauthenticated redirect specs + empty auth-state).
# Screenshot per screen kept under e2e-artifacts/. Failures -> deduped/capped fix-tasks.
# No claude -> no rate concern. Manual re-run: call e2e-runner.sh directly.
export PATH=/usr/bin:/bin:/usr/local/bin
LIB=/home/davinci/conductor-agent/lib
bash "$LIB/e2e-runner.sh" xirigo-admin  conductor/admin-redesign  e2e/smoke  --file
bash "$LIB/e2e-runner.sh" xirigo-vendor conductor/vendor-redesign e2e/vendor --file
