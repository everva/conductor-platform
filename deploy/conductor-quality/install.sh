#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# Conductor Quality Kit — reproducible, capability-gated installer.
#
# Makes the whole quality layer (deterministic gates, i18n merge-driver, semantic
# auditor loop, real-backend E2E+visual runner) reproducible on ANY host from version
# control — so a formatted / re-provisioned box is one `install.sh` away from full
# recovery, and a NEW host only needs to declare its capabilities.
#
# Idempotent: safe to re-run. Server-INDEPENDENT: nothing here is baked into a specific
# machine; the host just declares CONDUCTOR_CAPABILITIES and the installer enables the
# matching features (e2e needs 'e2e', iOS steps need 'ios', etc.).
#
#   Usage: ./install.sh [--caps "linux,web,backend,quality-audit,e2e,ios"]
#          (capabilities default to CONDUCTOR_CAPABILITIES in conductor-agent.env)
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
AGENT_HOME="${CONDUCTOR_AGENT_HOME:-$HOME/conductor-agent}"
LIB="$AGENT_HOME/lib"

# ── capabilities: CLI flag > agent env > safe default ────────────────────────
CAPS=""
[ "${1:-}" = "--caps" ] && CAPS="${2:-}"
[ -z "$CAPS" ] && CAPS="$(grep '^CONDUCTOR_CAPABILITIES=' "$AGENT_HOME/conductor-agent.env" 2>/dev/null | cut -d= -f2- || true)"
[ -z "$CAPS" ] && CAPS="linux,web,backend"
has() { echo ",$CAPS," | grep -q ",$1,"; }
echo "install: AGENT_HOME=$AGENT_HOME"
echo "install: capabilities = $CAPS"

# ── 1. lib scripts — the deterministic gates + drivers + loops (always) ──────
mkdir -p "$LIB"
cp "$HERE/lib/"* "$LIB/"
chmod +x "$LIB/"*.sh "$LIB/"*.py 2>/dev/null || true
echo "install: lib/ deployed ($(ls "$HERE/lib" | wc -l | tr -d ' ') files)"

# ── 2. git merge driver (additive i18n JSON deep-merge) ──────────────────────
# Global config: a no-op unless a repo's committed .gitattributes references it, so it
# is safe everywhere. This is what makes the messages/*.json merge-hotspot fix portable.
git config --global merge.json-deepmerge.name "additive i18n json deep-merge"
git config --global merge.json-deepmerge.driver "python3 $LIB/json-merge-driver.py %O %A %B %P"
echo "install: git merge driver 'json-deepmerge' configured (global)"

# ── 3. wire the deterministic gates into any existing recipe gate-verify.sh ──
wired=0
for gv in "$AGENT_HOME"/recipes/*/gate-verify.sh; do
  [ -f "$gv" ] || continue
  grep -q fabrication-data-check "$gv" && continue         # already wired
  base="$(grep -oE 'conductor/[a-z-]+-redesign' "$gv" | head -1)"
  [ -z "$base" ] && continue                                # not a redesign recipe
  bash "$HERE/wire-gate.sh" "$gv" "$base" && wired=$((wired+1))
done
echo "install: gate-verify wired into $wired recipe(s)"

# ── 4. crons — CAPABILITY-GATED ──────────────────────────────────────────────
CT="$(mktemp)"; crontab -l 2>/dev/null | grep -vE "auditor-cron.sh|e2e-cron.sh" > "$CT" || true
if has quality-audit || has web; then
  echo "20 */3 * * * $LIB/auditor-cron.sh >> $AGENT_HOME/auditor-cron.log 2>&1" >> "$CT"
  echo "install: [cap] auditor cron ENABLED (every 3h)"
else
  echo "install: auditor cron skipped (needs 'quality-audit' or 'web')"
fi
if has e2e; then
  echo "40 */6 * * * $LIB/e2e-cron.sh >> $AGENT_HOME/e2e-cron.log 2>&1" >> "$CT"
  echo "install: [cap] e2e cron ENABLED (every 6h)"
else
  echo "install: e2e cron skipped (needs 'e2e' capability)"
fi
crontab "$CT"; rm -f "$CT"

# ── 5. env templates — seed if missing, NEVER overwrite host secrets ─────────
for ex in "$HERE"/env-templates/*.example; do
  [ -f "$ex" ] || continue
  dst="$AGENT_HOME/$(basename "${ex%.example}")"
  if [ -f "$dst" ]; then echo "install: env $(basename "$dst") exists — kept"
  else cp "$ex" "$dst"; echo "install: env $(basename "$dst") SEEDED — fill secrets before use"; fi
done

# ── 6. capability notes (feature availability on THIS host) ──────────────────
has e2e && echo "install: E2E available (browsers installed on demand by the runner)" \
        || echo "install: E2E disabled — add 'e2e' capability + a <project>-e2e.env to enable"
has ios && echo "install: iOS build steps AVAILABLE (expects darwin + Xcode)" \
        || echo "install: iOS steps disabled — add 'ios' capability on a darwin+Xcode host"

echo "install: DONE — quality kit reproduced on this host."
