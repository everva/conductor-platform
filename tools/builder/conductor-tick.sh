#!/usr/bin/env bash
# conductor-tick.sh — ONE headless builder tick for conductor-platform.
#
# Ported from xirigo (docs/blueprint/ops/conductor-tick.sh). The builder is a
# LIGHT light-conductor (ADR-0019): it is NOT the conductor-platform product —
# it is the bash+python tool that BUILDS that Go product, one Faz-1a task at a
# time, autonomously.
#
# Invoked by launchd (com.conductor-platform-builder) every StartInterval. Each
# run executes ONE tick with FRESH context (the builder reads all durable state
# from the on-disk JSON ledger — ORCHESTRATION.md §0). Self-protecting per
# ADR-0016 (liveness/recovery, ported verbatim from xirigo's hard-won fixes):
#   - mkdir-atomic lock so overlapping launchd fires no-op instead of racing
#   - stale-lock steal if the holder PID is dead (the "105-min stall" lesson)
#   - progress-aware watchdog kills ONLY a genuinely-wedged tick (mtime of the
#     stream output + go build/test cache), never an actively-building tick
#   - orphan-sweep: kill lockless `claude -p` performer trees reparented to init
#   - caffeinate keeps the Mac awake for the whole tick (ADR-0015)
#   - explicit PATH for claude / go / gh / golangci-lint (launchd empty-PATH)
#   - CLAUDE_CODE_OAUTH_TOKEN read from the 0600 token file (keychain is
#     unreachable from launchd — ADR-0015). subscription `claude -p`, NO API key.
#
# A tick = pick ONE ready task from the ledger -> spawn a performer (`claude -p`,
# pipeline runs INSIDE the performer, ADR-0014) -> parse the Verdict JSON
# deterministically (malformed/no-verdict -> blocked; "Not logged in" -> stop +
# notify, ADR-0014) -> update the ledger. The Go quality gate
# (go build/test/vet + golangci-lint, ADR-0019/ADR-0003) is run INSIDE the
# performer; this script only orchestrates and trusts the deterministic Verdict.
set -u

# --- explicit PATH (launchd starts with an almost-empty PATH; ADR-0015) -------
# Cover Homebrew (Intel + Apple Silicon), ~/.local/bin, the Go toolchain, and
# GOPATH/bin where golangci-lint usually lands via `go install`.
export PATH="$HOME/.local/bin:$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin:/opt/homebrew/go/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"

RUNTIME="$HOME/.conductor-platform-builder"
mkdir -p "$RUNTIME"

# --- PAUSE switch: graceful stop. If the flag exists, start NO new tick. -------
# A tick already running is unaffected (it finishes its task + commits).
# Resume = rm ~/.conductor-platform-builder/PAUSE
if [ -f "$RUNTIME/PAUSE" ]; then exit 0; fi

# --- paths --------------------------------------------------------------------
# REPO = the conductor-platform Go repo this builder is coding. Default assumes
# the builder scripts live inside that repo (tools/builder/), so REPO is two
# levels up. Override with CP_BUILDER_REPO if the clone lives elsewhere.
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${CP_BUILDER_REPO:-$(cd "$SELF_DIR/../.." && pwd)}"
PROMPT_FILE="$SELF_DIR/conductor-prompt.txt"
SCENARIOS_DIR="$SELF_DIR/scenarios"

# Ledger lives in RUNTIME (working state), NOT in the repo — mirrors the xirigo
# blueprint(versioned) <-> ~/.xirigo(runtime) split (ADR-0019). On first run we
# seed it from the versioned template under state/ in the repo.
LEDGER="$RUNTIME/state.json"
LEASES="$RUNTIME/leases.json"
JOURNAL_DIR="$RUNTIME/journal"
LOCK="$RUNTIME/conductor.lock"
HB_SCRIPT="$SELF_DIR/write-heartbeat.py"

