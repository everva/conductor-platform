#!/usr/bin/env python3
"""
write-heartbeat.py — compact builder snapshot to
~/.conductor-platform-builder/heartbeat.json.

Ported from xirigo's write-heartbeat.py. Run by the builder tick at the end of
each fire. An independent external alerter (Faz-1b, or a manual cron) can read
ONLY ~/.conductor-platform-builder/ (never ~/Documents), sidestepping macOS TCC,
and surface a STALL if this snapshot goes stale (ADR-0016 independent backstop).

Pure stdlib. Defensive: logs to heartbeat.log, never crashes the caller.
"""
import json
import os
import subprocess
from datetime import datetime, timezone

RT = os.path.expanduser("~/.conductor-platform-builder")
LEDGER = os.path.join(RT, "state.json")
OUT = os.path.join(RT, "heartbeat.json")
LOG = os.path.join(RT, "heartbeat.log")

REPO = os.environ.get(
    "CP_BUILDER_REPO",
    os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")),
)
BUILD_BRANCH = os.environ.get("CP_BUILDER_BRANCH", "develop")


def log(m: str) -> None:
    try:
        os.makedirs(RT, exist_ok=True)
        with open(LOG, "a") as f:
            f.write(f"{datetime.now(timezone.utc).isoformat()} {m}\n")
    except Exception:
        pass


def git(args, cwd):
    try:
        return subprocess.run(["git"] + args, cwd=cwd, capture_output=True,
                              text=True, timeout=20).stdout.strip()
    except Exception:
        return ""


def main() -> None:
    try:
        os.makedirs(RT, exist_ok=True)
        with open(LEDGER) as f:
            d = json.load(f)
        tasks = d.get("tasks", [])
        by = {}
        for t in tasks:
            by[t.get("status", "?")] = by.get(t.get("status", "?"), 0) + 1
        total = len(tasks)
        done = by.get("done", 0)
        head = ""
        if os.path.isdir(os.path.join(REPO, ".git")):
            head = git(["log", "-1", "--format=%h %s (%cr)", BUILD_BRANCH], REPO) \
                or git(["log", "-1", "--format=%h %s (%cr)"], REPO)
        snap = {
            "ts": datetime.now(timezone.utc).isoformat(),
            "done": done, "total": total,
            "pct": int(100 * done / total) if total else 0,
            "by": by,
            "running": sorted(t["id"] for t in tasks if t.get("status") == "running"),
            "blocked": sorted(t["id"] for t in tasks if t.get("status") == "blocked"),
            "head": head,
            "auth_expired": os.path.exists(os.path.join(RT, "AUTH_EXPIRED")),
            "all_done": done == total and total > 0,
        }
        tmp = OUT + ".tmp"
        with open(tmp, "w") as f:
            json.dump(snap, f)
        os.replace(tmp, OUT)  # atomic
        log(f"heartbeat written: {done}/{total}")
    except Exception as e:
        log(f"ERROR: {e}")


if __name__ == "__main__":
    main()
