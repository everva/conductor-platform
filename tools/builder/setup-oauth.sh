#!/usr/bin/env bash
# setup-oauth.sh — mint a long-lived OAuth token for the headless builder.
#
# Run this ONCE in your own interactive Terminal (NOT from launchd). The macOS
# keychain that holds your normal `claude` login is UNREACHABLE from a launchd
# daemon (ADR-0015), so the builder authenticates with a long-lived token minted
# by `claude setup-token`. The token survives reboot / keychain-lock and is read
# by every tick from the 0600 file below.
#
# subscription mode: this uses your Claude subscription via `claude -p`. There is
# NO ANTHROPIC_API_KEY involved (ADR-0015) — do not set one.
set -euo pipefail

RT="$HOME/.conductor-platform-builder"
TOKEN_FILE="$RT/oauth-token"
CLAUDE="$(command -v claude || echo "$HOME/.local/bin/claude")"

mkdir -p "$RT"

if [ ! -x "$CLAUDE" ] && ! command -v claude >/dev/null 2>&1; then
  echo "ERROR: claude CLI not found (looked for $CLAUDE and on PATH)." >&2
  echo "Install Claude Code first, then re-run this." >&2
  exit 1
fi

echo "A browser window will open. Approve the request, then copy the token it shows."
echo "(It looks like: sk-ant-oat...)"
echo
"$CLAUDE" setup-token || {
  echo "claude setup-token failed. Are you logged in interactively (claude /login)?" >&2
  exit 1
}
echo
read -r -p "Paste the token here: " TOK
if [ -z "${TOK:-}" ]; then
  echo "no token entered — aborted" >&2
  exit 1
fi

# write atomically, 0600
umask 077
printf '%s' "$TOK" > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"
chmod 600 "$TOKEN_FILE"

# clear any stale auth-expired flag now that we have a fresh token
rm -f "$RT/AUTH_EXPIRED" 2>/dev/null || true

echo "saved to $TOKEN_FILE ($(wc -c < "$TOKEN_FILE" | tr -d ' ') bytes, mode $(stat -f '%Lp' "$TOKEN_FILE"))"
echo "Done. The builder ticks will now authenticate headlessly."
