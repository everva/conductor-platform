#!/usr/bin/env python3
"""
auto-reconcile.py — DETERMINISTIC reconciler for the conductor-platform builder.

Ported from xirigo's ~/.xirigo/auto-reconcile.py. This is the INDEPENDENT
backstop demanded by ADR-0016: it is NOT embedded in the builder tick loop, it
runs as its OWN launchd job every few minutes, so a wedged/dead builder can
still be recovered (a conductor cannot rescue its own death). NO LLM — pure
stdlib, idempotent (ADR-0016: deterministic base + backstop only; the LLM-advisor
gray-zone layer is Faz-1b).

Failure modes it fixes (xirigo-proven, adapted to the Go gate / JSON ledger):

  1. RECONCILE: a task is `running`/`ready` but a real `[task:<id>]` commit
     already exists on the build branch (the performer committed then crashed
     before the verdict was recorded) -> mark done. Conservative: ONLY when a
     commit exists; a bare commit is the deterministic merge proof (ADR-0004
     squash trailer). We do NOT re-run the gate here (no LLM, no heavy work).
  2. DEAD TICK: no conductor-tick.sh process AND heartbeat stale -> kickstart
     the launchd job (drop a stale lock first). Auth-aware: if AUTH_EXPIRED flag
     or "Not logged in" in the stream, do NOT kickstart into a login wall.
  3. ORPHANS: >1 conductor-tick.sh, or lockless performer claude trees -> kill.
  4. STALE LEASE: lease for a now-done/blocked task, or a lease whose owner PID
     is dead -> drop it (ADR-0010 reaper, single-host form).

Logs every action to ~/.conductor-platform-builder/auto-reconcile.log.
"""
import json
import os
import re
import subprocess
import time
from datetime import datetime, timezone

RT = os.path.expanduser("~/.conductor-platform-builder")
LEDGER = os.path.join(RT, "state.json")
LEASES = os.path.join(RT, "leases.json")
HB = os.path.join(RT, "heartbeat.json")
LOG = os.path.join(RT, "auto-reconcile.log")
AUTH_FLAG = os.path.join(RT, "AUTH_EXPIRED")
TICK_OUT = os.path.join(RT, "conductor-tick.out.jsonl")
UID = str(os.getuid())

# The launchd label of the builder tick job (must match the plist Label).
LAUNCHD_LABEL = "com.conductor-platform-builder"

# Repo + build branch for commit-based reconcile. CP_BUILDER_REPO overrides.
REPO = os.environ.get(
    "CP_BUILDER_REPO",
    os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")),
)
BUILD_BRANCH = os.environ.get("CP_BUILDER_BRANCH", "develop")

# Heartbeat staleness before we treat the tick as dead. The launchd job re-fires
# on its own StartInterval (default 300s), so a brief gap is normal and must NOT
# trigger a redundant kickstart.
DEAD_HB_MIN = 12


def sh(c, cwd=None):
    return subprocess.run(c, shell=True, capture_output=True, text=True,
                          cwd=cwd).stdout.strip()


def log(msg):
    line = f"{datetime.now(timezone.utc).isoformat()} | {msg}"
    try:
        with open(LOG, "a") as f:
            f.write(line + "\n")
    except Exception:
        pass
    return line


def committed_tasks():
    """Set of task ids with a real [task:<id>] commit on the build branch
    (ADR-0004 squash trailer). Last 300 commits is plenty."""
    found = set()
    if not os.path.isdir(os.path.join(REPO, ".git")):
        return found
    out = sh(f"git log {BUILD_BRANCH} --format=%s -300 2>/dev/null", cwd=REPO) \
        or sh("git log --format=%s -300 2>/dev/null", cwd=REPO)
    for m in re.findall(r"\[task:([^\]]+)\]", out):
        found.add(m.strip())
    return found


def pid_alive(pid):
    try:
        os.kill(int(pid), 0)
        return True
    except Exception:
        return False


