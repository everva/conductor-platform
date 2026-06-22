# conductor-agent — gateway-mediated host-agent (runbook)

The **production** path for running real conductor work on a performer host (davinci)
against the live hetzner control-plane, with **held-for-review** governance (every
merge waits for your approval). Architecture: ADR-0048. The host-agent talks ONLY to
the conductor-api gateway over HTTP (tailscale) and **never opens Postgres** — all DB
stays in k8s.

```
davinci (host-agent)  ──HTTP/tailscale──▶  conductor-api gateway (k8s)  ──▶  Postgres (k8s)
  claude + git + gates                      lease / verdict / approve            (DB never on hosts)
```

## ✅ Already done (autonomous, this session)
- **Gateway agent-API** (lease / heartbeat / report / result / decision / merged /
  release) — built, gate+CI green, and **LIVE in production** (auto-deployed via GitOps;
  verified: `POST /agent/heartbeat` returns the agent-API handler's response).
- **conductor-agent host binary** built (orchestration + client fully tested) and
  **cross-compiled for davinci** (linux/amd64) → copied to `davinci:~/conductor-agent/`.
- **optiway onboarded** to the gateway: project `optiway`, base `conductor/optiway`,
  governance empty → **held-for-review** (fail-safe). optiway `main` is never touched.
- **davinci → gateway reachability confirmed** over tailscale (`http://conductor-api:8080`).

## ▶ You do (needs your credentials / decisions)
The remaining steps need an interactive `claude` login, your GitHub token, and your
approval — none of which can be done autonomously.

### 1. Log in to claude on davinci
```bash
ssh davinci
claude            # complete the interactive subscription login (browser/oauth)
claude -p 'print OK'   # confirm it works headlessly
```

### 2. Install the optiway toolchain on davinci (for the verify gate)
The gate runs optiway's real build/lint/test, so davinci needs the toolchain:
```bash
ssh davinci
cd ~/optiway-conductor && git checkout conductor/optiway && git pull
corepack enable && pnpm install        # or your usual bootstrap
```

### 3. Commit the recipe + first scenario to `conductor/optiway`
The performer + gate read these **from the checkout**, so they must live in the repo on
the base branch. Drafts are in this directory:
```bash
cd ~/optiway-conductor && git checkout conductor/optiway
mkdir -p .conductor/scenarios
cp ~/conductor-agent/optiway-recipe.config.yaml   .conductor/config.yaml
cp ~/conductor-agent/optiway-scenario-SMOKE-1.yaml .conductor/scenarios/SMOKE-1.yaml
# REVIEW the recipe gates (scope with --filter for speed; the gate is the sole merge authority).
git add .conductor && git commit -m "conductor: recipe + SMOKE-1 scenario" && git push origin conductor/optiway
```

### 4. Fill the agent env (secrets) on davinci
```bash
cd ~/conductor-agent
cp conductor-agent.env.example conductor-agent.env && chmod 600 conductor-agent.env
# CONDUCTOR_AGENT_TOKEN: kubectl --context hetzner-k8s-1 -n conductor get secret conductor-secret -o jsonpath='{.data.CONDUCTOR_API_TOKEN}' | base64 -d
# CONDUCTOR_GH_TOKEN:    a PAT (or `gh auth token`) with Contents:read+write on everva/optiway
$EDITOR conductor-agent.env
```

### 5. Create the task in the gateway (intake the scenario)
The committed scenario also needs a task row in the gateway. From a machine with
kubectl + the token (e.g. your Mac), port-forward and intake:
```bash
TOKEN=$(kubectl --context hetzner-k8s-1 -n conductor get secret conductor-secret -o jsonpath='{.data.CONDUCTOR_API_TOKEN}' | base64 -d)
kubectl --context hetzner-k8s-1 -n conductor port-forward svc/conductor-api 18080:8080 &
curl -s -X POST -H "Authorization: Bearer $TOKEN" --data-binary @optiway-scenario-SMOKE-1.yaml \
  -H "Content-Type: text/plain" http://localhost:18080/projects/optiway/intake
# → {"created":["SMOKE-1"], ...}
```

### 6. Start the agent
```bash
ssh davinci && cd ~/conductor-agent
# Foreground (first run, to watch logs):
set -a && . ./conductor-agent.env && set +a
./conductor-agent -gateway "$CONDUCTOR_GATEWAY" -project "$CONDUCTOR_PROJECT" \
  -repo "$CONDUCTOR_REPO" -base "$CONDUCTOR_BASE" -recipe-dir "$CONDUCTOR_RECIPE_DIR" \
  -capabilities "$CONDUCTOR_CAPABILITIES" -root "$CONDUCTOR_ROOT"
# …or install the service: sudo cp conductor-agent.service /etc/systemd/system/ &&
#    sudo systemctl daemon-reload && sudo systemctl enable --now conductor-agent
```

### 7. Watch → review → approve
The agent leases SMOKE-1 → clones conductor/optiway → develops (claude) → runs the gate
→ **holds for review** (status `awaiting-approval`). Watch + approve via:
- **Conductor Editor** (Command Center / Events / the held task), or
- **conductorctl / curl**:
  ```bash
  curl -s -H "Authorization: Bearer $TOKEN" http://localhost:18080/projects/optiway/tasks   # SMOKE-1 awaiting-approval
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{"task_id":"SMOKE-1"}' http://localhost:18080/projects/optiway/approve
  ```
On approve, the agent re-verifies against base drift, squash-merges to `conductor/optiway`
(pushed), and reports done. **main is never touched.** Reject instead by leaving it held
or `POST /projects/optiway/abort`.

## Notes
- **Held-for-review** is the fail-safe default (empty governance policy → every task held).
  To change a project to tier-based or auto, set its `GovernancePolicy` ("risk-layered" /
  "auto") — held is recommended for a live product like optiway.
- The agent restarts cleanly (systemd `Restart=always`); a held task waits across restarts
  but is re-merged-on-approval only while an agent is actively polling it (start the agent
  before approving, or keep it running).
- Holdout injection (ADR-0018) is not served by the gateway yet (Faz G3); the gate runs
  optiway's real build/lint/test, which is the merge authority. Add the hidden holdout
  later for defense-in-depth.
- Token discipline: the bearer + gh tokens live only in `conductor-agent.env` (chmod 600)
  and the Authorization header; they are never logged and are stripped from the performer
  subprocess env.
