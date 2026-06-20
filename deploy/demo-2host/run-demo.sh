#!/usr/bin/env bash
# deploy/demo-2host/run-demo.sh — the LIVE two-machine (Linux + macOS) capability-
# routed multi-project demo, Faz-A: genuinely-separate daemon PROCESSES coordinating
# through ONE shared Postgres, proving capability routing + multi-project + the
# deterministic gate — all with the REAL conductor + conductorctl binaries.
#
# It stands in for two physical machines with TWO daemon processes against the SAME
# -dsn, each with a distinct -host / -capabilities:
#   host-linux  caps: linux,web,backend      drives the WEB project
#   host-mac    caps: macos,ios-build,maestro drives the iOS project
#
# What it proves, live, with separate OS processes:
#   1. CAPABILITY REFUSAL (deterministic, -once): host-linux CANNOT pick the iOS
#      task (requires ios-build ⊄ linux caps) — a clean no-op, nothing merges.
#   2. ROUTED WORK (looping daemons): host-linux merges the web task; host-mac merges
#      the iOS task — multi-project, each on its capable host, through the real
#      develop→verify→merge pipeline with a [task:<id>] trailer.
#   3. GATE: the merge rides the INDEPENDENT deterministic gate (go build/test), never
#      a performer self-report (Rule#9).
#
# SANDBOX NOTE: the develop "brain" here is a DETERMINISTIC sh-performer, NOT
# `claude -p` (claude subprocess auth is unavailable in this nested-agent sandbox).
# The routing / multi-host / lease / gate MECHANISM is the genuine article; only the
# LLM is swapped for a script — exactly the e2e_test.go discipline. On a real host
# (Faz C) -develop-cmd is `claude -p` and the gates add the visual/maestro recipe.
#
# Requires: a running Postgres (default: the :5433 demo container `conductor-pg`),
# git, go. It creates an ISOLATED schema and DROPS it on exit, so it never pollutes
# the shared database. Set KEEP=1 to retain the workdir + schema for inspection.
#
# Usage:
#   deploy/demo-2host/run-demo.sh
#   PG_DSN='postgres://user:pw@host:5432/db?sslmode=disable' deploy/demo-2host/run-demo.sh
#   KEEP=1 deploy/demo-2host/run-demo.sh
set -euo pipefail

# --- config ------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PG_DSN="${PG_DSN:-postgres://conductor:conductor@localhost:5433/conductor?sslmode=disable}"
PG_CONTAINER="${PG_CONTAINER:-conductor-pg}"
SCHEMA="${SCHEMA:-cp_demo_$$}"
WORK="$(mktemp -d -t cp-demo-2host.XXXXXX)"
DAEMON_INTERVAL="${DAEMON_INTERVAL:-2s}"
POLL_TIMEOUT="${POLL_TIMEOUT:-120}"

# Schema-scoped DSN: every process (conductorctl + both daemons) targets the SAME
# isolated schema via search_path, so the demo shares one store but no public tables.
sep='?'; [[ "$PG_DSN" == *'?'* ]] && sep='&'
SCHEMA_DSN="${PG_DSN}${sep}search_path=${SCHEMA}"

LINUX_ROOT="$WORK/host-linux"      # host-linux daemon workspace (clones/worktrees)
MAC_ROOT="$WORK/host-mac"          # host-mac daemon workspace
MISASSIGN_ROOT="$WORK/host-linux-misassign"
BIN="$WORK/bin"
LOGS="$WORK/logs"
mkdir -p "$LINUX_ROOT" "$MAC_ROOT" "$MISASSIGN_ROOT" "$BIN" "$LOGS"

LINUX_PID="" ; MAC_PID=""

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()  { printf '  \033[1;32m✓\033[0m %s\n' "$*"; }
die() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

psql_exec() { docker exec -i "$PG_CONTAINER" psql -U conductor -d conductor -v ON_ERROR_STOP=1 -qtA -c "$1"; }

cleanup() {
  local rc=$?
  [[ -n "$LINUX_PID" ]] && kill "$LINUX_PID" 2>/dev/null || true
  [[ -n "$MAC_PID" ]] && kill "$MAC_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  if [[ "${KEEP:-0}" == "1" ]]; then
    printf '\n[KEEP=1] workdir retained: %s   schema: %s\n' "$WORK" "$SCHEMA"
  else
    psql_exec "DROP SCHEMA IF EXISTS ${SCHEMA} CASCADE;" >/dev/null 2>&1 || true
    rm -rf "$WORK"
  fi
  return $rc
}
trap cleanup EXIT