CLAUDE="$(command -v claude || echo "$HOME/.local/bin/claude")"
PYTHON="$(command -v python3 || echo /usr/bin/python3)"

mkdir -p "$JOURNAL_DIR"
ts() { date +"%Y-%m-%dT%H:%M:%S%z"; }
LOG="$RUNTIME/conductor-tick.log"
log() { echo "[$(ts)] $*" | tee -a "$LOG" >&2; }

cd "$REPO" || { log "FATAL: repo not found: $REPO (set CP_BUILDER_REPO)"; exit 1; }

# Seed the runtime ledger from the versioned template on first run.
if [ ! -f "$LEDGER" ]; then
  if [ -f "$SELF_DIR/state/state.json" ]; then
    cp "$SELF_DIR/state/state.json" "$LEDGER"
    log "seeded ledger from template -> $LEDGER"
  else
    log "FATAL: no ledger and no template at $SELF_DIR/state/state.json — skipping"
    exit 0
  fi
fi
if [ ! -f "$LEASES" ]; then
  if [ -f "$SELF_DIR/state/leases.json" ]; then
    cp "$SELF_DIR/state/leases.json" "$LEASES"
  else
    printf '{"active": [], "max_workers": 1, "max_per_repo": 1}\n' > "$LEASES"
  fi
fi

# ---- lock (mkdir is atomic; no flock on macOS — ADR-0016) -------------------
acquire_lock() {
  if mkdir "$LOCK" 2>/dev/null; then
    echo "$$" > "$LOCK/pid"
    return 0
  fi
  local holder
  holder="$(cat "$LOCK/pid" 2>/dev/null || echo "")"
  if [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null; then
    log "tick already running (pid $holder) — skipping this fire"
    return 1
  fi
  # holder DEAD (or pid unreadable) — prior tick crashed before its EXIT trap.
  # A dead holder means nothing is running; steal immediately (waiting just
  # stalls recovery — the xirigo 105-min stall bug).
  log "stealing lock from dead/absent holder (pid=${holder:-none}) — prior tick crashed"
  rm -rf "$LOCK"
  if mkdir "$LOCK" 2>/dev/null; then echo "$$" > "$LOCK/pid"; return 0; fi
  log "could not acquire lock after steal (race) — skipping this fire"
  return 1
}
acquire_lock || exit 0
trap 'rm -rf "$LOCK"' EXIT

# ---- orphan-sweep: lockless performer claude trees (ADR-0016) ---------------
# A prior kill/crash can leave a `claude -p` performer reparented to init (PID 1)
# while we now hold the lock. Two such trees would double-write the same repo/
# ledger. We hold the lock and have NOT spawned our claude yet, so any existing
# builder performer is an orphan: kill it.
for opid in $(pgrep -f 'claude -p .*CONDUCTOR-PLATFORM BUILDER PERFORMER' 2>/dev/null); do
  log "killing orphan builder performer claude pid=$opid (we hold the lock; it is lockless)"
  kill -TERM "$opid" 2>/dev/null || true
done

# ---- auth: long-lived OAuth token (keychain unreachable from launchd) --------
# Minted once via setup-oauth.sh; survives reboot / keychain lock (ADR-0015).
if [ -f "$RUNTIME/oauth-token" ]; then
  CLAUDE_CODE_OAUTH_TOKEN="$(cat "$RUNTIME/oauth-token")"
  export CLAUDE_CODE_OAUTH_TOKEN
else
  log "FATAL: no $RUNTIME/oauth-token — run tools/builder/setup-oauth.sh once. Skipping tick."
  exit 0
fi
# subscription mode: ensure no stray API key forces metered billing (ADR-0015).
unset ANTHROPIC_API_KEY

# ---- preflight (ADR-0015): fail loud, never silent ---------------------------
preflight_ok=1
command -v claude >/dev/null 2>&1 || { log "PREFLIGHT FAIL: claude not on PATH"; preflight_ok=0; }
command -v go     >/dev/null 2>&1 || { log "PREFLIGHT FAIL: go not on PATH"; preflight_ok=0; }
command -v gh     >/dev/null 2>&1 || log "PREFLIGHT WARN: gh not on PATH (commits ok, push/PR may fail)"
command -v golangci-lint >/dev/null 2>&1 || log "PREFLIGHT WARN: golangci-lint not on PATH — gate will fail inside performer"
[ "$preflight_ok" = 1 ] || { log "preflight failed — skipping tick (fix env, re-fire)"; exit 0; }

# ---- pick ONE ready task from the ledger (deterministic, in python) ----------
# A task is "ready" when status==ready (or todo with deps all done) and no lease
# is held against it. Single-host Faz-1a: max_workers=1, so if any lease is
# active we no-op this fire. Emits the chosen task id + its scenario_ref path,
# or "NONE" / "BUSY" / "ALLDONE".
PICK="$("$PYTHON" - "$LEDGER" "$LEASES" "$SCENARIOS_DIR" <<'PYEOF'
import json, os, sys
ledger_p, leases_p, scen_dir = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(ledger_p))
tasks = d.get("tasks", [])
le = json.load(open(leases_p))
active = le.get("active", [])
maxw = le.get("max_workers", 1)
by_id = {t["id"]: t for t in tasks}
def is_done(tid): return by_id.get(tid, {}).get("status") == "done"
nondone = [t for t in tasks if t.get("status") not in ("done", "blocked")]
if not nondone:
    print("ALLDONE"); sys.exit(0)
