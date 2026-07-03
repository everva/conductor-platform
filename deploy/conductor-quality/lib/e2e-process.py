#!/usr/bin/env python3
"""Process Playwright JSON reporter output: summarize pass/fail, log, and (in --file
mode) convert fresh failures to intake fix-tasks (deduped + capped).
  argv: <project> <results.json> <log_file> <mode:report|--file> <artifact_dir>
"""
import sys, json, hashlib, urllib.request

project, results_f, log_f, mode, artdir = sys.argv[1:6]
CAP = 5
state_f = f"/home/davinci/conductor-agent/e2e-filed-{project}.txt"

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

# --file: fresh failures → fix-tasks
open(state_f, "a").close()
seen = set(open(state_f).read().split())
def fid(fd):
    return hashlib.sha1(f"{fd['file']}|{fd['title']}".encode()).hexdigest()[:10]
# dedup WITHIN this batch too: the same spec+title fails once per browser project
# (chromium + mobile-chrome) → identical fid → duplicate task id → intake 400.
fresh, batch_seen = [], set()
for fd in fails:
    h = fid(fd)
    if h in seen or h in batch_seen:
        continue
    batch_seen.add(h)
    fresh.append((h, fd))
    if len(fresh) >= CAP:
        break
if not fresh:
    print("  no fresh failures to file (all already filed)"); sys.exit(0)

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
for line in open("/home/davinci/conductor-agent/conductor-agent.env"):
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