# --- 0. preflight ------------------------------------------------------------
command -v git >/dev/null || die "git not found"
command -v go  >/dev/null || die "go not found"
docker exec "$PG_CONTAINER" true 2>/dev/null || die "Postgres container '$PG_CONTAINER' not reachable (set PG_CONTAINER / start it)"

# --- 1. build the REAL binaries ----------------------------------------------
say "Building conductor + conductorctl"
( cd "$REPO_ROOT" && go build -o "$BIN/conductor" ./cmd/conductor && go build -o "$BIN/conductorctl" ./cmd/conductorctl )
ok "binaries at $BIN"
CTL=("$BIN/conductorctl" -dsn "$SCHEMA_DSN")

# --- 2. the deterministic performer (stands in for `claude -p`) --------------
# Writes a compiling, test-passing Go file + test into the worktree (package app),
# commits on the per-task branch, and emits a schema-valid Verdict on stdout. The
# INDEPENDENT gate (go build/test) is what actually decides the merge (Rule#9).
PERFORMER="$WORK/performer.sh"
cat > "$PERFORMER" <<'PERF'
#!/bin/sh
set -e
cat > feature.go <<'EOF'
package app

// Feature is added by the deterministic demo performer (stands in for claude -p).
func Feature() string { return "shipped" }
EOF
cat > feature_test.go <<'EOF'
package app

import "testing"

func TestFeature(t *testing.T) {
	if Feature() != "shipped" {
		t.Fatalf("got %q", Feature())
	}
}
EOF
export GIT_AUTHOR_NAME=performer GIT_AUTHOR_EMAIL=performer@local
export GIT_COMMITTER_NAME=performer GIT_COMMITTER_EMAIL=performer@local
git add feature.go feature_test.go
git commit -q -m "feat: add Feature helper"
BR=$(git rev-parse --abbrev-ref HEAD)
SHA=$(git rev-parse HEAD)
cat <<EOF
{"result":"pass","branch":"$BR","commit_sha":"$SHA","checks":[{"name":"local","result":"pass","evidence":"committed"}],"files":["feature.go","feature_test.go"],"summary":"added Feature"}
EOF
PERF
chmod +x "$PERFORMER"
ok "performer at $PERFORMER"

# --- 3. two throwaway product repos (web-shaped + iOS-shaped) -----------------
# Each is a real git repo on `develop` with a valid Go module so the deterministic
# gate runs. The web repo carries a playwright marker, the iOS repo maestro/Xcode
# markers — so the scaffolder would detect web/ios (visual-diff / maestro recipe,
# proven in internal/scaffolder + visual_e2e_test.go). The daemon here uses the
# default Go gate; the recipe gates are the Faz-C step.
seed_repo() { # <dir> <module> ; extra markers added by caller
  local dir="$1" mod="$2"
  git init -q "$dir"
  ( cd "$dir"
    git config user.name demo && git config user.email demo@local
    git checkout -q -b develop
    printf 'module %s\n\ngo 1.26\n' "$mod" > go.mod
    printf 'package app\n\n// Name is the seed symbol.\nfunc Name() string { return "%s" }\n' "$mod" > app.go
    printf 'package app\n\nimport "testing"\n\nfunc TestName(t *testing.T) {\n\tif Name() == "" {\n\t\tt.Fatal("empty")\n\t}\n}\n' > app_test.go
  )
}
WEB_REPO="$WORK/web-shop"   # -> project id "web-shop"
IOS_REPO="$WORK/ios-app"    # -> project id "ios-app"
seed_repo "$WEB_REPO" "web-shop"
printf 'import { defineConfig } from "@playwright/test";\nexport default defineConfig({});\n' > "$WEB_REPO/playwright.config.ts"
( cd "$WEB_REPO" && git add . && git commit -q -m "seed: web-shop (playwright-shaped)" )
seed_repo "$IOS_REPO" "ios-app"
mkdir -p "$IOS_REPO/maestro" "$IOS_REPO/App.xcodeproj"
printf 'appId: com.demo.app\n---\n- launchApp\n' > "$IOS_REPO/maestro/flow.yaml"
printf '<!-- demo xcode project marker -->\n' > "$IOS_REPO/App.xcodeproj/project.pbxproj"
( cd "$IOS_REPO" && git add . && git commit -q -m "seed: ios-app (maestro/Xcode-shaped)" )
ok "repos: $WEB_REPO (web-shop), $IOS_REPO (ios-app)"