# single-host: any active lease => busy
if len(active) >= maxw:
    print("BUSY"); sys.exit(0)
# pick: status ready|todo, all deps done, ordered by list order (plan order)
for t in tasks:
    if t.get("status") not in ("ready", "todo"):
        continue
    deps = t.get("deps", [])
    if all(is_done(dep) for dep in deps):
        ref = t.get("scenario_ref", "")
        path = os.path.join(scen_dir, ref) if ref and not os.path.isabs(ref) else ref
        print(f"TASK\t{t['id']}\t{t.get('lane','')}\t{path}")
        sys.exit(0)
print("NONE")
PYEOF
)"

case "$PICK" in
  ALLDONE) log "all tasks done/blocked — nothing to do"; "$PYTHON" "$HB_SCRIPT" 2>/dev/null || true; exit 0 ;;
  BUSY)    log "a lease is active (single-host max_workers reached) — skipping fire"; exit 0 ;;
  NONE)    log "no ready task (deps unmet or all in-flight) — skipping fire"; exit 0 ;;
esac

TASK_ID="$(printf '%s' "$PICK" | cut -f2)"
TASK_LANE="$(printf '%s' "$PICK" | cut -f3)"
SCEN_PATH="$(printf '%s' "$PICK" | cut -f4)"
log "picked task=$TASK_ID lane=$TASK_LANE scenario=$SCEN_PATH"

if [ ! -f "$SCEN_PATH" ]; then
  log "FATAL: scenario file missing: $SCEN_PATH — marking task blocked"
  "$PYTHON" - "$LEDGER" "$TASK_ID" "missing scenario file: $SCEN_PATH" <<'PYEOF'
import json, sys
lp, tid, reason = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(lp))
for t in d.get("tasks", []):
    if t["id"] == tid:
        t["status"] = "blocked"; t["blocked_reason"] = reason
json.dump(d, open(lp, "w"), indent=2, ensure_ascii=False)
PYEOF
  exit 0
fi

# ---- take lease + mark running -----------------------------------------------
"$PYTHON" - "$LEDGER" "$LEASES" "$TASK_ID" "$$" <<'PYEOF'
import json, sys, time
lp, lep, tid, pid = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
d = json.load(open(lp))
for t in d.get("tasks", []):
    if t["id"] == tid:
        t["status"] = "running"
