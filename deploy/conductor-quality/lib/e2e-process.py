#!/usr/bin/env python3
"""Process Playwright JSON reporter output: summarize pass/fail, log, and (in --file
mode) convert fresh failures to intake fix-tasks (deduped + capped).
  argv: <project> <results.json> <log_file> <mode:report|--file> <artifact_dir>
"""
import sys, os, json, hashlib, urllib.request

project, results_f, log_f, mode, artdir = sys.argv[1:6]
CAP = 5
state_f = os.path.expanduser(f"~/conductor-agent/e2e-filed-{project}.txt")

def log(msg):
    with open(log_f, "a") as f:
        f.write(f"e2e[{project}]: {msg}\n")

try:
    data = json.load(open(results_f))
except Exception as e:
    log(f"results parse failed: {e}")
    print(f"e2e[{project}]: results unreadable ({e}) — run likely crashed before JSON")
    sys.exit(0)

# walk suites → specs
fails = []
passed = 0
def walk(suite):
    global passed
    for sp in suite.get("specs", []):
        ok = sp.get("ok", True)
        if ok:
            passed += 1
        else:
            # first failing test's error
            err = ""
            for t in sp.get("tests", []):
                for r in t.get("results", []):
                    if r.get("status") not in ("passed", "skipped"):
                        err = (r.get("error", {}) or {}).get("message", "") or r.get("status", "failed")
                        break
                if err:
                    break
            fails.append({"file": sp.get("file", suite.get("file", "?")),
                          "line": sp.get("line", 0),
                          "title": sp.get("title", "?"),
                          "error": err})
    for s in suite.get("suites", []):
        walk(s)

for s in data.get("suites", []):
    walk(s)

stats = data.get("stats", {})
total = passed + len(fails)
# NEVER fake-green: a run that executed 0 tests is a CRASH/misconfig, not success.
if total == 0:
    log("ERROR 0 tests executed — run crashed / misconfigured (NOT green)")
    print(f"e2e[{project}]: ERROR — 0 tests ran (crash/misconfig, NOT green). See {artdir}")
    sys.exit(2)
print(f"e2e[{project}]: {passed}/{total} passed, {len(fails)} FAILED  (screenshots: {artdir})")
log(f"RESULT {passed}/{total} passed, {len(fails)} failed, artifacts {artdir}")
for fd in fails:
    log(f"  FAIL {fd['file']}:{fd['line']} — {fd['title']} :: {fd['error'][:160]}")

if not fails:
    print("  all green — visual+functional verification passed")
    sys.exit(0)
for fd in fails[:8]:
    print(f"  FAIL {fd['file']} — {fd['title']}")

if mode != "--file":
    print("  (report mode — not filing fix-tasks)")
    sys.exit(0)

# --file: only file failures CONFIRMED across TWO consecutive runs. A flaky or staging-
# DATA-dependent failure (e.g. a row that isn't seeded this run) doesn't reproduce, so it
# never becomes a fix-task the agent can't fix — this kills the A-E2E block-storm. A genuine
# code defect fails every run and gets filed on the 2nd sighting. (cron cadence = 6h, so
# confirmation latency is ~6h — an acceptable trade for precision.)
open(state_f, "a").close()
seen = set(open(state_f).read().split())          # already-filed fids
pending_f = os.path.expanduser(f"~/conductor-agent/e2e-pending-{project}.txt")
open(pending_f, "a").close()
pending = set(open(pending_f).read().split())     # fids that FAILED the previous run
def fid(fd):
    return hashlib.sha1(f"{fd['file']}|{fd['title']}".encode()).hexdigest()[:10]
# this run's failures (dict dedups the chromium+mobile-chrome double-fail by fid)
cur_fail = {}
for fd in fails:
    cur_fail[fid(fd)] = fd
# next run's pending baseline = this run's failures (a failure must RECUR to be confirmed)
with open(pending_f, "w") as f:
    for h in cur_fail:
        f.write(h + "\n")
# confirmed = failed BOTH this run and last run, not already filed
confirmed = [(h, fd) for h, fd in cur_fail.items() if h in pending and h not in seen]
observed_new = [h for h in cur_fail if h not in pending and h not in seen]
if observed_new:
    log(f"observed {len(observed_new)} NEW failure(s) — awaiting 2nd-run confirmation, NOT filed (flaky-guard): {observed_new}")
    print(f"  {len(observed_new)} new failure(s) observed — awaiting confirmation (flaky/data-dependent guard)")
fresh = confirmed[:CAP]
if not fresh:
    print("  no CONFIRMED (2-run reproducible) failures to file")
    sys.exit(0)
print(f"  {len(fresh)} CONFIRMED reproducible failure(s) → filing")

import re
_ANSI = re.compile(r"\x1b\[[0-9;]*[a-zA-Z]")
def esc(s):
    s = _ANSI.sub("", str(s))                    # strip ANSI colour codes
    s = "".join(c if ord(c) >= 32 else " " for c in s)  # drop control chars (incl \n)
    return s.replace('"', "'").strip()[:300]
docs = []
for h, fd in fresh:
    tid = f"A-E2E-{h}"
    scr = fd["file"].split("/")[-1].replace(".spec.ts", "")
    docs.append(
        f"id: {tid}\n"
        f"title: \"[e2e] failing smoke — {esc(scr)}\"\n"
        f"lane: redesign\ntier: T2\ndeps: []\n"
        f"acceptance:\n"
        f"  - \"[E2E FIX] The real-backend Playwright smoke for this screen FAILS. Make the screen pass its OWN existing spec ({esc(fd['file'])}). Keep design 1:1; fix the defect the test exposes.\"\n"
        f"  - \"FAILING TEST: {esc(fd['title'])}\"\n"
        f"  - \"ERROR: {esc(fd['error'])}\"\n"
        f"  - \"Run `npm run test:e2e -- {esc(fd['file'])}` locally until green. The verify gate (typecheck/lint/vitest/i18n/honesty/fabricated-data) must also stay green.\"\n"
        f"hidden_holdout_ref: \"store://holdouts/{project}/{tid}\"\n"
    )
yaml = "\n---\n".join(docs)
tok = ""
for line in open(os.path.expanduser("~/conductor-agent/conductor-agent.env")):
    if line.startswith("CONDUCTOR_AGENT_TOKEN="):
        tok = line.split("=", 1)[1].strip()
req = urllib.request.Request(
    f"http://conductor-api:8080/projects/{project}/intake",
    data=yaml.encode(), method="POST",
    headers={"Authorization": f"Bearer {tok}", "Content-Type": "application/x-yaml"})
try:
    body = json.loads(urllib.request.urlopen(req, timeout=30).read())
    created = body.get("created", [])
    log(f"FILED {len(created)} e2e fix-task(s): {created}")
    print(f"  FILED {len(created)} e2e fix-task(s): {created}")
    with open(state_f, "a") as f:
        for h, _ in fresh:
            f.write(h + "\n")
except Exception as e:
    log(f"intake POST failed: {e}")
    print(f"  intake POST failed: {e}")
