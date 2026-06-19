// Fork webview React entry — mounts the 3B cockpit over the 4B-2 postMessage bridge
// (Faz-4 DALGA 4B, 4B-3).
//
// This is the SECOND consumer of the shared cockpit barrel (web/src/cockpit.ts, the
// FIRST being the web App). It runs in FORK MODE (ADR-0027/0028):
//   * NO token in the webview — auth is entirely host-side (SecretStorage). We mount with
//     token="" and inject transports that route every REST/WS call to the extension host
//     over postMessage (4B-2), which attaches the bearer / ?token= and does the real I/O.
//   * The mere presence of `eventTransport` puts FleetDashboard in fork mode (4A-2): it
//     mounts enabled even with the empty token and runs the live stream over the bridge.
//
// The CSP set by the host (extension.ts) is `connect-src 'none'`, so the bundle CANNOT do
// its own network — the bridge is the only data path. main.tsx stays THIN: it acquires the
// vscode api, builds the bridge transports, and renders. The pure factory wiring lives in
// connect.ts (unit-testable without React).
import { createRoot } from "react-dom/client";
import { createBridgeTransports, subscribeToMessages } from "../src/bridge/webviewTransport";
import { forkClientFactories } from "./connect";
import { FleetDashboard, ApiClient } from "@cockpit";

// The VS Code webview api (declared in types.d.ts). The only channel to the host.
const vscodeApi = acquireVsCodeApi();

// 4B-2 transports: post via the vscode api; subscribe to host→webview messages via the
// shared `subscribeToMessages` seam (the real window "message" wiring — one definition,
// reused here instead of re-rolled, so the seam's removeEventListener isn't dead code).
const { http, events } = createBridgeTransports(
  { postMessage: (m) => vscodeApi.postMessage(m) },
  subscribeToMessages,
);

// Three zero-arg client factories over the one HTTP transport. Each ignores the (empty)
// token — the host owns auth — so they're assignable to the dashboard's (token) => …
// props. Built via the React-free helper so the wiring is itself unit-testable.
const { makeClient, makeControlClient, makeIntakeClient } = forkClientFactories(ApiClient, http);

const root = document.getElementById("root");
if (root === null) {
  throw new Error("conductor webview: #root element is missing");
}

createRoot(root).render(
  <FleetDashboard
    token=""
    makeClient={makeClient}
    makeControlClient={makeControlClient}
    makeIntakeClient={makeIntakeClient}
    eventTransport={events}
    onUnauthorized={() => {
      // FORK MODE: the host owns auth, so the webview can't re-prompt. A 401 here means a
      // stored token went stale; surfacing it to the host (a postMessage → re-auth flow)
      // is a later step (4C-3). For 4B-3, log it (no token is involved — safe to warn).
      console.warn("conductor webview: gateway returned 401 (host owns auth)");
    }}
  />,
);
