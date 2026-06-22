// Fork webview React entry — mounts the 3B cockpit over the 4B-2 postMessage bridge
// (Faz-4 DALGA 4B, 4B-3) + the N3 host→webview deep-link control channel.
//
// This is the SECOND consumer of the shared cockpit barrel (web/src/cockpit.ts, the
// FIRST being the web App). It runs in FORK MODE (ADR-0027/0028):
//   * NO token in the webview — auth is entirely host-side (SecretStorage). We mount with
//     token="" and inject transports that route every REST/WS call to the extension host
//     over postMessage (4B-2), which attaches the bearer / ?token= and does the real I/O.
//   * The mere presence of `eventTransport` puts FleetDashboard in fork mode (4A-2): it
//     mounts enabled even with the empty token and runs the live stream over the bridge.
//
// N3 (deep-link): `ForkApp` listens for host→webview `navigate-session` control messages
// (the sessions-tree click path) and drives FleetDashboard.navigateTo, and posts a
// `webview-ready` ping on mount so the host can flush a navigate it buffered during the
// cold-start window. These control messages carry NO correlation id, so the bridge's own
// router ignores them (and they carry no token — only project/task ids).
//
// The CSP set by the host (extension.ts) is `connect-src 'none'`, so the bundle CANNOT do
// its own network — the bridge is the only data path. The pure factory wiring lives in
// connect.ts (unit-testable without React); the React-free guard `isHostNavigate` lives in
// the bridge protocol module.
import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { createBridgeTransports, subscribeToMessages } from "../src/bridge/webviewTransport";
import { isHostNavigate } from "../src/bridge/protocol";
import { forkClientFactories } from "./connect";
import { FleetDashboard, ApiClient } from "@cockpit";

// The VS Code webview api (declared in types.d.ts). The only channel to the host.
const vscodeApi = acquireVsCodeApi();

// 4B-2 transports: post via the vscode api; subscribe to host→webview messages via the
// shared `subscribeToMessages` seam (the real window "message" wiring — one definition).
const { http, events } = createBridgeTransports(
  { postMessage: (m) => vscodeApi.postMessage(m) },
  subscribeToMessages,
);

// Five zero-arg client factories over the one HTTP transport (each ignores the empty token —
// the host owns auth). makeScenarioClient (N3) lets a deep-linked SessionView fetch its spec
// over the bridge (the CSP blocks a direct fetch). Built via the React-free helper so the
// wiring is itself unit-testable.
const { makeClient, makeControlClient, makeIntakeClient, makeHistory, makeScenarioClient } =
  forkClientFactories(ApiClient, http);

/**
 * The fork cockpit app. Holds the N3 deep-link target: it subscribes to host→webview
 * `navigate-session` control messages and drives FleetDashboard.navigateTo, and announces
 * `webview-ready` on mount so the host flushes any buffered navigate. A NEW navigate object
 * per message means a repeat deep-link (same ids) still re-fires the dashboard's effect.
 */
function ForkApp(): React.JSX.Element {
  const [navigateTo, setNavigateTo] = useState<{ project: string; task: string } | null>(null);

  useEffect(() => {
    const unsubscribe = subscribeToMessages((msg) => {
      if (isHostNavigate(msg)) {
        setNavigateTo({ project: msg.project, task: msg.task });
      }
    });
    // Tell the host we're mounted + listening (flush any navigate buffered at cold start).
    vscodeApi.postMessage({ kind: "webview-ready" });
    return unsubscribe;
  }, []);

  return (
    <FleetDashboard
      token=""
      makeClient={makeClient}
      makeControlClient={makeControlClient}
      makeIntakeClient={makeIntakeClient}
      makeHistory={makeHistory}
      makeScenarioClient={makeScenarioClient}
      eventTransport={events}
      navigateTo={navigateTo}
      onUnauthorized={() => {
        // FORK MODE: the host owns auth, so the webview can't re-prompt. A 401 here means a
        // stored token went stale; surfacing it to the host is a later step. Log it (no token).
        console.warn("conductor webview: gateway returned 401 (host owns auth)");
      }}
    />
  );
}

const root = document.getElementById("root");
if (root === null) {
  throw new Error("conductor webview: #root element is missing");
}

createRoot(root).render(<ForkApp />);
