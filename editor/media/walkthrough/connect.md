## Connect to your gateway

Conductor talks to your **gateway** (the control plane) over an authenticated link. Run
**Conductor: Connect to Gateway**, paste your API token, and the status bar shows
`$(plug) Conductor: connected`.

- Set the gateway URL once in **Settings → `conductor.gatewayUrl`** (the token is **never**
  stored there — it lives in VS Code SecretStorage).
- Drops are healed automatically (you'll see `reconnecting` in the status bar, then `connected`).

Once connected, your projects appear in the **Conductors** view (activity bar).
