#!/usr/bin/env bash
# launchd wrapper for the conductor host-agent on a macOS host (e.g. everva). Sources the 0600 env
# file (so secrets stay OUT of the plist) and exec's the agent. PATH includes the tools the verify
# gate shells to (claude, pnpm, node, git). The macOS analogue of davinci's systemd ExecStart.
# Adjust $HOME paths + -host-id for the host. Install: see ../README.md "macOS host (launchd)".
set -euo pipefail
export PATH="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"
set -a; . "$HOME/conductor-agent/conductor-agent.env"; set +a
exec "$HOME/conductor-agent/conductor-agent" \
  -gateway "$CONDUCTOR_GATEWAY" -project "$CONDUCTOR_PROJECT" -repo "$CONDUCTOR_REPO" \
  -base "$CONDUCTOR_BASE" -recipe-dir "$CONDUCTOR_RECIPE_DIR" -root "$CONDUCTOR_ROOT" \
  -capabilities "$CONDUCTOR_CAPABILITIES" -host-id "${CONDUCTOR_HOST_ID:-$(hostname -s)}"
