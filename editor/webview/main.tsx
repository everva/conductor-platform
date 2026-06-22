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
// Selection (Faz-Q / Q0): `ForkApp` listens for host→webview `select` control messages (the
// native sessions-tree click path) and drives FleetDashboard.selection, and posts a
// `webview-ready` ping on mount so the host can flush a selection it buffered during the
// cold-start window. These control messages carry NO correlation id, so the bridge's own
// router ignores them (and they carry no token — only project/task ids). `select` with a `task`
// is the former N3 deep-link-to-session; without one it scopes the cockpit to the project.
//
// The CSP set by the host (extension.ts) is `connect-src 'none'`, so the bundle CANNOT do
// its own network — the bridge is the only data path. The pure factory wiring lives in
// connect.ts (unit-testable without React); the React-free guard `isSelect` lives in
// the bridge protocol module.
import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
// Design foundation (N5 CSS fix): the shared cockpit components style themselves with these
// design TOKENS (`:root` custom properties) + base styles — the web App imports the same in its
// entry (web/src/main.tsx). The `@cockpit` barrel pulls in the COMPONENT css but NOT this
// foundation, so without these imports the fork webview's hundreds of `var(--…)` resolve to
// nothing → a fully UNSTYLED cockpit.
//
// Faz-P/P1 (VS Code theme bridge): theme-vscode.css re-maps the cockpit's CHROME tokens (bg /
// surface / text / border / font / focus / scrollbar) onto the editor's `--vscode-*` theme, so
// the cockpit ADOPTS the user's editor theme (light / dark / high-contrast) instead of a fixed
// foreign dark. It is imported LAST so its `:root` redefinitions win over tokens.css AND its
// scrollbar rules win over index.css; every map keeps the web token as a fallback. The standalone
// web App never imports it, so its branded look is untouched. (Honoring --vscode-font-family also
// makes the Geist webfont an optional extra, not a requirement — the editor's UI font is used.)
import "../../web/src/theme/tokens.css";
import "../../web/src/index.css";
import "./theme-vscode.css";
import { createBridgeTransports, subscribeToMessages } from "../src/bridge/webviewTransport";
import { isSelect } from "../src/bridge/protocol";
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
 * The fork cockpit app. Holds the host-owned selection (Faz-Q / Q0): it subscribes to host→webview
 * `select` control messages and drives FleetDashboard.selection, and announces `webview-ready` on
 * mount so the host flushes any buffered selection. A NEW selection object per message means a
 * repeat selection (same ids) still re-fires the dashboard's effect. `task` may be absent (select
 * a project) or present (also open that task's session — the former deep-link).
 */
function ForkApp(): React.JSX.Element {
  const [selection, setSelection] = useState<{ project: string; task?: string } | null>(null);

  useEffect(() => {
    const unsubscribe = subscribeToMessages((msg) => {
      if (isSelect(msg)) {
        setSelection({ project: msg.project, ...(msg.task !== undefined ? { task: msg.task } : {}) });
      }
    });
    // Tell the host we're mounted + listening (flush any selection buffered at cold start).
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
      selection={selection}
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