def main():
    # PAUSE switch: graceful stop. Do nothing (no reconcile, no kickstart).
    if os.path.exists(os.path.join(RT, "PAUSE")):
        log("paused (PAUSE flag present) — skipping")
        return
    if not os.path.exists(LEDGER):
        log("no ledger yet (builder not seeded) — skipping")
        return

    actions = []
    d = json.load(open(LEDGER))
    tasks = d.get("tasks", [])
    by_id = {t["id"]: t for t in tasks}
    committed = committed_tasks()

    # --- 1. RECONCILE: committed but not marked done ---
    # Conservative: a real merge-trailer commit is the deterministic proof the
    # task landed (the performer ran the gate before committing, ADR-0014/0019).
    # Only flip running/ready -> done; never touch blocked/done.
    changed = False
    for t in tasks:
        if t["id"] in committed and t.get("status") in ("running", "ready", "todo"):
            t["status"] = "done"
            t["last_verdict"] = t.get("last_verdict") or "pass"
            t["blocked_reason"] = None
            changed = True
            actions.append(f"RECONCILE {t['id']} -> done ([task:{t['id']}] commit on {BUILD_BRANCH})")
    if changed:
        json.dump(d, open(LEDGER, "w"), indent=2, ensure_ascii=False)

    # --- 4. STALE LEASE: lease for a done/blocked task, or dead-PID owner ---
    if os.path.exists(LEASES):
        le = json.load(open(LEASES))
        active = le.get("active", [])
        keep = []
        for a in active:
            tid = a.get("task") or a.get("id")
            st = by_id.get(tid, {}).get("status")
            owner = a.get("pid")
            if st in ("done", "blocked"):
                actions.append(f"DROP stale lease {tid} (status={st})")
            elif owner and not pid_alive(owner):
                actions.append(f"DROP dead-owner lease {tid} (pid {owner} gone)")
                # the tick crashed mid-run; revert running -> ready so it retries
                tk = by_id.get(tid)
                if tk and tk.get("status") == "running":
                    tk["status"] = "ready"
                    json.dump(d, open(LEDGER, "w"), indent=2, ensure_ascii=False)
            else:
                keep.append(a)
        if len(keep) != len(active):
            le["active"] = keep
            json.dump(le, open(LEASES, "w"), indent=2, ensure_ascii=False)

    # --- 3. ORPHANS: extra tick scripts / lockless performer claude trees ---
    # BUILDER-SPECIFIC: xirigo also runs a `conductor-tick.sh`; a bare match would
    # treat the xirigo tick as an "extra" builder tick and kill it (cross-project hazard).
    ticks = sh("pgrep -f tools/builder/conductor-tick.sh").split()
    lock_pid = sh(f"cat {RT}/conductor.lock/pid 2>/dev/null")
    if len(ticks) > 1:
        for t in ticks:
            if t != lock_pid:
                sh(f"kill -TERM {t} 2>/dev/null")
                actions.append(f"KILL extra conductor-tick {t} (lock holder={lock_pid})")
    # lockless builder performer claude (reparented to init)
    for p in sh("pgrep -f 'claude -p .*CONDUCTOR-PLATFORM BUILDER PERFORMER'").split():
        ppid = sh(f"ps -o ppid= -p {p}").strip()
        if ppid == "1":
            sh(f"kill -TERM {p} 2>/dev/null")
            actions.append(f"KILL orphan performer claude {p} (parent=1)")

    # --- 2. DEAD TICK: no tick + heartbeat stale -> kickstart ---
    ticks = sh("pgrep -f tools/builder/conductor-tick.sh").split()  # re-read (builder-specific)
    try:
        hb = json.load(open(HB))
        hb_age = (datetime.now(timezone.utc)
                  - datetime.fromisoformat(hb["ts"])).total_seconds() / 60
    except Exception:
        hb_age = 999
    nondone = [t for t in tasks if t.get("status") not in ("done", "blocked")]
    if not ticks and nondone and hb_age > DEAD_HB_MIN:
        # auth check first — never kickstart into a login wall forever (ADR-0014)
        tail = sh(f"tail -c 4000 {TICK_OUT} 2>/dev/null")
        if os.path.exists(AUTH_FLAG) or "Not logged in" in tail:
            actions.append("AUTH EXPIRED — needs tools/builder/setup-oauth.sh (NOT kickstarting)")
        else:
            if os.path.isdir(os.path.join(RT, "conductor.lock")):
                sh(f"rm -rf {RT}/conductor.lock")
            # NO `-k`: plain kickstart starts a tick only if none runs; `-k` would
            # KILL a live long-running tick (a pgrep race could mis-fire this).
            sh(f"launchctl kickstart gui/{UID}/{LAUNCHD_LABEL}")
            actions.append(f"DEAD TICK (hb {int(hb_age)}min) -> kickstarted (no -k)")

    # summary
    done = sum(1 for t in tasks if t.get("status") == "done")
    running = [t["id"] for t in tasks if t.get("status") == "running"]
    if actions:
        for a in actions:
            log(a)
        log(f"-> done={done}/{len(tasks)} running={running}")
    else:
        log(f"idle (done={done}/{len(tasks)}, tick={'yes' if ticks else 'NO'})")


if __name__ == "__main__":
    main()