json.dump(d, open(lp, "w"), indent=2, ensure_ascii=False)
le = json.load(open(lep))
le.setdefault("active", [])
le["active"] = [a for a in le["active"] if a.get("task") != tid]
le["active"].append({"task": tid, "pid": pid, "started_at": int(time.time())})
json.dump(le, open(lep, "w"), indent=2, ensure_ascii=False)
PYEOF

# ---- build the performer stdin (ADR-0014 contract) ---------------------------
# stdin = the performer prompt (instruction) + the scenario YAML (spec + public
# acceptance tests + holdout pointer). The performer runs the FULL pipeline
# (architect -> test-first -> dev -> self-heal -> gate -> commit) INSIDE itself
# and prints a schema-forced Verdict JSON on stdout. Go does NOT orchestrate the
# pipeline (ADR-0014).
PERFORMER_INPUT="$(
  cat "$PROMPT_FILE"
  printf '\n\n===== TASK / SCENARIO (YAML) =====\n'
  printf 'task_id: %s\n' "$TASK_ID"
  printf 'repo_path: %s\n' "$REPO"
  printf -- '---\n'
  cat "$SCEN_PATH"
)"

# ---- run one tick (PROGRESS-AWARE watchdog — ADR-0016) -----------------------
# The performer is killed ONLY when genuinely WEDGED: for FREEZE_LIMIT seconds
# there is NO activity of any kind — no live claude child process and no fresh
# stream output / go build cache mtime. A healthy long tick (a slow `go test` or
# a cold module download) always keeps one of those signals fresh and is NEVER
# killed regardless of elapsed time. MAX_TOTAL is the absolute backstop against
# a true runaway loop (ADR-0006 Layer-3).
FREEZE_LIMIT=1200     # 20 min of ZERO activity => wedged (Go gate is fast vs iOS)
MAX_TOTAL=10800       # 3 h absolute backstop
OUT="$RUNTIME/conductor-tick.out.jsonl"
GOCACHE_DIR="$(go env GOCACHE 2>/dev/null || echo "$HOME/Library/Caches/go-build")"
log "tick start task=$TASK_ID (freeze-limit=${FREEZE_LIMIT}s, max=${MAX_TOTAL}s)"

# Keep the Mac awake for the whole tick; self-exits when this script ($$) exits.
caffeinate -dimsu -w "$$" &

: > "$OUT"   # fresh stream per tick; baseline activity = now (no false wedge)
"$CLAUDE" -p "$PERFORMER_INPUT" \
    --dangerously-skip-permissions \
    --model opus \
    --output-format stream-json \
    --verbose \
  >> "$OUT" 2>> "$RUNTIME/conductor-tick.err.log" &
CLAUDE_PID=$!

start=$(date +%s)
killed=""
mtime_of() { stat -f %m "$1" 2>/dev/null || echo 0; }
newest_in() { # newest mtime of files under a dir, bounded; 0 if none
  local d="$1" n=0 m
  [ -d "$d" ] || { echo 0; return; }
  m=$(stat -f %m "$d" 2>/dev/null || echo 0); [ "$m" -gt "$n" ] && n=$m
  echo "$n"
}
idle_seconds() {
  # Seconds since the most recent sign of life, SCOPED to this tick's performer:
  #   - claude has ANY direct child  => mid tool-call / build / subagent => active
  #   - else newest of: claude stream output ($OUT) mtime, GOCACHE dir mtime
  #     (refreshed by `go build`/`go test`). NOT a global `go` pgrep: an
  #     unrelated compile must not count as this tick's activity.
  local now last m
  now=$(date +%s)
  if pgrep -P "$CLAUDE_PID" >/dev/null 2>&1; then echo 0; return; fi
  last=$(mtime_of "$OUT")
  m=$(newest_in "$GOCACHE_DIR"); [ "$m" -gt "$last" ] && last=$m
  echo $(( now - last ))
}
while kill -0 "$CLAUDE_PID" 2>/dev/null; do
  sleep 60
  now=$(date +%s)
  if [ $(( now - start )) -ge "$MAX_TOTAL" ]; then
    log "MAX_TOTAL ${MAX_TOTAL}s reached — killing tick (claude pid $CLAUDE_PID)"
    killed="max"
    kill -TERM "$CLAUDE_PID" 2>/dev/null; sleep 8; kill -KILL "$CLAUDE_PID" 2>/dev/null
    break
  fi
  idle=$(idle_seconds)
  if [ "$idle" -ge "$FREEZE_LIMIT" ]; then
    log "WEDGED: no claude/go activity for ${idle}s (>=${FREEZE_LIMIT}) — killing tick (claude pid $CLAUDE_PID)"
    killed="wedge"
    kill -TERM "$CLAUDE_PID" 2>/dev/null; sleep 8; kill -KILL "$CLAUDE_PID" 2>/dev/null
    break
  fi