# --- 3b. hidden holdouts (ADR-0018), repo-EXTERNAL --------------------------
# Each scenario's hidden_holdout_ref resolves under this root: store://holdouts/<id>/...
# → <root>/holdouts/<id>/inject/* is injected into the throwaway VERIFY worktree at
# verify time (the performer never sees it, cannot overfit). The holdout is a REAL
# acceptance test the gate runs (`go test ./...`) — proving the merge rides a hidden
# check, not the performer's self-report (Rule#9 / ADR-0018).
HOLDOUT_ROOT="$WORK/holdouts-external"
write_holdout() { # <scenario-id>
  local d="$HOLDOUT_ROOT/holdouts/$1/inject"
  mkdir -p "$d"
  cat > "$d/holdout_test.go" <<EOF
package app

import "testing"

// Hidden acceptance holdout for $1 (ADR-0018): injected at verify time only.
func TestHoldout_${1//-/_}(t *testing.T) {
	if Feature() != "shipped" {
		t.Fatalf("hidden holdout: Feature()=%q, want \"shipped\"", Feature())
	}
}
EOF
}
write_holdout W-1
write_holdout I-1
ok "hidden holdouts at $HOLDOUT_ROOT (injected at verify, never in the repo)"

# --- 4. isolated schema (shared by all processes, dropped on exit) -----------
say "Creating isolated schema $SCHEMA"
psql_exec "CREATE SCHEMA ${SCHEMA};" >/dev/null
ok "schema $SCHEMA"

# --- 5. onboard + intake both projects (REAL conductorctl) -------------------
say "Onboarding projects + intaking scenarios (conductorctl)"
"${CTL[@]}" onboard "$WEB_REPO" >/dev/null
"${CTL[@]}" onboard "$IOS_REPO" >/dev/null
cat > "$WORK/web.scenario.yaml" <<'EOF'
id: W-1
title: "Web: ship the Feature helper"
lane: web
tier: T2
acceptance:
  - "package app exposes Feature() returning a non-empty string"
hidden_holdout_ref: "store://holdouts/W-1/holdout.go"
EOF
cat > "$WORK/ios.scenario.yaml" <<'EOF'
id: I-1
title: "iOS: ship the Feature helper"
lane: ios
tier: T2
acceptance:
  - "package app exposes Feature() returning a non-empty string"
hidden_holdout_ref: "store://holdouts/I-1/holdout.go"
EOF
"${CTL[@]}" intake --project web-shop --file "$WORK/web.scenario.yaml" >/dev/null
"${CTL[@]}" intake --project ios-app  --file "$WORK/ios.scenario.yaml" >/dev/null
ok "web-shop ← W-1, ios-app ← I-1"

# Tag the iOS task with its host-capability requirement (requires: ios-build) so it
# HARD-routes to a Mac host. The recipe's Capability is "ios-build" (ADR-0008); this
# stamps that routing requirement onto the task the way the gateway/recipe wiring
# will. (conductorctl has no task-requires verb yet — a documented follow-up.)
say "Tagging I-1 with requires=[ios-build] (the iOS routing requirement)"
psql_exec "UPDATE ${SCHEMA}.tasks SET requires = '[\"ios-build\"]'::jsonb WHERE id = 'I-1';" >/dev/null
[[ "$(psql_exec "SELECT requires FROM ${SCHEMA}.tasks WHERE id='I-1';")" == *ios-build* ]] || die "could not set I-1 requires"
ok "I-1 requires ios-build"

# trailer_present reports whether a clone's develop tip carries the [task:<id>]
# merge trailer — the unambiguous git-truth signal that the task actually merged.
trailer_present() { git -C "$1" log -1 --format=%B develop 2>/dev/null | grep -q "\[task:$2\]"; }

# --- 6. CAPABILITY REFUSAL (deterministic, -once) ----------------------------
# host-linux ticks the iOS project ONCE. I-1 requires ios-build ⊄ {linux,web,backend}
# → PickReady skips it → clean no-op, exit 0, nothing merges (no clone is even cut).
say "PROOF 1 — capability refusal: host-linux -once on ios-app (must NOT pick I-1)"
"$BIN/conductor" -once -project ios-app -dsn "$SCHEMA_DSN" \
  -host host-linux -capabilities linux,web,backend \
  -develop-cmd "$PERFORMER" -root "$MISASSIGN_ROOT" -holdout-store "$HOLDOUT_ROOT" -interval "$DAEMON_INTERVAL" \
  > "$LOGS/refusal.log" 2>&1 || die "host-linux -once exited non-zero (see $LOGS/refusal.log)"
