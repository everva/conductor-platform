#!/usr/bin/env python3
"""Process the auditor claude's raw output: extract JSON findings, log, dedup, and
(in --file mode) convert fresh P0/P1 findings to intake fix-tasks (capped).
  stdin: raw claude output
  argv:  <project> <state_file> <log_file> <mode:report|--file> <cap> <ts> <base>
"""
import sys, re, json, hashlib, subprocess, urllib.request

project, state_f, log_f, mode, cap, ts, base = sys.argv[1:8]
cap = int(cap)
raw = sys.stdin.read()

def log(msg):
    with open(log_f, "a") as f:
        f.write(f"{ts} auditor[{project}]: {msg}\n")

# --- extract the JSON object (last {"findings":...} in the output) ---
m = re.findall(r'\{"findings".*\}', raw, re.S)
if not m:
    log(f"NO JSON in output ({len(raw)} chars) — claude may have refused/errored")
    print(f"auditor[{project}]: no parseable findings")
    sys.exit(0)
try:
    data = json.loads(m[-1])
    findings = data.get("findings", [])
except Exception as e:
    log(f"JSON parse error: {e}")
    print(f"auditor[{project}]: JSON parse error")
    sys.exit(0)

if not findings:
    log("0 findings (clean)")
    print(f"auditor[{project}]: 0 findings — clean")
    sys.exit(0)

seen = set(open(state_f).read().split()) if state_f else set()
def fid(fd):
    return hashlib.sha1(f"{fd.get('file')}|{fd.get('class')}|{fd.get('problem')}".encode()).hexdigest()[:10]

# log every finding
by_sev = {}
fresh = []
for fd in findings:
    h = fid(fd)
    sev = fd.get("severity", "P?")
    by_sev[sev] = by_sev.get(sev, 0) + 1
    dup = " [already-filed]" if h in seen else ""
    log(f"{sev} {fd.get('class')} {fd.get('file')}:{fd.get('line')} — {fd.get('problem')}{dup}")
    if h not in seen and sev in ("P0", "P1"):
        fresh.append((h, fd))

summary = ", ".join(f"{k}:{v}" for k, v in sorted(by_sev.items()))
print(f"auditor[{project}]: {len(findings)} findings ({summary}); {len(fresh)} fresh P0/P1")

if mode != "--file":
    print("  (report mode — not filing fix-tasks)")
    for h, fd in fresh[:cap]:
        print(f"  WOULD FILE: {fd.get('severity')} {fd.get('file')}:{fd.get('line')} {fd.get('class')}")
    sys.exit(0)

# --- --file mode: build one intake YAML for the capped fresh findings, POST it ---
fresh = fresh[:cap]
if not fresh:
    print("  no fresh P0/P1 to file"); sys.exit(0)

def esc(s):
    return str(s).replace('"', "'").replace("\n", " ").strip()[:300]

docs = []
for h, fd in fresh:
    tid = f"A-AUDIT-{fd.get('class','x')}-{h}"[:60]
    docs.append(
        f"id: {tid}\n"
        f"title: \"[auditor] {esc(fd.get('class'))} — {esc(fd.get('screen'))}\"\n"
        f"lane: redesign\ntier: T2\ndeps: []\n"
        f"acceptance:\n"
        f"  - \"[AUDITOR FIX] Independent audit found a {esc(fd.get('class'))} defect on an already-landed screen. Keep the design 1:1; fix ONLY the defect. Do not restyle.\"\n"
        f"  - \"DEFECT ({esc(fd.get('severity'))}): {esc(fd.get('file'))}:{fd.get('line')} — {esc(fd.get('problem'))}\"\n"
        f"  - \"FIX: {esc(fd.get('fix'))}\"\n"
        f"  - \"The verify gate HARD-FAILS on honesty + i18n-render + fabricated-data; make all PASS. Commit only when typecheck + tests + i18n green.\"\n"
        f"hidden_holdout_ref: \"store://holdouts/{project}/{tid}\"\n"
    )
yaml = "\n---\n".join(docs)

# gateway token
tok = ""
for line in open("/home/davinci/conductor-agent/conductor-agent.env"):
    if line.startswith("CONDUCTOR_AGENT_TOKEN="):
        tok = line.split("=", 1)[1].strip()
req = urllib.request.Request(
    f"http://conductor-api:8080/projects/{project}/intake",
    data=yaml.encode(), method="POST",
    headers={"Authorization": f"Bearer {tok}", "Content-Type": "application/x-yaml"})
try:
    resp = urllib.request.urlopen(req, timeout=30)
    body = json.loads(resp.read())
    created = body.get("created", [])
    log(f"FILED {len(created)} fix-task(s): {created}")
    print(f"  FILED {len(created)} fix-task(s): {created}")
    with open(state_f, "a") as f:
        for h, _ in fresh:
            f.write(h + "\n")
except Exception as e:
    log(f"intake POST failed: {e}")
    print(f"  intake POST failed: {e}")