done
wait "$CLAUDE_PID" 2>/dev/null
rc=$?

# ---- parse the Verdict JSON deterministically (ADR-0014) ---------------------
# Output handling rules (ADR-0014, no silent retry / no false-green):
#   - "Not logged in" / auth-expiry anywhere in output  -> STOP whole tick +
#     write AUTH_EXPIRED flag (reconcile/operator surfaces it). Do NOT mark the
#     task blocked (auth is not the task's fault) and do NOT kickstart.
#   - killed (wedge/max) or rc!=0 with no verdict         -> blocked: no-verdict
#   - last JSON block missing/unparseable                 -> blocked: malformed-verdict
#   - verdict.result == pass  -> task done   (gate ran + passed INSIDE performer)
#   - verdict.result == fail/blocked -> retry_count++ ; >=2 -> blocked, else ready
RESULT="$("$PYTHON" - "$LEDGER" "$LEASES" "$JOURNAL_DIR" "$OUT" "$TASK_ID" "$killed" "$rc" "$RUNTIME" <<'PYEOF'
import json, os, re, sys, time
lp, lep, jdir, outp, tid, killed, rc, runtime = sys.argv[1:9]
rc = int(rc)

raw = ""
try:
    with open(outp, "r", errors="replace") as f:
        raw = f.read()
except Exception:
    raw = ""

# stream-json: assistant text is spread across events. Flatten any text we can
# find so JSON-extraction + the auth-string check both work.
flat = []
for line in raw.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        flat.append(line)
        continue
    # claude stream-json: text lives in message.content[].text or result
    def walk(o):
        if isinstance(o, dict):
            if "text" in o and isinstance(o["text"], str):
                flat.append(o["text"])
            for v in o.values():
                walk(v)
        elif isinstance(o, list):
            for v in o:
                walk(v)
    walk(ev)
text = "\n".join(flat) if flat else raw

# --- auth wall (ADR-0014): stop the whole tick, surface, do NOT block task ---
auth_markers = ("Not logged in", "Invalid API key", "OAuth token expired",
                "Please run /login", "authentication_error")
if any(m.lower() in text.lower() for m in auth_markers):
    with open(os.path.join(runtime, "AUTH_EXPIRED"), "w") as f:
        f.write(time.strftime("%Y-%m-%dT%H:%M:%S%z"))
    # release lease, revert task to ready (not its fault)
    d = json.load(open(lp))
    for t in d.get("tasks", []):
        if t["id"] == tid and t.get("status") == "running":
            t["status"] = "ready"
    json.dump(d, open(lp, "w"), indent=2, ensure_ascii=False)
    le = json.load(open(lep)); le["active"] = [a for a in le.get("active", []) if a.get("task") != tid]
    json.dump(le, open(lep, "w"), indent=2, ensure_ascii=False)
    print("AUTH_EXPIRED")
    sys.exit(0)