[[ ! -d "$MISASSIGN_ROOT/clones/ios-app" ]] || die "host-linux must not have cloned ios-app (it cannot run it)"
ok "host-linux refused I-1 (no clone, no work) — capability routing holds"

# --- 7. ROUTED WORK — two genuinely-separate daemon processes ----------------
say "PROOF 2 — routed work: starting two REAL daemon processes (shared -dsn)"
"$BIN/conductor" -project web-shop -dsn "$SCHEMA_DSN" \
  -host host-linux -capabilities linux,web,backend \
  -develop-cmd "$PERFORMER" -root "$LINUX_ROOT" -holdout-store "$HOLDOUT_ROOT" -interval "$DAEMON_INTERVAL" \
  > "$LOGS/host-linux.log" 2>&1 &
LINUX_PID=$!
"$BIN/conductor" -project ios-app -dsn "$SCHEMA_DSN" \
  -host host-mac -capabilities macos,ios-build,maestro \
  -develop-cmd "$PERFORMER" -root "$MAC_ROOT" -holdout-store "$HOLDOUT_ROOT" -interval "$DAEMON_INTERVAL" \
  > "$LOGS/host-mac.log" 2>&1 &
MAC_PID=$!
ok "host-linux pid=$LINUX_PID (→web-shop), host-mac pid=$MAC_PID (→ios-app)"

say "Waiting for both tasks to merge (timeout ${POLL_TIMEOUT}s)"
deadline=$((SECONDS + POLL_TIMEOUT))
while :; do
  w=no; trailer_present "$LINUX_ROOT/clones/web-shop" W-1 && w=MERGED
  i=no; trailer_present "$MAC_ROOT/clones/ios-app" I-1 && i=MERGED
  printf '\r  web-shop/W-1=%-7s ios-app/I-1=%-7s' "$w" "$i"
  [[ "$w" == MERGED && "$i" == MERGED ]] && { echo; break; }
  kill -0 "$LINUX_PID" 2>/dev/null || { echo; die "host-linux daemon died early (see $LOGS/host-linux.log)"; }
  kill -0 "$MAC_PID" 2>/dev/null || { echo; die "host-mac daemon died early (see $LOGS/host-mac.log)"; }
  [[ $SECONDS -lt $deadline ]] || { echo; die "timeout: web=$w ios=$i (logs in $LOGS)"; }
  sleep 1
done
ok "both tasks merged"

# --- 8. git truth: each merge landed on the RIGHT host's clone ---------------
say "PROOF 3 — git truth: routed merges carry the right [task:<id>] trailer"
web_tip="$(git -C "$LINUX_ROOT/clones/web-shop" log -1 --format=%B develop)"
ios_tip="$(git -C "$MAC_ROOT/clones/ios-app" log -1 --format=%B develop)"
[[ "$web_tip" == *"[task:W-1]"* ]] || die "web-shop develop missing [task:W-1] on host-linux:\n$web_tip"
[[ "$ios_tip" == *"[task:I-1]"* ]] || die "ios-app develop missing [task:I-1] on host-mac:\n$ios_tip"
# No cross-contamination: host-linux never cloned ios-app, host-mac never cloned web-shop.
[[ ! -d "$LINUX_ROOT/clones/ios-app" ]] || die "host-linux must not have touched ios-app"
[[ ! -d "$MAC_ROOT/clones/web-shop" ]] || die "host-mac must not have touched web-shop"
ok "web-shop merged on host-linux, ios-app merged on host-mac — no cross-contamination"

# --- 9. report ---------------------------------------------------------------
say "Hosts (shared registry)"
"${CTL[@]}" hosts
say "Ledger — web-shop"
"${CTL[@]}" status --project web-shop
say "Ledger — ios-app"
"${CTL[@]}" status --project ios-app

printf '\n\033[1;32m================================================================\033[0m\n'
printf ' DEMO PASSED — two-machine capability-routed multi-project, LIVE\n'
printf '   • host-linux (linux,web,backend)   REFUSED iOS work, MERGED web work\n'
printf '   • host-mac   (macos,ios-build,maestro) MERGED iOS work\n'
printf '   • both via the deterministic gate (Rule#9), separate OS processes,\n'
printf '     one shared Postgres schema (%s)\n' "$SCHEMA"
printf '\033[1;32m================================================================\033[0m\n'