def extract_last_json(s):
    """Last balanced {...} block that parses AND has a 'result' key."""
    cands, depth, startpos = [], 0, None
    for i, ch in enumerate(s):
        if ch == "{":
            if depth == 0: startpos = i
            depth += 1
        elif ch == "}":
            if depth > 0:
                depth -= 1
                if depth == 0 and startpos is not None:
                    cands.append(s[startpos:i+1]); startpos = None
    for blk in reversed(cands):
        try:
            o = json.loads(blk)
        except Exception:
            continue
        if isinstance(o, dict) and "result" in o:
            return o
    return None

verdict = extract_last_json(text)

d = json.load(open(lp))
task = next((t for t in d.get("tasks", []) if t["id"] == tid), None)
if task is None:
    print("UNKNOWN_TASK"); sys.exit(0)

def release_lease():
    le = json.load(open(lep)); le["active"] = [a for a in le.get("active", []) if a.get("task") != tid]
    json.dump(le, open(lep, "w"), indent=2, ensure_ascii=False)

# journal append (verbatim verdict or the failure note — never re-summarized)
jpath = os.path.join(jdir, f"{tid}.md")
def journal(note):
    with open(jpath, "a") as f:
        f.write(f"\n## attempt @ {time.strftime('%Y-%m-%dT%H:%M:%S%z')}\n")
        f.write(f"- killed={killed or 'no'} rc={rc}\n")
        f.write("```json\n" + note + "\n```\n")

outcome = None
if verdict is None:
    # malformed/no-verdict: distinguish empty (timeout/wedge) from garbage
    reason = "no-verdict" if (killed or not text.strip() or rc != 0) else "malformed-verdict"
    task["retry_count"] = task.get("retry_count", 0) + 1
    journal(json.dumps({"result": "blocked", "blocked_reason": reason,
                        "killed": killed, "rc": rc}, indent=2))
    if task["retry_count"] >= 2:
        task["status"] = "blocked"; task["blocked_reason"] = reason
        outcome = f"BLOCKED ({reason}, retries exhausted)"
    else:
        task["status"] = "ready"; task["blocked_reason"] = None
        outcome = f"RETRY ({reason}, attempt {task['retry_count']})"
else:
    journal(json.dumps(verdict, indent=2, ensure_ascii=False))
    task["last_verdict"] = verdict.get("result")
    task["commit_sha"] = verdict.get("commit_sha")
    res = (verdict.get("result") or "").lower()
    if res == "pass":
        # the Go gate (build+test+vet+lint) ran and passed INSIDE the performer
        # (ADR-0014/0019). Trust the deterministic Verdict; never re-judge.
        task["status"] = "done"; task["blocked_reason"] = None
        outcome = "DONE (verdict pass)"
    else:
        task["retry_count"] = task.get("retry_count", 0) + 1
        br = verdict.get("blocked_reason") or f"verdict={res}"
        if task["retry_count"] >= 2:
            task["status"] = "blocked"; task["blocked_reason"] = br
            outcome = f"BLOCKED ({br}, retries exhausted)"
        else:
            task["status"] = "ready"; task["blocked_reason"] = None
            outcome = f"RETRY ({br}, attempt {task['retry_count']})"

json.dump(d, open(lp, "w"), indent=2, ensure_ascii=False)
release_lease()
print(outcome)
PYEOF
)"

log "tick result task=$TASK_ID -> $RESULT (killed=${killed:-no}, rc=$rc)"

# refresh heartbeat (best-effort; reconcile + external alerter read it)
"$PYTHON" "$HB_SCRIPT" 2>/dev/null || true

if [ "$RESULT" = "AUTH_EXPIRED" ]; then
  log "AUTH EXPIRED — wrote $RUNTIME/AUTH_EXPIRED. Re-run setup-oauth.sh. Stopping (no kickstart)."
fi
exit 0
