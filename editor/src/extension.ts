// Conductor Platform — VS Code extension entry (Faz-4 DALGA 4B).
//
// SCOPE: 4B-0 contributed the "Conductor" activity-bar view container + a placeholder
// webview + a command skeleton. 4B-1 wires the gateway CONNECTION + AUTH: the
// `conductor.connect` flow prompts for a token, validates it against the gateway, and
// stores it in VS Code SecretStorage; `conductor.disconnect` forgets it; a status-bar
// item mirrors the connection state; restore-on-activate re-validates a stored token.
// 4B-2 added the host↔webview postMessage bridge (the fork transport seam). 4B-3 loads
// the SHARED 3B cockpit bundle into the Fleet view: the provider now serves a strict-CSP
// HTML page that nonce-loads dist/webview/main.js (the React bundle built from
// web/src/cockpit.ts via esbuild, ADR-0029) over that live bridge — the panels render
// real data, token-free (auth host-side).
//
// TOKEN DISCIPLINE (HARD): the token is read via a password input, handed straight to
// the ConnectionManager (SecretStorage + the gateway Authorization header), and is
// NEVER logged, shown in a message, or passed to a webview. User-facing messages carry
// only the gateway URL and enum-keyed reasons.
//
// TESTABILITY: `activate` stays thin; the command flows are extracted into `runConnect`
// /`runDisconnect` and the wiring into `registerConductor`, all written against a
// narrow structural `VscodeApi` slice so vitest drives them with a mock — headlessly,
// no electron, no display. The webview HTML is the pure `webviewHtml`.
import * as vscode from "vscode";
import { randomBytes } from "node:crypto";
import { deriveWsUrl, makeGatewayProbe, normalizeBaseUrl, GATEWAY_TOKEN_KEY } from "./gateway";
import { ConnectionManager, type ConnectionState, CLAUDE_OAUTH_TOKEN_KEY } from "./connection";
import { CredentialClient, type CredentialResult } from "./credentialClient";
import { HostBridge, type WebviewLike } from "./bridge/hostBridge";
import { isWebviewReady } from "./bridge/protocol";
import { wsConnector } from "./bridge/wsConnector";
import { ControlClient, ControlListError, type ControlAction } from "./controlClient";
import {
  TIER_CHOICES,
  parseTier,
  slugifyId,
  defaultHoldoutRef,
  parseAcceptance,
  buildIntakeYaml,
  dispatchOutcome,
  type DispatchSpec,
} from "./dispatch";
import { InterventionNotifier, type Intervention } from "./notifier";
import { DiffObserver, type TaskDiff } from "./diffObserver";
import { reconstructDiffFiles } from "./diffReconstruct";
import { DiffContentClient, type FullTaskDiff } from "./diffContentClient";
import { FleetReadClient, type UsageSnapshot } from "./fleetReadClient";
import { SessionsTreeProvider, SESSIONS_VIEW_ID, nodeProjectId, nodeTaskRef, reviewBadgeValue } from "./sessionsTree";
import { EventsWatcher } from "./eventsWatcher";
import { ReconnectController } from "./reconnect";
import { EventsTreeProvider, EVENTS_VIEW_ID, SHOW_EVENT_JSON_COMMAND } from "./eventsTree";
import type { FeedEvent } from "./eventsWatcher";
import { DiagnosticsTreeProvider, DIAGNOSTICS_VIEW_ID } from "./diagnosticsTree";
import { buildDiagnosticRows, type DiagSnapshot } from "./diagnostics";
import { ActivityTreeProvider, ACTIVITY_VIEW_ID } from "./activityTree";
import { summarizeActivity, activityNowText } from "./activity";

/** Command id for the gateway-connect action. */
export const CONNECT_COMMAND = "conductor.connect";

/** Command id for the gateway-disconnect action. */
export const DISCONNECT_COMMAND = "conductor.disconnect";

/** Command id for the editor-mediated claude login (Faz L1 — ADR-0049). Mints + stores the
 * portable claude OAuth token so the user no longer SSHes into the performer host to run an
 * interactive `claude` login (the xirigo model). */
export const LOGIN_COMMAND = "conductor.login";

/** Command id that forgets the stored claude OAuth token (Faz L1). */
export const LOGOUT_COMMAND = "conductor.logout";

/** Command id that uploads the stored claude OAuth token to the gateway's encrypted credential
 * store (Faz L3 — ADR-0049), so every conductor agent fetches it (the editor = single login). */
export const PUSH_CREDENTIAL_COMMAND = "conductor.pushCredential";

/** Command id that removes the gateway-stored claude credential (Faz L3 logout propagation). */
export const REMOVE_CREDENTIAL_COMMAND = "conductor.removeCredential";

/** The credential "kind" the editor uploads + the agent fetches. It is the env var name the
 * headless performer (`claude -p`) reads, kept in lockstep with the agent's
 * claudeCredentialKind and the gateway path segment. */
export const CLAUDE_CREDENTIAL_KIND = "CLAUDE_CODE_OAUTH_TOKEN";

/** The Claude Code CLI subcommand that mints a portable, long-lived OAuth token: it prints the
 * token to the terminal and SAVES NOTHING (the xirigo model). We run it in an integrated
 * terminal so the user completes the browser OAuth and copies the printed token; we NEVER
 * capture its stdout (that would put the account-level token in a buffer/log) — the user pastes
 * it into a masked input. The official env var the headless performer reads is
 * CLAUDE_CODE_OAUTH_TOKEN (consumed in Faz L2). */
export const CLAUDE_SETUP_TOKEN_CMD = "claude setup-token";

/** Command id for the pause-project control action (4C-2). */
export const PAUSE_COMMAND = "conductor.pause";

/** Command id for the resume-project control action (4C-2). */
export const RESUME_COMMAND = "conductor.resume";

/** Command id for the abort-running-task control action (4C-2). */
export const ABORT_COMMAND = "conductor.abort";

/** Command id for the approve-awaiting-task control action (4C-2). */
export const APPROVE_COMMAND = "conductor.approve";

/** Command id for the show-task-diff action (4C-1b). Opens the most-recent task diff (or a
 * quick-pick when several are pending) in a native read-only diff document. */
export const SHOW_DIFF_COMMAND = "conductor.showDiff";

/** Command id that opens (or reveals) the editor-area Command Center (N0 — ADR-0036). The
 * Devin "Command Center = default surface" model: the cockpit lives in the MAIN editor area,
 * not only the activity-bar sidebar. N0 ships the command + panel; the sidebar Fleet view
 * stays during the transition, and the startup auto-open is deferred to N1. */
export const OPEN_COMMAND = "conductor.open";

/** Command id that refetches the native "Conductors" sessions tree (N2). Surfaced as the
 * view's title refresh button + the command palette. */
export const REFRESH_SESSIONS_COMMAND = "conductor.refreshSessions";

/** Command id that re-probes the native Diagnostics (Status) tree — title button + palette. */
export const REFRESH_DIAGNOSTICS_COMMAND = "conductor.refreshDiagnostics";

/** Command id that deep-links the Command Center to a specific session (N3). Invoked by a
 * sessions-tree task click with `[projectId, taskId]` arguments: it opens/reveals the panel
 * and navigates its cockpit to that task's SessionView. */
export const OPEN_SESSION_COMMAND = "conductor.openSession";

/** Command id that opens a specific task's diff (P3 agentic): the sessions-tree TASK
 * `view/item/context` "Open Diff" action. Invoked with the task node; opens that task's
 * most-recent native diff (or a clear info message if none). */
export const OPEN_TASK_DIFF_COMMAND = "conductor.openTaskDiff";

/** Command id that opens the Intake "New Work" window (Faz-Q / Q3a) — Intake as its OWN
 * editor-area surface instead of a buried cockpit tab (the user's "intake ayrı bir window"). */
export const NEW_WORK_COMMAND = "conductor.newWork";

/** Command id that resets a BLOCKED task to ready (re-run) — gateway POST .../retry. Invoked from
 * a blocked sessions-tree task node or a blocked decision row in the live Stream. */
export const RETRY_TASK_COMMAND = "conductor.retryTask";

/** Command id of the NATIVE "Dispatch Work" flow (Faz-R) — a webview-less command-palette path
 * (QuickPick + InputBox) that synthesizes intake YAML and POSTs it to the gateway. The keyboard-
 * first complement to the rich webview Intake (NEW_WORK_COMMAND). */
export const DISPATCH_COMMAND = "conductor.dispatch";

/** Command id that (re)opens the native Getting Started walkthrough — the "how to use Conductor
 * well" tour. Auto-opened once on first launch; re-runnable from the palette anytime. */
export const OPEN_TOUR_COMMAND = "conductor.openTour";

/** The walkthrough's fully-qualified id (`<publisher>.<name>#<walkthroughId>`), the argument
 * `workbench.action.openWalkthrough` expects. */
export const WALKTHROUGH_ID = "everva.conductor-editor#conductor.gettingStarted";

/** WebviewPanel viewType + tab title of the editor-area Intake window (Q3a). A singleton panel
 * (its own tab) that mounts the standalone IntakeChat surface (data-surface="intake"). */
export const INTAKE_VIEW_TYPE = "conductor.intake";
export const INTAKE_TITLE = "New Work";

/** Command ids for the per-surface "separate windows" (B — the user's "board/fleet/events'i ayrı
 * pencerelere"). Each opens (or reveals) a SINGLETON editor-area panel LOCKED to one cockpit
 * surface, so the director can place Board / Fleet / Events side by side or "Move into New Window"
 * natively. They reuse the SAME cockpit bundle + live bridge as the Command Center — only the
 * mounted surface (data-surface) differs. */
export const OPEN_BOARD_COMMAND = "conductor.openBoard";
export const OPEN_FLEET_COMMAND = "conductor.openFleet";
export const OPEN_EVENTS_COMMAND = "conductor.openEvents";

/** The three surface-window descriptors: (surface, command, viewType, tab title). The IntakePanel
 * already owns "intake"; these add Board/Fleet/Events as their own windows via {@link SurfacePanel}. */
export const SURFACE_WINDOWS: ReadonlyArray<{
  readonly surface: "board" | "fleet" | "events";
  readonly command: string;
  readonly viewType: string;
  readonly title: string;
}> = [
  { surface: "board", command: OPEN_BOARD_COMMAND, viewType: "conductor.boardWindow", title: "Conductor Board" },
  { surface: "fleet", command: OPEN_FLEET_COMMAND, viewType: "conductor.fleetWindow", title: "Conductor Fleet" },
  { surface: "events", command: OPEN_EVENTS_COMMAND, viewType: "conductor.eventsWindow", title: "Conductor Events" },
];

/** when-clause context key (P3): true while the gateway connection is live. Set via the
 * built-in `setContext` on every connection-state change; the sessions-tree context menus +
 * the control keybindings gate on it (`when: conductor.connected`) so they don't offer
 * actions that can only fail while disconnected. */
export const CONTEXT_CONNECTED = "conductor.connected";

/** globalState key marking that the first-launch Conductor layout reveal has run (N5): the
 * activity-bar container is revealed ONCE (a fresh install) so the native "Conductors" sessions
 * rail shows alongside the auto-opened Command Center; later launches respect the user's
 * last-used view container (we don't re-grab the sidebar every window). */
export const FIRST_LAUNCH_KEY = "conductor.firstLaunchRevealed";

/** Custom URI scheme for the read-only diff virtual documents (4C-1b). Registered with a
 * TextDocumentContentProvider; the diff body is served from a bounded in-memory store. */
export const DIFF_SCHEME = "conductor-diff";

/** WebviewPanel viewType of the editor-area Command Center (N0 — ADR-0036). The cockpit's ONLY
 * mount (Faz-Q / Q0.4 — ADR-0044): the activity-bar sidebar's webview Fleet view was removed so
 * there is a SINGLE cockpit + a single live event stream (the offline/live bug is gone); the
 * sidebar now holds only the native "Conductors" tree (the selection driver). */
export const COMMAND_CENTER_VIEW_TYPE = "conductor.commandCenter";

/** Editor tab title for the Command Center panel (N0). */
export const COMMAND_CENTER_TITLE = "Conductor";

/** Activity-bar view CONTAINER id (package.json `viewsContainers.activitybar`). The 4C-3
 * notification's "Open Conductor" action reveals it via
 * `workbench.view.extension.<containerId>`. */
export const VIEW_CONTAINER_ID = "conductor";

/** The built-in VS Code command that reveals an activity-bar view container, suffixed with
 * the container id. Picking "Open Conductor" on an intervention toast runs this (4C-3). */
export const REVEAL_CONTAINER_COMMAND = `workbench.view.extension.${VIEW_CONTAINER_ID}`;

/** The action label on the intervention notification; picking it reveals the view (4C-3). */
export const OPEN_CONDUCTOR_ACTION = "Open Conductor";

/** Intervention-toast action: approve the held task in place (confirm-gated → a real merge).
 * The director acts on the gate-green task without first opening the panel (4C-3 upgrade). */
export const APPROVE_ACTION = "Approve";

/** Intervention-toast action: open THAT task's most-recent native diff (the take-over-into-
 * the-editor path) so the director can inspect the change hands-on (4C-3 upgrade / "take over"). */
export const OPEN_DIFF_ACTION = "Open diff";

/** Settings key (under the "conductor" section) holding the gateway base URL. */
export const GATEWAY_URL_SETTING = "gatewayUrl";

/** Fallback gateway URL when the setting is unset (matches package.json's default). */
export const DEFAULT_GATEWAY_URL = "http://localhost:8080";

/**
 * Narrow structural slice of the vscode API the wiring uses. Declaring it explicitly
 * (rather than the full module) lets the unit tests pass a small mock and keeps the
 * flows pure and assertable. Auth note: the token only ever flows into
 * `manager.connect`; nothing on this surface is handed the raw token.
 */
export interface VscodeApi {
  readonly commands: {
    registerCommand(command: string, callback: (...args: unknown[]) => unknown): vscode.Disposable;
    // 4C-3: the intervention notification's "Open Conductor" action reveals the activity-bar
    // container via the built-in `workbench.view.extension.conductor` command. Variadic args so
    // Faz-R's dispatch can deep-link a created session (executeCommand(openSession, project, task));
    // the real vscode.executeCommand is variadic too. The mock is a vi.fn (any args).
    executeCommand(command: string, ...args: unknown[]): Thenable<unknown>;
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    // Faz-R: the dispatch success toast offers an action item ("Open Session"); the rest form
    // resolves the chosen item (the single-arg form above stays valid — same vi.fn mock).
    showInformationMessage(message: string, ...items: string[]): Thenable<string | undefined>;
    showErrorMessage(message: string): Thenable<string | undefined>;
    showInputBox(options?: vscode.InputBoxOptions): Thenable<string | undefined>;
    // 4C-2: the control commands let the user pick a project and confirm destructive
    // actions. Narrow signatures (the items the handlers actually pass) so the flows stay
    // assertable against the headless mock. The two showWarningMessage overloads cover the
    // confirm-modal form (4C-2: options + item) and the 4C-3 intervention toast (message +
    // action label, no options object) — both resolve the chosen item.
    showQuickPick(
      items: readonly string[],
      options?: vscode.QuickPickOptions,
    ): Thenable<string | undefined>;
    showWarningMessage(
      message: string,
      options: vscode.MessageOptions,
      item: string,
    ): Thenable<string | undefined>;
    // Rest form covers the single-action toast (4C-2) AND the 4C-3 multi-action
    // intervention toast (Approve / Open diff / Open Conductor).
    showWarningMessage(message: string, ...items: string[]): Thenable<string | undefined>;
  };
}

/**
 * The ADDITIONAL narrow vscode surface the 4C-1b native-diff flow uses, kept SEPARATE from
 * {@link VscodeApi} so the connect/control/intervention flows (which only need commands +
 * window) stay assertable against `{ commands, window }` and don't have to supply these.
 * `registerConductor`/`activate` pass the real `vscode`, which satisfies both. Uses minimal
 * structural types (DiffUri/DiffDocument/DiffContentProvider) so BOTH the real vscode and the
 * headless mock satisfy it; the returns the flow ignores are typed `unknown`. NONE of these
 * carry a token (the diff render is token-free — see the diff section below).
 */
export interface DiffVscodeApi {
  readonly commands: {
    registerCommand(command: string, callback: (...args: unknown[]) => unknown): vscode.Disposable;
    // P2a: open a NATIVE diff editor via the built-in `vscode.diff` command
    // (left, right, title, options). Variadic so the real vscode + the mock both satisfy it;
    // the return is ignored (the flow only awaits it). Carries no token (the URIs are
    // `conductor-diff:` virtual docs served from the token-free store).
    executeCommand(command: string, ...args: unknown[]): Thenable<unknown>;
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    showQuickPick(
      items: readonly string[],
      options?: vscode.QuickPickOptions,
    ): Thenable<string | undefined>;
    // Open the rendered diff document. preview:true so diffs don't pile up tabs. The return
    // is ignored (the flow only awaits it) → `unknown`, keeping the real vscode (TextEditor)
    // and the headless mock structurally assignable.
    showTextDocument(
      document: DiffDocument,
      options?: vscode.TextDocumentShowOptions,
    ): Thenable<unknown>;
  };
  // The read-only virtual document: register the scheme's content provider + open a diff URI
  // (the provider fills it). Minimal structural types so both the real vscode + the mock fit.
  readonly workspace: {
    registerTextDocumentContentProvider(
      scheme: string,
      provider: DiffContentProvider,
    ): vscode.Disposable;
    openTextDocument(uri: DiffUri): Thenable<DiffDocument>;
  };
  // A `.diff`-suffixed URI already gets the `diff` language; this is the explicit belt-and-
  // braces call so highlighting is deterministic. The return is ignored.
  readonly languages: {
    setTextDocumentLanguage(document: DiffDocument, languageId: string): Thenable<unknown>;
  };
  // Build the per-diff `conductor-diff:` URI. Only `parse` is needed.
  readonly Uri: {
    parse(value: string): DiffUri;
  };
}

/** Minimal structural URI the diff flow needs: just `toString()` (the store keys on the
 * string form). The real `vscode.Uri` and the headless mock's UriLike both satisfy it. */
export interface DiffUri {
  toString(): string;
}

/** Minimal structural document the diff flow needs: just its `uri` (to assert WHICH diff was
 * opened; the body is supplied by the content provider). Real `vscode.TextDocument` (uri:
 * Uri) and the headless mock's `{ uri }` both satisfy it. */
export interface DiffDocument {
  readonly uri: DiffUri;
}

/** Minimal structural content provider for the `conductor-diff` scheme: the host calls
 * `provideTextDocumentContent(uri)` to fill the virtual document. Real
 * `vscode.TextDocumentContentProvider` (whose method takes an optional extra
 * CancellationToken) is assignable here, as is {@link makeDiffContentProvider}'s return. */
export interface DiffContentProvider {
  provideTextDocumentContent(uri: DiffUri): string | undefined | null;
}

/** The arguments the live-cockpit HTML builder needs, all already resolved by the caller
 * (resolveWebviewView): the runtime `cspSource`, a fresh per-render `nonce`, and the
 * `asWebviewUri`-mapped script + style URLs of the bundled cockpit. */
export interface WebviewHtmlOptions {
  readonly cspSource: string;
  readonly nonce: string;
  readonly scriptUri: string;
  readonly styleUri: string;
  /** The cockpit surface to mount. Omitted → the full Command Center cockpit (default); "intake" →
   * the standalone Intake "New Work" window; "board" | "fleet" | "events" → the cockpit LOCKED to
   * that single surface (B — separate windows). Host-controlled literal (never user input); the
   * fork entry reads it off `#root`'s data-surface. */
  readonly surface?: string;
}

/** The surfaces the host may mount in their OWN editor-area window (B / Q3). A whitelist so the
 * data-surface attribute is injection-free even though `surface` is already host-controlled. */
export const KNOWN_SURFACES: ReadonlySet<string> = new Set(["intake", "board", "fleet", "events"]);

/**
 * Builds the Fleet view HTML that loads the SHARED cockpit bundle (4B-3). Pure (no vscode
 * runtime) so a test asserts the CSP + that it references the script/style URIs. The CSP
 * is strict: `default-src 'none'`; only our own nonce'd <script> runs and only nonce'd or
 * `cspSource` styles apply; images from `cspSource`/data:, fonts from `cspSource`. Crucially
 * `connect-src 'none'` forbids the webview from doing its OWN network — ALL data arrives
 * over the postMessage bridge (4B-2) from the authed host, so the token never reaches the
 * webview (ADR-0027/0029). There is NO inline script body and no 'unsafe-inline': the only
 * script is the nonce'd bundle <script src=…>. The bundle mounts FleetDashboard in fork
 * mode (token="", injected transports) — see webview/main.tsx.
 */
export function webviewHtml(opts: WebviewHtmlOptions): string {
  const { cspSource, nonce, scriptUri, styleUri, surface } = opts;
  // Surface attribute: only a KNOWN surface literal is emitted (host-controlled); anything else is
  // the default cockpit (no attribute). The whitelist keeps the markup injection-free.
  const surfaceAttr = surface !== undefined && KNOWN_SURFACES.has(surface) ? ` data-surface="${surface}"` : "";
  return `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta
      http-equiv="Content-Security-Policy"
      content="default-src 'none'; script-src 'nonce-${nonce}'; style-src 'nonce-${nonce}' ${cspSource}; img-src ${cspSource} data:; font-src ${cspSource}; connect-src 'none';"
    />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <link rel="stylesheet" nonce="${nonce}" href="${styleUri}" />
    <title>Conductor</title>
  </head>
  <body>
    <div id="root"${surfaceAttr}></div>
    <script nonce="${nonce}" src="${scriptUri}"></script>
  </body>
</html>`;
}

/**
 * Builds a HostBridge over a resolved webview. Injectable so the CommandCenterPanel tests
 * can pass a fake (no real `ws`/fetch) and the production path uses the real wsConnector.
 * The TokenProvider reads the bearer token from SecretStorage per request (the bridge
 * never caches it); `baseUrl` is normalized + `wsBaseUrl` derived once here so the
 * webview's path/filter only ever extends the host's own (authed) base.
 */
export type BridgeFactory = (webview: WebviewLike) => { attach(): void; dispose(): void };

/** Configuration the {@link CommandCenterPanel} needs to load the cockpit bundle + attach the
 * bridge. `extensionUri` is the installed extension root (context.extensionUri) used to build the
 * webview resource URIs of the bundled cockpit under dist/webview. */
export interface FleetViewConfig {
  readonly secrets: SecretStore;
  readonly gatewayUrl: string;
  readonly extensionUri: vscode.Uri;
  /** Override the bridge factory in tests; defaults to the real HostBridge + wsConnector. */
  readonly bridgeFactory?: BridgeFactory;
}

/** The slice of `SecretStore` (connection.ts) the bridge's TokenProvider reads. Declared
 * locally to avoid widening this module's imports; `context.secrets` satisfies it. */
interface SecretStore {
  get(key: string): Thenable<string | undefined>;
}

/**
 * Builds the real bridge factory: a HostBridge whose TokenProvider reads
 * GATEWAY_TOKEN_KEY from SecretStorage, bound to the normalized REST base + its derived
 * WS base, using the real `ws` connector. The token flows only into the host's fetch
 * header / WS URL — never to the webview (see hostBridge.ts).
 */
export function makeBridgeFactory(secrets: SecretStore, gatewayUrl: string): BridgeFactory {
  const baseUrl = normalizeBaseUrl(gatewayUrl);
  const wsBaseUrl = deriveWsUrl(baseUrl);
  return (webview) =>
    new HostBridge({
      webview,
      baseUrl,
      wsBaseUrl,
      tokenProvider: { getToken: () => Promise.resolve(secrets.get(GATEWAY_TOKEN_KEY)) },
      wsConnector,
    });
}

/**
 * The editor-area Command Center (N0 — ADR-0036, implementing ADR-0032's "Command Center =
 * default surface"). A SINGLETON WebviewPanel in the MAIN editor area (ViewColumn.One) — the
 * Devin Desktop model where the agent command center is the primary surface, not a narrow
 * activity-bar sidebar. Since Faz-Q / Q0.4 (ADR-0044) it is the cockpit's ONLY mount: the
 * activity-bar sidebar's webview Fleet view was removed (the sidebar now holds only the native
 * "Conductors" tree), so there is a SINGLE live event stream — the old offline/live split
 * between two cockpit mounts is gone. Its wiring:
 *   - strict-CSP {@link webviewHtml} nonce-loading the shared cockpit bundle from dist/webview;
 *   - a HostBridge from {@link makeBridgeFactory}, so the token stays host-side (SecretStorage +
 *     the bridge's fetch/WS) and the panel is NOT a new token surface — CSP `connect-src 'none'`
 *     forbids any webview-originated network. No token, message, or log ever crosses into the panel.
 *
 * Singleton: a second `conductor.open` REVEALS the existing panel rather than spawning a
 * duplicate. `retainContextWhenHidden` keeps the live cockpit + its WS bridge alive while the
 * tab is backgrounded — a command center is long-lived, and re-mounting on every tab switch
 * would drop the event stream. The bridge is torn down (WS handles + listener) when the panel
 * is closed, and {@link dispose} (called on deactivate) closes the panel + bridge.
 *
 * N0 SCOPE: ships the panel + the `conductor.open` command, COEXISTING with the sidebar Fleet
 * view. It does NOT auto-open on startup (that default flip is N1) nor remove the sidebar (N2).
 */
export class CommandCenterPanel implements vscode.Disposable {
  readonly #config: FleetViewConfig;
  #panel: vscode.WebviewPanel | undefined;
  #bridge: { dispose(): void } | undefined;
  // Selection buffering (Faz-Q / Q0): `#ready` flips true when the webview posts `webview-ready`;
  // a select requested before then (cold-start window) is held in `#pendingSelect` + flushed on ready.
  #ready = false;
  #pendingSelect: { project: string; task?: string } | undefined;

  constructor(config: FleetViewConfig) {
    this.#config = config;
  }

  /**
   * Opens the Command Center in the editor area, or reveals the existing panel (singleton).
   * Loads the shared cockpit bundle ({@link webviewHtml}) + attaches a HostBridge, but targets a
   * WebviewPanel whose options are set at CREATION (not assigned onto `webview.options`).
   */
  open(): void {
    if (this.#panel !== undefined) {
      this.#panel.reveal(vscode.ViewColumn.One);
      return;
    }
    const config = this.#config;
    const distRoot = vscode.Uri.joinPath(config.extensionUri, "dist", "webview");
    const panel = vscode.window.createWebviewPanel(
      COMMAND_CENTER_VIEW_TYPE,
      COMMAND_CENTER_TITLE,
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        // Scope asset loading to the bundle dir (paired with the strict CSP): the webview can
        // read ONLY dist/webview, nothing else in the extension.
        localResourceRoots: [distRoot],
        // Keep the cockpit + its WS bridge alive while the tab is hidden (long-lived surface).
        retainContextWhenHidden: true,
      },
    );
    this.#panel = panel;

    const webview = panel.webview;
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.js")).toString();
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.css")).toString();
    webview.html = webviewHtml({
      cspSource: webview.cspSource,
      nonce: makeNonce(),
      scriptUri,
      styleUri,
    });

    const factory =
      config.bridgeFactory ?? makeBridgeFactory(config.secrets, config.gatewayUrl);
    const bridge = factory(webview);
    this.#bridge = bridge;
    bridge.attach();

    // N3 deep-link: a fresh webview starts NOT ready; it posts `webview-ready` on mount, at
    // which point we flush any navigate buffered during the cold-start window. This listener
    // observes the same webview as the bridge (both fire) but only acts on the ready ping.
    this.#ready = false;
    webview.onDidReceiveMessage((msg) => {
      if (isWebviewReady(msg)) {
        this.#ready = true;
        this.#flushPendingSelect();
      }
    });

    // Closing the tab tears the bridge down (WS handles + listener) and clears the singleton
    // (+ the N3 ready/pending state) so a later open builds a fresh panel.
    panel.onDidDispose(() => {
      bridge.dispose();
      if (this.#bridge === bridge) {
        this.#bridge = undefined;
      }
      if (this.#panel === panel) {
        this.#panel = undefined;
      }
      this.#ready = false;
      this.#pendingSelect = undefined;
    });
  }

  /**
   * Pushes a selection onto the cockpit (Faz-Q / Q0): ensures the Command Center is open in the
   * editor area, then posts a `select` control message to its webview. `task` omitted → select the
   * project (the cockpit scopes its board to it); `task` present → ALSO open that task's SessionView
   * (the sessions-tree deep-link, formerly `navigate-session`). If the webview hasn't signalled
   * `webview-ready` yet (cold start), the selection is BUFFERED and flushed on ready — so the first
   * click right after a cold open isn't lost. Token-free (only project/task ids cross); re-selecting
   * posts a fresh message.
   */
  select(project: string, task?: string): void {
    this.open();
    const panel = this.#panel;
    if (panel === undefined) {
      return;
    }
    if (this.#ready) {
      void panel.webview.postMessage({
        kind: "select",
        project,
        ...(task !== undefined ? { task } : {}),
      });
    } else {
      this.#pendingSelect = { project, ...(task !== undefined ? { task } : {}) };
    }
  }

  /** Posts a selection buffered during cold start, once the webview signals ready (Faz-Q / Q0). */
  #flushPendingSelect(): void {
    const panel = this.#panel;
    const sel = this.#pendingSelect;
    if (panel === undefined || sel === undefined) {
      return;
    }
    this.#pendingSelect = undefined;
    void panel.webview.postMessage({
      kind: "select",
      project: sel.project,
      ...(sel.task !== undefined ? { task: sel.task } : {}),
    });
  }

  /** Disposes the panel + its bridge if open (deactivate / test cleanup). Idempotent: closing
   * the panel fires its onDidDispose, which tears the bridge down; if it never opened, no-op. */
  dispose(): void {
    this.#panel?.dispose();
    // Defensive: if there's no panel but a bridge somehow lingered, drop it.
    if (this.#panel === undefined) {
      this.#bridge?.dispose();
      this.#bridge = undefined;
    }
  }
}

/**
 * Maps a connection state onto a status-bar label (codicon + text). Pure so a test can
 * assert the rendering for each state. Carries no token, only the state.
 */
export function statusBarText(state: ConnectionState): string {
  switch (state) {
    case "connected":
      return "$(plug) Conductor: connected";
    case "connecting":
      return "$(sync~spin) Conductor: connecting";
    case "error":
      return "$(error) Conductor: error";
    case "disconnected":
      return "$(debug-disconnect) Conductor: disconnected";
  }
}

/**
 * Renders the native fleet-glance status-bar label for a project count (Faz-Q / Q4.2): the
 * at-a-glance "$(server) N Conductor(s)" the director reads without opening the cockpit. Pure so
 * a test asserts the 0/1/N renderings. Returns "" for ≤0 (the caller hides the item then);
 * singular/plural for ≥1. Carries no token — only the count.
 */
export function fleetBarText(count: number): string {
  if (count <= 0) {
    return "";
  }
  return `$(server) ${count} Conductor${count === 1 ? "" : "s"}`;
}

/**
 * Renders the native claude-usage status-bar text + tooltip from the GET /usage snapshot map
 * (Faz-U). Compact text shows each subscription's 5-hour used %, e.g. "$(pulse) admin 42% ·
 * vendor 16%"; the tooltip carries the 7-day window + reset times + any unavailable reason. Pure
 * so a test asserts the renderings. Returns text:"" when there is nothing to show (the caller
 * hides the item). Carries no token — only the percentages the gateway already exposes.
 */
export function usageBarText(usage: Record<string, UsageSnapshot>): { text: string; tooltip: string } {
  const keys = Object.keys(usage).sort();
  if (keys.length === 0) {
    return { text: "", tooltip: "" };
  }
  const parts: string[] = [];
  const tip: string[] = ["Claude subscription usage"];
  const pct = (v: number | null | undefined): string => (v == null ? "—" : `${Math.round(v)}%`);
  for (const k of keys) {
    const s = usage[k] ?? {};
    const name = s.label ?? k;
    if (s.available === false) {
      parts.push(`${k} —`);
      tip.push(`• ${name}: unavailable${s.reason ? ` (${s.reason})` : ""}`);
      continue;
    }
    parts.push(`${k} ${pct(s.five_hour?.used_pct)}`);
    const r5 = s.five_hour?.resets_at ? `, resets ${s.five_hour.resets_at.slice(11, 16)}` : "";
    tip.push(`• ${name}: 5h ${pct(s.five_hour?.used_pct)}${r5} · 7d ${pct(s.seven_day?.used_pct)}`);
  }
  return { text: `$(pulse) ${parts.join(" · ")}`, tooltip: tip.join("\n") };
}

/**
 * Renders the dedicated intervention status-bar label for a pending count (4C-3). Pure so a
 * test asserts the 0/1/N renderings. Returns "" for 0 (the caller hides the item then);
 * singular/plural for ≥1. Carries no token — only the count. Distinct codicon ($(bell))
 * from the connection item so the two reads don't collide.
 */
export function interventionStatusText(count: number): string {
  if (count <= 0) {
    return "";
  }
  const noun = count === 1 ? "intervention" : "interventions";
  return `$(bell) Conductor: ${count} ${noun}`;
}

/** The slice of the intervention status-bar item `handleIntervention` updates: set its
 * text + show/hide it. The mock's StatusBarItem (and the real vscode.StatusBarItem) satisfy
 * it. Declared narrowly so the helper stays assertable. */
export interface InterventionStatusBar {
  text: string;
  show(): void;
  hide(): void;
}

/**
 * The task-scoped actions the intervention toast can take, injected so `handleIntervention`
 * stays pure of the control/diff wiring (a test passes spies; activate passes the real
 * flows). Both are scoped to the intervention's own project/task. Token-free by contract.
 */
export interface InterventionActions {
  /** Approve THIS held task in place (confirm-gated → a real merge). */
  approve: (project: string, task: string) => void | Promise<void>;
  /** Open THIS task's most-recent native diff (take-over-into-the-editor). */
  openDiff: (project: string, task: string) => void | Promise<void>;
}

/**
 * The testable core of the intervention handler (4C-3, upgraded): given the new pending
 * COUNT and the distilled (token-free) intervention, (1) update + show the dedicated
 * status-bar item, and (2) show a warning toast naming `project/task: reason` with THREE
 * one-click actions — Approve (act on the gate-green task in place), Open diff (take over
 * the change in the native editor), Open Conductor (reveal the panel). The choice routes to
 * the injected action (or the reveal command) so the director never has to dig for it. Pure
 * of the count bookkeeping (the caller owns the counter) so a test drives it with a fixed
 * count + a mock api + spy actions.
 *
 * TOKEN NOTE: `intervention` carries only project/task/reason (the notifier guarantees no
 * token in it), and neither the toast nor the status text echoes anything else — so no
 * token can reach a vscode message here.
 */
export async function handleIntervention(
  api: VscodeApi,
  statusBar: InterventionStatusBar,
  count: number,
  intervention: Intervention,
  actions: InterventionActions,
): Promise<void> {
  statusBar.text = interventionStatusText(count);
  statusBar.show();

  const { project, task, reason } = intervention;
  const choice = await api.window.showWarningMessage(
    `Intervention needed — ${project}/${task}: ${reason}`,
    APPROVE_ACTION,
    OPEN_DIFF_ACTION,
    OPEN_CONDUCTOR_ACTION,
  );
  if (choice === OPEN_CONDUCTOR_ACTION) {
    await api.commands.executeCommand(REVEAL_CONTAINER_COMMAND);
  } else if (choice === APPROVE_ACTION) {
    await actions.approve(project, task);
  } else if (choice === OPEN_DIFF_ACTION) {
    await actions.openDiff(project, task);
  }
}

/**
 * The editor-area Intake "New Work" window (Faz-Q / Q3a, ADR-0046): a SINGLETON WebviewPanel that
 * hosts the standalone IntakeChat surface (data-surface="intake") in its OWN editor tab — the
 * user's "intake ayrı bir window". REST-only (distill/intake/listProjects over the bridge): the
 * webview opens NO event stream, so there is no live WS + no connection indicator → the Q0.4
 * single-connection discipline holds. Reuses the SAME cockpit bundle + bridge factory as the
 * Command Center; the token stays host-side (SecretStorage + the bridge's fetch), CSP
 * `connect-src 'none'`. Singleton: a second open reveals the existing tab.
 */
export class IntakePanel implements vscode.Disposable {
  readonly #config: FleetViewConfig;
  #panel: vscode.WebviewPanel | undefined;
  #bridge: { dispose(): void } | undefined;

  constructor(config: FleetViewConfig) {
    this.#config = config;
  }

  /** Opens the Intake window in the editor area, or reveals the existing panel (singleton). */
  open(): void {
    if (this.#panel !== undefined) {
      this.#panel.reveal(vscode.ViewColumn.One);
      return;
    }
    const config = this.#config;
    const distRoot = vscode.Uri.joinPath(config.extensionUri, "dist", "webview");
    const panel = vscode.window.createWebviewPanel(INTAKE_VIEW_TYPE, INTAKE_TITLE, vscode.ViewColumn.One, {
      enableScripts: true,
      localResourceRoots: [distRoot],
      retainContextWhenHidden: true,
    });
    this.#panel = panel;

    const webview = panel.webview;
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.js")).toString();
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.css")).toString();
    // surface:"intake" → the SAME bundle mounts IntakeChat instead of the cockpit (webview/main.tsx).
    webview.html = webviewHtml({
      cspSource: webview.cspSource,
      nonce: makeNonce(),
      scriptUri,
      styleUri,
      surface: "intake",
    });

    const factory = config.bridgeFactory ?? makeBridgeFactory(config.secrets, config.gatewayUrl);
    const bridge = factory(webview);
    this.#bridge = bridge;
    bridge.attach();

    panel.onDidDispose(() => {
      bridge.dispose();
      if (this.#bridge === bridge) {
        this.#bridge = undefined;
      }
      if (this.#panel === panel) {
        this.#panel = undefined;
      }
    });
  }

  /** Disposes the panel + its bridge if open (deactivate / test cleanup). Idempotent. */
  dispose(): void {
    this.#panel?.dispose();
    if (this.#panel === undefined) {
      this.#bridge?.dispose();
      this.#bridge = undefined;
    }
  }
}

/**
 * A per-surface editor-area window (B — the user's "board/fleet/events'i ayrı pencerelere"): a
 * SINGLETON {@link vscode.WebviewPanel} LOCKED to one cockpit surface (board / fleet / events) via
 * data-surface, so the director can place surfaces side by side or "Move into New Window" natively.
 * Reuses the SAME cockpit bundle + LIVE bridge as the Command Center (the full FleetDashboard with
 * the WS event stream) — the only difference is the locked surface. Opens BESIDE the active editor
 * (so it lands next to the Command Center, not on top of it); a second open reveals the existing
 * window. Token stays host-side (SecretStorage + the bridge's fetch), CSP `connect-src 'none'`.
 */
export class SurfacePanel implements vscode.Disposable {
  readonly #config: FleetViewConfig;
  readonly #surface: string;
  readonly #viewType: string;
  readonly #title: string;
  #panel: vscode.WebviewPanel | undefined;
  #bridge: { dispose(): void } | undefined;

  constructor(config: FleetViewConfig, surface: string, viewType: string, title: string) {
    this.#config = config;
    this.#surface = surface;
    this.#viewType = viewType;
    this.#title = title;
  }

  /** Opens the surface window beside the active editor, or reveals the existing one (singleton). */
  open(): void {
    if (this.#panel !== undefined) {
      this.#panel.reveal();
      return;
    }
    const config = this.#config;
    const distRoot = vscode.Uri.joinPath(config.extensionUri, "dist", "webview");
    const panel = vscode.window.createWebviewPanel(this.#viewType, this.#title, vscode.ViewColumn.Beside, {
      enableScripts: true,
      localResourceRoots: [distRoot],
      retainContextWhenHidden: true,
    });
    this.#panel = panel;

    const webview = panel.webview;
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.js")).toString();
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.css")).toString();
    // The SAME bundle mounts the cockpit LOCKED to this.#surface (webview/main.tsx reads data-surface).
    webview.html = webviewHtml({
      cspSource: webview.cspSource,
      nonce: makeNonce(),
      scriptUri,
      styleUri,
      surface: this.#surface,
    });

    const factory = config.bridgeFactory ?? makeBridgeFactory(config.secrets, config.gatewayUrl);
    const bridge = factory(webview);
    this.#bridge = bridge;
    bridge.attach();

    panel.onDidDispose(() => {
      bridge.dispose();
      if (this.#bridge === bridge) {
        this.#bridge = undefined;
      }
      if (this.#panel === panel) {
        this.#panel = undefined;
      }
    });
  }

  /** Disposes the panel + its bridge if open (deactivate / test cleanup). Idempotent. */
  dispose(): void {
    this.#panel?.dispose();
    if (this.#panel === undefined) {
      this.#bridge?.dispose();
      this.#bridge = undefined;
    }
  }
}

// ─────────────────────────────────────────────────────────────────────────────────────
// 4C-1b — native diff render (ADR-0030): the conductor emits a bounded KindDiff at the green
// gate; the host renders it in a NATIVE VS Code diff view. The render is a READ-ONLY VIRTUAL
// DOCUMENT: each diff gets a unique, stable `conductor-diff:` URI whose path ends in `.diff`
// (so VS Code auto-applies the `diff` language for highlighting), and the immutable body is
// served by a TextDocumentContentProvider from a small bounded store. Content is immutable
// per URI → no EventEmitter/onDidChange is needed. Mirrors the 4C-3 quiet bell pattern: the
// signal is a SEPARATE status-bar item, not a per-diff toast (interventions already toast —
// avoid double-noise; auto-surfacing held-task diffs is a possible later enhancement).
// ─────────────────────────────────────────────────────────────────────────────────────

/** Max diffs the bounded store retains (oldest evicted past this). Keeps the green-gate
 * stream from growing memory without bound; ~20 recent diffs is plenty for review. */
export const DIFF_STORE_CAP = 20;

/**
 * Renders the dedicated diff status-bar label for the count of retained diffs (4C-1b). Pure
 * so a test asserts the 0/1/N renderings. Returns "" for 0 (the caller hides the item then);
 * singular/plural for ≥1. Carries no token — only the count. Distinct codicon ($(git-compare))
 * from the connection ($(plug)) and intervention ($(bell)) items so the three reads don't
 * collide.
 */
export function diffStatusText(count: number): string {
  if (count <= 0) {
    return "";
  }
  const noun = count === 1 ? "diff" : "diffs";
  return `$(git-compare) Conductor: ${count} ${noun}`;
}

/**
 * Builds the read-only diff document body for a {@link TaskDiff}. PURE (no vscode runtime) so
 * a test asserts it directly. Layout: a short human header (project/task, `<base>...<branch>`,
 * the file count, and the word `truncated` when the producer capped the diff), then one stat
 * line per file (`M  path  +3 -1`), then a blank line, then the unified `patch` verbatim.
 *
 * TOKEN NOTE: `taskDiff` is the notifier-distilled, token-free shape; this only reorders its
 * own fields into text, so no token can reach the rendered document.
 */
export function renderDiffDocument(taskDiff: TaskDiff): string {
  const { project, task, branch, base, files, patch, truncated } = taskDiff;
  const range = `${base || "?"}...${branch || "?"}`;
  const fileWord = files.length === 1 ? "file" : "files";
  const header = [
    `# Conductor diff — ${project}/${task}`,
    `# ${range} · ${files.length} ${fileWord}${truncated ? " · truncated" : ""}`,
  ];
  const stats = files.map((f) => `# ${(f.status || "?").padEnd(2)} ${f.path}  +${f.additions} -${f.deletions}`);
  // Header + per-file stats, a blank separator, then the patch (which may be "" when the
  // producer dropped it / there's no textual change — the stats still convey the change).
  return [...header, ...stats, "", patch].join("\n");
}

/**
 * One changed file's reconstruction in a {@link StoredDiff} (P2a). `before`/`after` are the
 * hunk-scoped base/modified text (from {@link reconstructDiffFiles}); `diffable` is false for
 * a binary file or a pure rename (nothing to render side-by-side → the unified fallback is
 * used). Token-free (derived from the patch).
 */
export interface StoredDiffFile {
  path: string;
  before: string;
  after: string;
  diffable: boolean;
}

/**
 * One retained diff in the {@link DiffStore}: enough to (a) serve the content provider both the
 * per-file before/after sides (native `vscode.diff`) AND the unified fallback doc, and (b) label
 * a quick-pick. `n` is the monotonic id embedded in every URI; `uri` is the unified-fallback URI
 * (kept for back-compat — the status-bar/intervention paths reference it). Token-free.
 */
export interface StoredDiff {
  n: number;
  uri: string;
  project: string;
  task: string;
  fileCount: number;
  files: StoredDiffFile[];
  unified: string;
}

/** Builds the unified-fallback `conductor-diff:` URI for a diff `n` (ends in `.diff` so VS Code
 * applies the `diff` language to the fallback document). */
export function diffUnifiedUri(n: number): string {
  return `${DIFF_SCHEME}:/${n}/unified.diff`;
}

/** Builds a per-file SIDE `conductor-diff:` URI (P2a). The path ends in the real file path so
 * VS Code infers the language (TS/JS/… highlighting) on each side of the native diff; `n` +
 * `fileIdx` + `side` locate the content. Each path segment is encoded so odd names can't break
 * the URI (lookup never depends on the path — only on n/fileIdx/side). */
export function diffSideUri(n: number, fileIdx: number, side: "before" | "after", path: string): string {
  const encodedPath = path
    .split("/")
    .map((seg) => encodeURIComponent(seg))
    .join("/");
  return `${DIFF_SCHEME}:/${n}/file/${fileIdx}/${side}/${encodedPath}`;
}

/** A parsed `conductor-diff:` URI: the unified fallback doc, or one side of one file. */
type ParsedDiffUri =
  | { kind: "unified"; n: number }
  | { kind: "side"; n: number; fileIdx: number; side: "before" | "after" };

/** Parses a `conductor-diff:` URI string back to {@link ParsedDiffUri}, or undefined if it isn't
 * one we minted. Tolerant: an unknown/legacy/garbage URI (NaN id, wrong shape) → undefined →
 * the content provider serves the evicted placeholder. */
export function parseDiffUri(uri: string): ParsedDiffUri | undefined {
  const prefix = `${DIFF_SCHEME}:`;
  if (!uri.startsWith(prefix)) {
    return undefined;
  }
  const segs = uri.slice(prefix.length).split("/").filter((s) => s.length > 0);
  const n = Number(segs[0]);
  if (!Number.isInteger(n)) {
    return undefined;
  }
  if (segs[1] === "unified.diff") {
    return { kind: "unified", n };
  }
  if (segs[1] === "file") {
    const fileIdx = Number(segs[2]);
    const side = segs[3];
    if (Number.isInteger(fileIdx) && (side === "before" || side === "after")) {
      return { kind: "side", n, fileIdx, side };
    }
  }
  return undefined;
}

/**
 * A small BOUNDED store of recently-emitted diffs, keyed by a monotonic id `n` (embedded in
 * every URI, so re-diffing the same task never collides with a stale document). Each entry
 * reconstructs the per-file before/after sides (P2a — for native `vscode.diff`) AND keeps the
 * unified-text render (the fallback when a file isn't diffable). Caps the retained set at
 * {@link DIFF_STORE_CAP} (evicting the OLDEST), serves the content provider, and lists the
 * recents (newest first) for the quick-pick. No vscode runtime → unit-tested directly.
 * Token-free by construction (every entry derives from a token-free TaskDiff).
 */
export class DiffStore {
  // Insertion-ordered: Map preserves insertion order, so the first key is the oldest.
  readonly #byN = new Map<number, StoredDiff>();
  #counter = 0;

  /** Reconstructs + stores a diff, returning the unified-fallback URI string (the path ends in
   * `.diff` so VS Code applies the diff language). Evicts the oldest entry past the cap. */
  add(taskDiff: TaskDiff): string {
    const n = (this.#counter += 1);
    const files: StoredDiffFile[] = reconstructDiffFiles(taskDiff.patch).map((f) => ({
      path: f.path,
      before: f.before,
      after: f.after,
      diffable: f.diffable,
    }));
    const entry: StoredDiff = {
      n,
      uri: diffUnifiedUri(n),
      project: taskDiff.project,
      task: taskDiff.task,
      fileCount: taskDiff.files.length,
      files,
      unified: renderDiffDocument(taskDiff),
    };
    this.#byN.set(n, entry);
    // Evict oldest while over the cap (normally a single eviction per add).
    while (this.#byN.size > DIFF_STORE_CAP) {
      const oldest = this.#byN.keys().next().value;
      if (oldest === undefined) {
        break;
      }
      this.#byN.delete(oldest);
    }
    return entry.uri;
  }

  /** Replaces a stored entry's per-file reconstruction with one derived from a FULLER patch
   * (P2b): the editor fetches the FULL-context patch on open and upgrades the entry IN PLACE, so
   * the content provider then serves whole-file before/after (full-file native diff) instead of
   * the bounded hunk view. No-op if `n` is unknown/evicted. The entry object is mutated, so a
   * caller holding the same reference (recents()/latestForTask()) sees the upgraded files. */
  upgradeFiles(n: number, patch: string): void {
    const entry = this.#byN.get(n);
    if (entry === undefined) {
      return;
    }
    entry.files = reconstructDiffFiles(patch).map((f) => ({
      path: f.path,
      before: f.before,
      after: f.after,
      diffable: f.diffable,
    }));
  }

  /** The content for a `conductor-diff:` URI: the unified fallback body, or one file's before/
   * after side (P2a), or undefined if unknown/evicted (the provider maps that to a placeholder). */
  content(uri: string): string | undefined {
    const parsed = parseDiffUri(uri);
    if (parsed === undefined) {
      return undefined;
    }
    const entry = this.#byN.get(parsed.n);
    if (entry === undefined) {
      return undefined;
    }
    if (parsed.kind === "unified") {
      return entry.unified;
    }
    const file = entry.files[parsed.fileIdx];
    if (file === undefined) {
      return undefined;
    }
    return parsed.side === "before" ? file.before : file.after;
  }

  /** The retained diffs, NEWEST first (the most recent is `recents()[0]`). */
  recents(): StoredDiff[] {
    return [...this.#byN.values()].reverse();
  }

  /** The most-recent retained diff for a specific project/task, or undefined if none is
   * retained (never emitted, or evicted past the cap). Drives the task-scoped "Open diff"
   * take-over from the intervention toast. */
  latestForTask(project: string, task: string): StoredDiff | undefined {
    return this.recents().find((d) => d.project === project && d.task === task);
  }

  /** The number of retained diffs (drives the status-bar count). */
  get size(): number {
    return this.#byN.size;
  }
}

/** The placeholder body a diff document shows once its entry has been evicted from the
 * bounded store (the URI outlived the cap). Plain text; carries no token. */
export const DIFF_EVICTED_PLACEHOLDER = "(diff no longer available)";

/**
 * Builds the read-only `TextDocumentContentProvider` for the `conductor-diff` scheme, backed
 * by the given store. VS Code calls `provideTextDocumentContent(uri)` to fill the virtual
 * document; we return the stored body, or a clear placeholder for an unknown/evicted URI.
 * Content is immutable per URI, so no onDidChange emitter is needed. Pure of vscode runtime
 * apart from the structural return type → unit-tested directly.
 */
export function makeDiffContentProvider(store: DiffStore): DiffContentProvider {
  return {
    provideTextDocumentContent(uri: DiffUri): string {
      return store.content(uri.toString()) ?? DIFF_EVICTED_PLACEHOLDER;
    },
  };
}

/**
 * Fetches a task's FULL-context diff for the full-file native view (P2b). Resolves undefined when
 * none is available (no endpoint / 404 / no token / error), in which case the open flow keeps the
 * bounded P2a hunk reconstruction. Wired in activate to {@link DiffContentClient.fetchFullDiff};
 * the diff open flows take it as an OPTIONAL param so the P2a tests (which omit it) are unchanged.
 */
export type FetchFullDiff = (project: string, task: string) => Promise<FullTaskDiff | undefined>;

/**
 * Opens ONE changed file as a NATIVE diff (P2a): `vscode.diff` between the file's before/after
 * `conductor-diff:` side URIs (the content provider fills each from the store). The diff editor
 * gives red-green gutters, side-by-side/inline toggle, and F7 change navigation — the "native
 * git diff / Claude Code" experience. The side URI paths end in the real file path so VS Code
 * infers the language on each side. `column` (ViewColumn.Beside) targets the session split.
 * Token-free (the URIs key a token-free store).
 */
async function openFileVscodeDiff(
  api: DiffVscodeApi,
  entry: StoredDiff,
  file: StoredDiffFile,
  column?: vscode.ViewColumn,
): Promise<void> {
  const fileIdx = entry.files.indexOf(file);
  if (fileIdx < 0) {
    return; // defensive: the file isn't part of this entry.
  }
  const left = api.Uri.parse(diffSideUri(entry.n, fileIdx, "before", file.path));
  const right = api.Uri.parse(diffSideUri(entry.n, fileIdx, "after", file.path));
  const title = `${file.path} (${entry.task})`;
  // preview:true so diffs don't stack tabs; viewColumn only when a split is requested.
  const options =
    column === undefined ? { preview: true } : { preview: true, viewColumn: column };
  await api.commands.executeCommand("vscode.diff", left, right, title, options);
}

/**
 * Opens the unified-text FALLBACK document (the 4C-1b render) for a diff: used when no file is
 * natively diffable (binary-only / pure rename / a patch dropped over budget). parse the URI →
 * `openTextDocument` (the content provider fills it) → `showTextDocument` (preview) → set the
 * `diff` language. Token-free.
 */
async function openUnifiedDiffDoc(
  api: DiffVscodeApi,
  uri: string,
  column?: vscode.ViewColumn,
): Promise<void> {
  const doc = await api.workspace.openTextDocument(api.Uri.parse(uri));
  await api.window.showTextDocument(
    doc,
    column === undefined ? { preview: true } : { preview: true, viewColumn: column },
  );
  await api.languages.setTextDocumentLanguage(doc, "diff");
}

/**
 * Opens a retained diff entry (P2a). The native path: one diffable file → open its `vscode.diff`
 * directly; several → either quick-pick the file (`interactive`, the explicit show-diff command)
 * or open the FIRST diffable file silently (`!interactive`, the deep-link session split — a
 * deep-link must not prompt). When NO file is natively diffable (binary-only / pure rename /
 * dropped patch) it falls back to the unified-text document so the change is still inspectable.
 * `column` (Beside) targets the session split. Token-free.
 */
async function openStoredDiff(
  api: DiffVscodeApi,
  store: DiffStore,
  entry: StoredDiff,
  interactive: boolean,
  fetchFull?: FetchFullDiff,
  column?: vscode.ViewColumn,
): Promise<void> {
  // P2b: prefer the FULL-context patch (full-file native diff). Fetch it on open and, on success,
  // UPGRADE this entry's per-file reconstruction so the content provider serves whole files. Any
  // failure (no endpoint / 404 / no token / network) leaves the bounded P2a reconstruction in
  // place — a graceful fallback to the hunk view. The token never leaves the host fetch client.
  if (fetchFull !== undefined) {
    let full: FullTaskDiff | undefined;
    try {
      full = await fetchFull(entry.project, entry.task);
    } catch {
      full = undefined;
    }
    if (full !== undefined && full.patch !== "") {
      store.upgradeFiles(entry.n, full.patch);
    }
  }
  const diffable = entry.files.filter((f) => f.diffable);
  if (diffable.length === 0) {
    await openUnifiedDiffDoc(api, entry.uri, column); // nothing to render side-by-side.
    return;
  }
  if (diffable.length === 1 || !interactive) {
    await openFileVscodeDiff(api, entry, diffable[0]!, column); // single, or the primary (no prompt).
    return;
  }
  // P2c: several diffable files + interactive → open the NATIVE MULTI-FILE diff editor (every
  // changed file in ONE scrollable view — the GitHub-PR / Devin review surface). If the host's
  // multi-diff command is unavailable it returns false and we fall back to a per-file quick-pick.
  if (await openMultiFileDiff(api, entry, diffable)) {
    return;
  }
  const labels = diffable.map((f) => f.path);
  const picked = await api.window.showQuickPick(labels, { placeHolder: "Select a changed file" });
  if (picked === undefined) {
    return; // cancelled — quiet no-op.
  }
  const file = diffable.find((f) => f.path === picked);
  if (file === undefined) {
    return; // defensive: the picked label isn't one we offered.
  }
  await openFileVscodeDiff(api, entry, file, column);
}

/**
 * Opens ALL of a task's diffable files in the NATIVE MULTI-FILE diff editor (P2c): one scrollable
 * tab listing every changed file, each rendered as a native diff (red/green, F7 to jump, file
 * headers collapse) — the way you actually review a 10-file change, instead of opening them one at
 * a time. It uses the built-in multi-diff editor over the SAME `conductor-diff:` before/after side
 * URIs (so the content provider — and the P2b full-file upgrade already applied to entry.files —
 * back every pane). Returns true if it opened; false (the command threw → unavailable on an older
 * host) so the caller can fall back to a per-file quick-pick. Token-free (the URIs key the store).
 */
async function openMultiFileDiff(
  api: DiffVscodeApi,
  entry: StoredDiff,
  files: StoredDiffFile[],
): Promise<boolean> {
  const resources = files.map((f) => {
    const idx = entry.files.indexOf(f);
    return {
      originalUri: api.Uri.parse(diffSideUri(entry.n, idx, "before", f.path)),
      modifiedUri: api.Uri.parse(diffSideUri(entry.n, idx, "after", f.path)),
    };
  });
  const title = `${entry.task} — ${files.length} files`;
  try {
    // The built-in multi-file diff editor (the surface the SCM "Open All Changes" uses).
    await api.commands.executeCommand("_workbench.openMultiDiffEditor", { title, resources });
    return true;
  } catch {
    return false; // unavailable on this host → caller falls back to a per-file quick-pick.
  }
}

/**
 * The show-diff flow (4C-1b, P2a-native), the `conductor.showDiff` command + the diff status-bar
 * click. With several retained diffs it first offers a quick-pick labelled `<task> (<n> files)`,
 * then opens the chosen diff natively (a further file pick if it touches several files); with
 * exactly one it opens it directly; with none it shows an info message. The quick-pick label is
 * index-aligned to the recents list. Token-free throughout (the store is token-free).
 */
export async function runShowDiff(
  api: DiffVscodeApi,
  store: DiffStore,
  fetchFull?: FetchFullDiff,
): Promise<void> {
  const recents = store.recents();
  if (recents.length === 0) {
    await api.window.showInformationMessage("No task diffs yet.");
    return;
  }
  if (recents.length === 1) {
    await openStoredDiff(api, store, recents[0]!, true, fetchFull);
    return;
  }
  const labels = recents.map((d) => diffQuickPickLabel(d));
  const picked = await api.window.showQuickPick(labels, { placeHolder: "Select a task diff" });
  if (picked === undefined) {
    return; // cancelled — quiet no-op.
  }
  const idx = labels.indexOf(picked);
  if (idx < 0) {
    return; // defensive: the picked label isn't one we offered.
  }
  await openStoredDiff(api, store, recents[idx]!, true, fetchFull);
}

/** Labels a stored diff for the quick-pick: `<task> (<n> file[s])`. Pure; token-free. */
function diffQuickPickLabel(d: StoredDiff): string {
  const noun = d.fileCount === 1 ? "file" : "files";
  return `${d.task} (${d.fileCount} ${noun})`;
}

/**
 * Opens a SPECIFIC task's most-recent native diff (4C-3 "take over"): the intervention toast's
 * "Open diff" routes here so the director lands on the change for the task that needs them —
 * not a quick-pick of everything. With no retained diff for that task (never emitted / evicted)
 * it shows a clear info message rather than opening a stale one. Token-free (the store is).
 */
export async function runShowDiffForTask(
  api: DiffVscodeApi,
  store: DiffStore,
  project: string,
  task: string,
  fetchFull?: FetchFullDiff,
): Promise<void> {
  const d = store.latestForTask(project, task);
  if (d === undefined) {
    await api.window.showInformationMessage(`No diff yet for ${project}/${task}.`);
    return;
  }
  await openStoredDiff(api, store, d, true, fetchFull);
}

/**
 * N3b — opens a task's most-recent native diff BESIDE the Command Center (the column passed,
 * `ViewColumn.Beside`), forming the session=workspace split: session detail (the cockpit
 * webview, column One) | native diff (column Two). QUIET: if no diff is retained for the task
 * (never emitted / evicted) it does NOTHING — unlike the intervention toast's "Open diff", a
 * deep-link's primary action is navigating the cockpit, so it must not nag with a "no diff"
 * message on every session click. Non-interactive: opens the PRIMARY changed file's native diff
 * (no file prompt) — `preview: true` reuses the one diff tab across clicks. Token-free.
 */
export async function openTaskDiffBeside(
  api: DiffVscodeApi,
  store: DiffStore,
  project: string,
  task: string,
  column: vscode.ViewColumn,
  fetchFull?: FetchFullDiff,
): Promise<void> {
  const d = store.latestForTask(project, task);
  if (d === undefined) {
    return;
  }
  await openStoredDiff(api, store, d, false, fetchFull, column);
}

/**
 * Approves a SPECIFIC held task in place (4C-3 toast "Approve"): confirm-gate it (approve
 * triggers a real merge), fire the authed task-level control POST, and surface a token-FREE
 * result. Mirrors runControl's approve branch but is task-scoped (no project pick) so the
 * director acts on the exact gate-green task the intervention named. A declined confirm is a
 * quiet no-op. The token lives only in the ControlClient header — never in a message here.
 */
export async function runApproveTask(
  api: VscodeApi,
  control: Pick<Control, "approve">,
  project: string,
  task: string,
): Promise<void> {
  const choice = await api.window.showWarningMessage(
    `Approve ${project}/${task}? This triggers a real merge.`,
    { modal: true },
    "Yes",
  );
  if (choice !== "Yes") {
    return; // declined / dismissed — quiet no-op.
  }
  const result = await control.approve(project, task);
  if (result.ok) {
    await api.window.showInformationMessage(`Approve requested for ${project}/${task}.`);
    return;
  }
  switch (result.reason) {
    case "conflict":
      await api.window.showWarningMessage(
        "No single task is awaiting approval (none or multiple).",
        {},
        "OK",
      );
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/**
 * The testable core of the diff handler (4C-1b): given the new diff, store + render it, then
 * reflect the retained-diff COUNT on the dedicated status-bar item (show it; the count is
 * always ≥1 here). Deliberately QUIET — NO toast (the status bar is the signal; interventions
 * already toast, so a per-diff toast would be double-noise; auto-surfacing held-task diffs is
 * a possible later enhancement). The status-bar item's `command` (set at activation) opens
 * the most-recent diff. Token-free: `taskDiff` carries no token and neither the store nor the
 * status text echoes anything else.
 */
export function handleDiff(
  store: DiffStore,
  statusBar: InterventionStatusBar,
  taskDiff: TaskDiff,
): void {
  store.add(taskDiff);
  statusBar.text = diffStatusText(store.size);
  statusBar.show();
}

/**
 * The connect flow: guide if the URL is unset, prompt for the token (password input),
 * delegate to the ConnectionManager, and surface a token-FREE result message. The
 * `token` never touches a message/log — only `manager.connect`. A cancelled/empty
 * prompt is a quiet no-op.
 */
export async function runConnect(
  api: VscodeApi,
  manager: ConnectionManager,
  gatewayUrl: string,
): Promise<void> {
  const url = gatewayUrl.trim();
  if (url === "") {
    await api.window.showErrorMessage(
      `Set the gateway URL in the "conductor.${GATEWAY_URL_SETTING}" setting, then run Connect again.`,
    );
    return;
  }

  const token = await api.window.showInputBox({
    password: true,
    ignoreFocusOut: true,
    prompt: "Conductor gateway token",
    placeHolder: "bearer token (stored in SecretStorage)",
  });
  if (token === undefined || token === "") {
    return; // cancelled / empty — quiet no-op.
  }

  const result = await manager.connect(token);
  if (result.ok) {
    await api.window.showInformationMessage(`Connected to ${url}`);
    return;
  }
  if (result.reason === "invalid-token") {
    await api.window.showErrorMessage("The gateway rejected that token.");
  } else {
    await api.window.showErrorMessage(`Could not reach the gateway at ${url}.`);
  }
}

/** The disconnect flow: forget the token + report. Carries no token. */
export async function runDisconnect(api: VscodeApi, manager: ConnectionManager): Promise<void> {
  await manager.disconnect();
  await api.window.showInformationMessage("Disconnected.");
}

/**
 * Minimal structural terminal the L1 login flow drives: open it (`show`) + type the mint
 * command (`sendText`). The real `vscode.Terminal` satisfies it; the headless mock supplies a
 * spy pair. NO token flows through here — only the (non-secret) `claude setup-token` command
 * string is sent; the printed token stays in the terminal for the user to copy.
 */
export interface LoginTerminal {
  sendText(text: string): void;
  show(): void;
}

/**
 * The narrow vscode surface the Faz L1 login flow uses, kept SEPARATE from {@link VscodeApi}
 * (like {@link DiffVscodeApi}) so the connect/control flows' mocks don't have to grow a
 * `createTerminal`. `registerConductor`/`activate` pass the real `vscode`, which satisfies it.
 *
 * TOKEN DISCIPLINE: no method here is ever HANDED the token. It enters only via the RETURN
 * value of `showInputBox` (a `password:true` field) and flows straight into
 * `manager.storeClaudeToken` (SecretStorage). `createTerminal`/`sendText` carry only the
 * non-secret mint command.
 */
export interface LoginVscodeApi {
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    showErrorMessage(message: string): Thenable<string | undefined>;
    showInputBox(options?: vscode.InputBoxOptions): Thenable<string | undefined>;
    createTerminal(options: vscode.TerminalOptions): LoginTerminal;
  };
}

/**
 * The L1 login flow (`conductor.login`, ADR-0049): mint + store the portable claude OAuth token
 * the xirigo way, so the director never SSHes into the performer host for an interactive
 * `claude` login. Opens an integrated terminal running `claude setup-token` (browser OAuth → the
 * CLI prints a long-lived `sk-ant-oat…` token), then prompts for that token via a PASSWORD input
 * and stores it in SecretStorage (CLAUDE_OAUTH_TOKEN_KEY) through the manager.
 *
 * TOKEN DISCIPLINE (HARD — account-level secret): we DO NOT capture the terminal's stdout — the
 * user copies the printed token and pastes it into the masked input, so the token never lands in
 * a buffer/log/process-table on our side. It flows ONLY into `manager.storeClaudeToken`
 * (SecretStorage); the success message names no token. A cancelled/empty paste is a quiet no-op.
 */
export async function runLogin(api: LoginVscodeApi, manager: ConnectionManager): Promise<void> {
  const terminal = api.window.createTerminal({ name: "Conductor Login" });
  terminal.show();
  // Type (not capture) the mint command: the user completes the browser OAuth and copies the
  // token the CLI prints. The command string is non-secret; the token is never read by us.
  terminal.sendText(CLAUDE_SETUP_TOKEN_CMD);

  const token = await api.window.showInputBox({
    password: true,
    ignoreFocusOut: true,
    prompt: "Paste the token printed by `claude setup-token`",
    placeHolder: "sk-ant-oat… (stored securely; your conductors will use it)",
  });
  if (token === undefined || token.trim() === "") {
    return; // cancelled / empty — quiet no-op.
  }

  await manager.storeClaudeToken(token.trim());
  await api.window.showInformationMessage(
    "Conductor login saved. The claude credential is stored securely and will be used by your conductors.",
  );
}

/** The L1 logout flow (`conductor.logout`): forget the stored claude OAuth token + report.
 * Carries no token. */
export async function runLogout(api: LoginVscodeApi, manager: ConnectionManager): Promise<void> {
  await manager.clearClaudeToken();
  await api.window.showInformationMessage("Conductor login cleared.");
}

/**
 * The L3 push flow (`conductor.pushCredential`, ADR-0049): upload the locally-stored claude
 * OAuth token to the gateway's ENCRYPTED credential store, so every conductor agent fetches it
 * (the editor is the single login point — no per-host secret-file). Reads the claude token from
 * SecretStorage host-side and hands it to the CredentialClient (which sends it ONLY in the
 * request body; the gateway bearer rides only in the Authorization header).
 *
 * TOKEN DISCIPLINE: neither secret reaches a message/log — the token-free `CredentialResult`
 * enum drives a fixed user-facing message. No stored claude token → guide to Log In first.
 */
export async function runPushCredential(
  api: LoginVscodeApi,
  secrets: SecretStore,
  client: CredentialClient,
): Promise<void> {
  const token = await secrets.get(CLAUDE_OAUTH_TOKEN_KEY);
  if (token === undefined || token === "") {
    await api.window.showErrorMessage('No claude login stored. Run "Conductor: Log In" first.');
    return;
  }
  const res = await client.upload(CLAUDE_CREDENTIAL_KIND, token);
  if (res.ok) {
    await api.window.showInformationMessage(
      "Login pushed to the gateway (encrypted). Your conductors will use it.",
    );
    return;
  }
  await api.window.showErrorMessage(credentialErrorMessage(res));
}

/** The L3 remove flow (`conductor.removeCredential`): delete the gateway-stored claude
 * credential (logout propagation). Token-free messaging. */
export async function runRemoveCredential(api: LoginVscodeApi, client: CredentialClient): Promise<void> {
  const res = await client.remove(CLAUDE_CREDENTIAL_KIND);
  if (res.ok) {
    await api.window.showInformationMessage("Login removed from the gateway.");
    return;
  }
  await api.window.showErrorMessage(credentialErrorMessage(res));
}

/** Maps a failed CredentialResult to a token-free, actionable message. Pure. */
function credentialErrorMessage(res: Extract<CredentialResult, { ok: false }>): string {
  switch (res.reason) {
    case "not-connected":
      return "Connect to the gateway first (Conductor: Connect to Gateway).";
    case "unauthorized":
      return "The gateway rejected the connection token.";
    case "not-configured":
      return "The gateway has no credential encryption configured (set CONDUCTOR_CREDENTIAL_KEY).";
    case "unreachable":
      return "Could not reach the gateway.";
  }
}

/** The narrow control surface `runControl` drives — the slice of ControlClient it uses.
 * Declared as an interface so the unit tests pass a fake (no real fetch/token) while the
 * production path injects a real ControlClient. None of these carry the token out. */
export interface Control {
  listProjects(): Promise<{ id: string }[]>;
  pause(projectId: string): Promise<ControlResult>;
  resume(projectId: string): Promise<ControlResult>;
  abort(projectId: string): Promise<ControlResult>;
  approve(projectId: string, taskId?: string): Promise<ControlResult>;
  retry(projectId: string, taskId: string): Promise<ControlResult>;
  // Faz-R native dispatch: POST raw scenario YAML to the gateway's intake endpoint.
  intake(projectId: string, yaml: string): Promise<ControlResult>;
}

/** Re-exported from controlClient so callers/tests reference one shape. */
export type ControlResult = Awaited<ReturnType<ControlClient["pause"]>>;

/** Human label for a control action, used in confirm prompts + result messages (e.g.
 * "Abort"). Pure; carries no token. */
function actionLabel(action: ControlAction): string {
  switch (action) {
    case "pause":
      return "Pause";
    case "resume":
      return "Resume";
    case "abort":
      return "Abort";
    case "approve":
      return "Approve";
  }
}

/** Whether an action needs an explicit modal confirm before firing. abort (cancels a
 * running task) and approve (a decisive human gate) are confirm-gated; pause/resume are
 * cheap + idempotent, so they fire directly. */
function needsConfirm(action: ControlAction): boolean {
  return action === "abort" || action === "approve";
}

/**
 * The inline-control flow (4C-2) for `conductor.pause`/`resume`/`abort`/`approve`. Lists
 * projects (authed, host-side), lets the user pick one, confirms destructive/decisive
 * actions (abort/approve) via a modal, fires the authed control POST, and surfaces a
 * token-FREE, action-specific result message. The token lives only in the ControlClient's
 * Authorization header — it never reaches a message here. Cancelling the pick (or the
 * confirm) is a quiet no-op. Reuses the existing control API; independent of the 4C-0
 * diff source (ADR-0030).
 */
export async function runControl(
  api: VscodeApi,
  control: Control,
  action: ControlAction,
  preselectedProjectId?: string,
): Promise<void> {
  const label = actionLabel(action);

  // P3: a preselected project (the sessions-tree context menu passes the clicked project) acts
  // on it DIRECTLY — no listProjects, no quick-pick. The palette path (no preselection) keeps
  // the original list-then-pick flow.
  let pick: string;
  if (preselectedProjectId !== undefined && preselectedProjectId !== "") {
    pick = preselectedProjectId;
  } else {
    let projects: { id: string }[];
    try {
      projects = await control.listProjects();
    } catch (err) {
      // listProjects failed before any action — branch on the closed reason (no token in
      // the message). "not-connected"/"unauthorized" guide the user to Connect; anything
      // else is an unreachable gateway.
      if (err instanceof ControlListError && err.reason === "not-connected") {
        await api.window.showErrorMessage("Connect to the gateway first.");
      } else if (err instanceof ControlListError && err.reason === "unauthorized") {
        await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      } else {
        await api.window.showErrorMessage("Could not reach the gateway.");
      }
      return;
    }

    if (projects.length === 0) {
      await api.window.showInformationMessage("No projects.");
      return;
    }

    const picked = await api.window.showQuickPick(
      projects.map((p) => p.id),
      { placeHolder: "Select a project" },
    );
    if (picked === undefined) {
      return; // cancelled — quiet no-op.
    }
    pick = picked;
  }

  if (needsConfirm(action)) {
    const choice = await api.window.showWarningMessage(
      `${label} ${pick}?`,
      { modal: true },
      "Yes",
    );
    if (choice !== "Yes") {
      return; // declined / dismissed — quiet no-op.
    }
  }

  const result = await control[action](pick);
  if (result.ok) {
    await api.window.showInformationMessage(`${label} requested for ${pick}.`);
    return;
  }
  switch (result.reason) {
    case "conflict":
      await api.window.showWarningMessage(conflictMessage(action), {}, "OK");
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/** Action-specific guidance for a 409 conflict from the gateway. abort: nothing is
 * running; approve: no SINGLE task is awaiting (zero or multiple). pause/resume are
 * idempotent and never 409, but the union is total so they get a generic fallback.
 * Carries no token. */
function conflictMessage(action: ControlAction): string {
  switch (action) {
    case "abort":
      return "No task is running to abort.";
    case "approve":
      return "No single task is awaiting approval (none or multiple).";
    case "pause":
    case "resume":
      return "The gateway could not apply that action.";
  }
}

/**
 * Retries a BLOCKED task: confirm → authed POST .../retry → refresh the sessions tree so the
 * status flips visibly (blocked→ready). Token-free messaging (the ControlResult carries only a
 * closed reason). A 409 means the task is not blocked (already running/done/awaiting). Exported
 * for tests; the token stays in the ControlClient's Authorization header.
 */
export async function runRetry(
  api: VscodeApi,
  control: Control,
  project: string,
  task: string,
): Promise<void> {
  const choice = await api.window.showWarningMessage(`Retry ${task}?`, { modal: true }, "Yes");
  if (choice !== "Yes") {
    return; // declined — quiet no-op.
  }
  const result = await control.retry(project, task);
  if (result.ok) {
    await api.window.showInformationMessage(`Retry requested for ${task} (re-queued).`);
    void api.commands.executeCommand(REFRESH_SESSIONS_COMMAND);
    return;
  }
  switch (result.reason) {
    case "conflict":
      await api.window.showWarningMessage("Only a blocked task can be retried.", {}, "OK");
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/**
 * NATIVE "Dispatch Work" flow (Faz-R): a webview-LESS, keyboard-first path to put new work into
 * the ledger. Pick a project (preselected from the sessions-tree project menu, else a quick-pick),
 * then a short prompt chain — title → acceptance → tier → lane — synthesizes a valid intake YAML
 * (dispatch.ts) and POSTs it to the gateway's intake endpoint. On success it shows the created ids,
 * refreshes the sessions tree, and offers to jump straight to the new session — the agentic-native
 * loop's entry point, all from the command palette. Any prompt cancelled (Esc) is a quiet no-op.
 *
 * TOKEN-FREE here: the YAML + ids carry no token; the authed POST lives in the ControlClient's
 * Authorization header. Exported so a unit test drives the whole chain against a mock api + control.
 */
export async function runDispatch(
  api: VscodeApi,
  control: Control,
  preselectedProjectId?: string,
): Promise<void> {
  // 1) Project — preselected (tree context menu) or quick-pick (palette).
  let project: string;
  if (preselectedProjectId !== undefined && preselectedProjectId !== "") {
    project = preselectedProjectId;
  } else {
    let projects: { id: string }[];
    try {
      projects = await control.listProjects();
    } catch (err) {
      if (err instanceof ControlListError && err.reason === "not-connected") {
        await api.window.showErrorMessage("Connect to the gateway first.");
      } else if (err instanceof ControlListError && err.reason === "unauthorized") {
        await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      } else {
        await api.window.showErrorMessage("Could not reach the gateway.");
      }
      return;
    }
    if (projects.length === 0) {
      await api.window.showInformationMessage("No projects. Onboard one first.");
      return;
    }
    const picked = await api.window.showQuickPick(
      projects.map((p) => p.id),
      { placeHolder: "Dispatch work to which project?" },
    );
    if (picked === undefined) {
      return; // cancelled.
    }
    project = picked;
  }

  // 2) Title (required).
  const title = (
    await api.window.showInputBox({
      title: `Dispatch work — ${project}`,
      prompt: "What should the agent build? (one line)",
      placeHolder: "e.g. Add a /healthz endpoint to the web server",
      ignoreFocusOut: true,
    })
  )?.trim();
  if (title === undefined || title === "") {
    return; // cancelled / empty.
  }

  // 3) Acceptance (required, ≥1) — newline/';'-separated criteria.
  const acceptance = parseAcceptance(
    await api.window.showInputBox({
      title: `Acceptance — ${project}`,
      prompt: "How is 'done' proven? Separate multiple criteria with ';'",
      placeHolder: "e.g. GET /healthz returns 200; response body is {\"ok\":true}",
      ignoreFocusOut: true,
    }),
  );
  if (acceptance.length === 0) {
    await api.window.showErrorMessage("At least one acceptance criterion is required.");
    return;
  }

  // 4) Tier (required, T1..T4) — risk → governance merge policy.
  const tier = parseTier(
    await api.window.showQuickPick(TIER_CHOICES, { placeHolder: "Risk tier (governs the merge gate)" }),
  );
  if (tier === undefined) {
    return; // cancelled.
  }

  // 5) Lane (required) — the capability lane that routes the task to a matching host. Prefilled
  // with a sensible default the user can edit (it is free-text vocabulary, not a fixed set).
  const lane = (
    await api.window.showInputBox({
      title: `Lane — ${project}`,
      prompt: "Capability lane (routes to a matching host)",
      value: "backend",
      ignoreFocusOut: true,
    })
  )?.trim();
  if (lane === undefined || lane === "") {
    return; // cancelled.
  }

  // 6) Id — derived from the title, shown for confirmation/edit (the user owns uniqueness).
  const id = (
    await api.window.showInputBox({
      title: `Task id — ${project}`,
      prompt: "Task id (must be unique in the project)",
      value: slugifyId(title),
      ignoreFocusOut: true,
    })
  )?.trim();
  if (id === undefined || id === "") {
    return; // cancelled.
  }

  const spec: DispatchSpec = {
    id,
    title,
    lane,
    tier,
    acceptance,
    holdoutRef: defaultHoldoutRef(id),
  };
  const yaml = buildIntakeYaml(spec);

  // 7) Final confirm — show the synthesized spec so the dispatch is never a surprise.
  const confirm = await api.window.showWarningMessage(
    `Dispatch "${title}" (${id}, ${tier}/${lane}) to ${project}?`,
    { modal: true },
    "Dispatch",
  );
  if (confirm !== "Dispatch") {
    return; // declined.
  }

  // 8) POST the intake YAML (authed, host-side).
  const result = await control.intake(project, yaml);
  if (result.ok) {
    const outcome = dispatchOutcome(result.body);
    void api.commands.executeCommand(REFRESH_SESSIONS_COMMAND);
    const first = outcome.createdIds[0];
    if (first !== undefined) {
      const choice = await api.window.showInformationMessage(
        outcome.message,
        "Open Session",
      );
      if (choice === "Open Session") {
        void api.commands.executeCommand(OPEN_SESSION_COMMAND, project, first);
      }
    } else {
      await api.window.showInformationMessage(outcome.message);
    }
    return;
  }
  switch (result.reason) {
    case "invalid":
      await api.window.showErrorMessage(
        result.detail ? `Rejected: ${result.detail}` : "The gateway rejected the work specification.",
      );
      return;
    case "conflict":
      await api.window.showWarningMessage("The gateway could not accept that work.", {}, "OK");
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/**
 * Wires the extension's contributions onto the given (real or mocked) vscode API and
 * returns the created disposables. Kept separate from `activate` so unit tests can call
 * it with a mock + a manager and assert the registration + command behavior without a
 * running host. `gatewayUrl` is captured for the connect flow's URL.
 */
export function registerConductor(
  api: VscodeApi & DiffVscodeApi & LoginVscodeApi,
  manager: ConnectionManager,
  gatewayUrl: string,
  control: Control,
  diffStore: DiffStore,
  fetchFullDiff?: FetchFullDiff,
  fleetConfig?: FleetViewConfig,
): vscode.Disposable[] {
  const connect = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void runConnect(api, manager, gatewayUrl);
  });
  const disconnect = api.commands.registerCommand(DISCONNECT_COMMAND, () => {
    void runDisconnect(api, manager);
  });
  // Faz L1 (ADR-0049): editor-mediated claude login. `conductor.login` mints + stores the
  // portable claude OAuth token (the agent consumes it in Faz L2); `conductor.logout` forgets
  // it. Editor-only, token-disciplined (see runLogin) — no Go, no gateway involvement.
  const login = api.commands.registerCommand(LOGIN_COMMAND, () => {
    void runLogin(api, manager);
  });
  const logout = api.commands.registerCommand(LOGOUT_COMMAND, () => {
    void runLogout(api, manager);
  });
  // 4C-2 inline control commands: each prompts for a project + (for abort/approve)
  // confirms, then fires the authed control POST host-side. The token stays in the
  // ControlClient's Authorization header — never in a message.
  const controlDisposables = (["pause", "resume", "abort", "approve"] as const).map((action) =>
    api.commands.registerCommand(controlCommandId(action), (arg?: unknown) => {
      // Faz-R: approve from a TASK node (sessions-tree awaiting task / Stream review row) is
      // TASK-PRECISE — it passes that task's id so the gateway approves THAT held task, which
      // works even when several await approval (the project-scoped auto-resolve would 409 then).
      if (action === "approve") {
        const ref = taskRefFromArg(arg);
        if (ref !== undefined) {
          void runApproveTask(api, control, ref.project, ref.task);
          return;
        }
      }
      // P3: from the sessions-tree project context menu VS Code passes the clicked node →
      // act on THAT project (no quick-pick). From the palette `arg` is undefined → list+pick.
      // A live-Stream review row passes a FeedEvent → projectIdFromArg resolves its project.
      void runControl(api, control, action, projectIdFromArg(arg));
    }),
  );
  // 4C-1b: the read-only diff scheme + the show-diff command. The content provider serves the
  // bounded store; the command opens the most-recent diff (or quick-picks when several are
  // retained). The DiffObserver (wired in activate) feeds the store on each green-gate diff.
  const diffProvider = api.workspace.registerTextDocumentContentProvider(
    DIFF_SCHEME,
    makeDiffContentProvider(diffStore),
  );
  const showDiff = api.commands.registerCommand(SHOW_DIFF_COMMAND, () => {
    void runShowDiff(api, diffStore, fetchFullDiff);
  });
  // N0 (ADR-0036): the editor-area Command Center — a singleton WebviewPanel hosting the cockpit
  // in the MAIN editor area (the Devin "Command Center = default surface" model). The cockpit's
  // ONLY mount since Q0.4. Built only on the production path (a fleetConfig with extensionUri/
  // secrets). `conductor.open` REVEALS it (keybinding / startup / status bar — no arg), OR, when a
  // sessions-tree project click passes a project id, SELECTS that project so the board scopes to
  // that Conductor (Faz-Q / Q1; `select` with no task → project-only selection over the bridge).
  const commandCenter = fleetConfig ? new CommandCenterPanel(fleetConfig) : undefined;
  const open = api.commands.registerCommand(OPEN_COMMAND, (arg?: unknown) => {
    const projectId = typeof arg === "string" ? arg : nodeProjectId(arg);
    if (projectId !== undefined) {
      commandCenter?.select(projectId);
    } else {
      commandCenter?.open();
    }
  });
  // N3: deep-link to a session — invoked by a sessions-tree task click with [projectId, taskId].
  // Opens/reveals the Command Center and navigates its cockpit to that task's SessionView.
  const openSession = api.commands.registerCommand(OPEN_SESSION_COMMAND, (...args: unknown[]) => {
    // Two callers: the tree CLICK passes [projectId, taskId] (set in getTreeItem.command); the
    // task `view/item/context` "Open Session" passes the task NODE (P3). Resolve both.
    const ref =
      typeof args[0] === "string" && typeof args[1] === "string"
        ? { project: args[0], task: args[1] }
        : nodeTaskRef(args[0]);
    if (ref !== undefined) {
      // Q0: select the task → the Command Center's cockpit opens its SessionView (editor area,
      // column One). `select` with a task IS the former N3 `navigate-session` deep-link.
      commandCenter?.select(ref.project, ref.task);
      // N3b: open that task's native diff BESIDE it (column Two) — the session=workspace split.
      // Quiet when no diff is retained yet (a deep-link shouldn't nag); reuses the preview tab.
      void openTaskDiffBeside(api, diffStore, ref.project, ref.task, vscode.ViewColumn.Beside, fetchFullDiff);
    }
  });
  // P3: the sessions-tree TASK "Open Diff" context action — opens THAT task's most-recent native
  // diff (a clear info message if none retained). Distinct from openSession (which also navigates
  // the cockpit + splits): this is the focused "just show me the change" action.
  const openTaskDiff = api.commands.registerCommand(OPEN_TASK_DIFF_COMMAND, (arg?: unknown) => {
    const ref = taskRefFromArg(arg); // tree task node OR a live-Stream diff/review row
    if (ref !== undefined) {
      void runShowDiffForTask(api, diffStore, ref.project, ref.task, fetchFullDiff);
    }
  });
  // A1/A3: drill a Stream row down to its FULL detailed JSON in a read-only doc — the user's
  // "istersem detaylı json lara bakarım". The row carries the original token-free event (`raw`).
  const showEventJson = api.commands.registerCommand(SHOW_EVENT_JSON_COMMAND, (arg?: unknown) => {
    const ev = arg as FeedEvent | undefined;
    const payload = ev?.raw ?? ev ?? {};
    // Open the full event as a read-only JSON doc. Uses the global vscode API directly (the injected
    // `api.workspace.openTextDocument` is narrowed to diff: URIs); this is a runtime drill-down
    // feature covered by the electron smoke, not the mocked unit path.
    void vscode.workspace
      .openTextDocument({ content: JSON.stringify(payload, null, 2), language: "json" })
      .then((doc) => vscode.window.showTextDocument(doc, { preview: true }));
  });
  // B/A3: retry a BLOCKED task (reset→ready) from a blocked tree node OR a blocked Stream row.
  // Confirm-gated host-side authed POST; refreshes the tree so the status flips visibly.
  const retryTask = api.commands.registerCommand(RETRY_TASK_COMMAND, (arg?: unknown) => {
    const ref = taskRefFromArg(arg);
    if (ref !== undefined) {
      void runRetry(api, control, ref.project, ref.task);
    }
  });
  // Q3a (ADR-0046): the editor-area Intake "New Work" window — Intake as its OWN tab (the user's
  // "intake ayrı bir window"), not a buried cockpit tab. Built only on the production path (a
  // fleetConfig); `conductor.newWork` opens or reveals it. REST-only (no live WS).
  const intakePanel = fleetConfig ? new IntakePanel(fleetConfig) : undefined;
  const newWork = api.commands.registerCommand(NEW_WORK_COMMAND, () => {
    intakePanel?.open();
  });
  // B — separate windows: a SINGLETON SurfacePanel per surface (Board / Fleet / Events), each opened
  // BESIDE the active editor so the director can place them side by side or "Move into New Window".
  // The commands register UNCONDITIONALLY (so they show in the palette); a panel only exists when the
  // host is configured/connected, so a command is a no-op until then (mirrors NEW_WORK_COMMAND).
  const surfacePanels = new Map<string, SurfacePanel>();
  if (fleetConfig) {
    for (const w of SURFACE_WINDOWS) {
      surfacePanels.set(w.command, new SurfacePanel(fleetConfig, w.surface, w.viewType, w.title));
    }
  }
  const surfaceCommands = SURFACE_WINDOWS.map((w) =>
    api.commands.registerCommand(w.command, () => surfacePanels.get(w.command)?.open()),
  );
  // Faz-R: the NATIVE, webview-less dispatch path. From the palette (no arg → quick-pick project)
  // or the sessions-tree project context menu (the clicked project is preselected). A short prompt
  // chain synthesizes intake YAML and POSTs it host-side (token in the Authorization header only).
  const dispatch = api.commands.registerCommand(DISPATCH_COMMAND, (arg?: unknown) => {
    void runDispatch(api, control, projectIdFromArg(arg));
  });
  // PO onboarding: (re)open the native Getting Started tour — "nasıl iyi kullanılacak" — from the
  // palette anytime. Auto-opened once on first launch (see activate). Delegates to the built-in
  // walkthrough surface, so no custom UI to maintain.
  const openTour = api.commands.registerCommand(OPEN_TOUR_COMMAND, () => {
    void api.commands.executeCommand("workbench.action.openWalkthrough", WALKTHROUGH_ID, false);
  });
  // Faz-Q / Q0.4 (ADR-0044): the sidebar webview Fleet view is gone — the cockpit mounts ONLY in
  // the editor-area Command Center, so a single HostBridge owns the one live event stream. The
  // sidebar keeps only the native "Conductors" tree (registered in `activate`, the selection driver).
  const disposables: vscode.Disposable[] = [
    connect,
    disconnect,
    login,
    logout,
    ...controlDisposables,
    diffProvider,
    showDiff,
    open,
    openSession,
    openTaskDiff,
    showEventJson,
    retryTask,
    newWork,
    ...surfaceCommands,
    dispatch,
    openTour,
  ];
  // Dispose the editor-area panels (+ their bridges) on deactivate when they were built.
  if (commandCenter) {
    disposables.push(commandCenter);
  }
  if (intakePanel) {
    disposables.push(intakePanel);
  }
  for (const panel of surfacePanels.values()) {
    disposables.push(panel);
  }
  return disposables;
}

/** Extracts a {project, task} ref from EITHER a sessions-tree task node (nodeTaskRef) OR a live
 * Stream FeedEvent (flat .project/.task strings), so the task actions (retry / open-diff) work from
 * both surfaces. Returns undefined when neither shape carries a task. */
function taskRefFromArg(arg: unknown): { readonly project: string; readonly task: string } | undefined {
  const node = nodeTaskRef(arg);
  if (node !== undefined) {
    return node;
  }
  if (arg !== null && typeof arg === "object") {
    const e = arg as { project?: unknown; task?: unknown };
    if (typeof e.project === "string" && e.project !== "" && typeof e.task === "string" && e.task !== "") {
      return { project: e.project, task: e.task };
    }
  }
  return undefined;
}

/** Project id from a tree node OR a Stream FeedEvent — so the project-level control actions
 * (approve/abort) can be invoked from a Stream row too. */
function projectIdFromArg(arg: unknown): string | undefined {
  return nodeProjectId(arg) ?? taskRefFromArg(arg)?.project;
}

/** Maps a control action to its contributed command id (kept in lockstep with the
 * package.json `contributes.commands` entries). */
function controlCommandId(action: ControlAction): string {
  switch (action) {
    case "pause":
      return PAUSE_COMMAND;
    case "resume":
      return RESUME_COMMAND;
    case "abort":
      return ABORT_COMMAND;
    case "approve":
      return APPROVE_COMMAND;
  }
}

/**
 * Reads the configured gateway URL from the "conductor.gatewayUrl" setting, falling
 * back to the default. Trimmed.
 */
export function readGatewayUrl(): string {
  const configured = vscode.workspace
    .getConfiguration("conductor")
    .get<string>(GATEWAY_URL_SETTING, DEFAULT_GATEWAY_URL);
  return (configured ?? DEFAULT_GATEWAY_URL).trim();
}

/**
 * Extension activation entry point (manifest `main` → esbuild bundle). Builds the
 * ConnectionManager (token at rest in SecretStorage, gateway probe bound to the
 * configured URL), a status-bar mirror of the connection state, registers the commands
 * + view, and kicks off a silent restore of any stored token.
 *
 * NOTE: the manager's gateway probe is bound to the URL read at activation. Changing
 * "conductor.gatewayUrl" takes effect after a window reload (acceptable for 4B-1;
 * live-reconfigure is a later polish).
 */
export function activate(context: vscode.ExtensionContext): void {
  const gatewayUrl = readGatewayUrl();

  const statusBar = vscode.window.createStatusBarItem();
  // M2: the auto-reconnect loop, assigned just after the ConnectionManager (it drives the loop via
  // manager.restore). Forward-declared here so the manager's onStateChange + the status-bar helper
  // can reference it; both run only at fire time, by which point it is assigned. (Must be `let`: it
  // is referenced lexically before its assignment, which a const declaration would forbid.)
  // eslint-disable-next-line prefer-const
  let reconnect: ReconnectController | undefined;
  // PO "what needs me?" — the activity-bar review badge updater. Forward-declared (reassigned once
  // the sessions view exists, below) so onStateChange can trigger it; a no-op until then.
  let updateReviewBadge: () => void = () => {};
  // updateConnectionStatusBar renders the link state, but shows a RECONNECTING label while the loop
  // is actively retrying a dropped link (so the user sees "trying to get back", not a dead
  // "disconnected"). A live "connected" always wins.
  const updateConnectionStatusBar = (): void => {
    if (manager.state !== "connected" && reconnect !== undefined && reconnect.phase !== "idle") {
      statusBar.text = "$(sync~spin) Conductor: reconnecting";
    } else {
      statusBar.text = statusBarText(manager.state);
    }
  };

  // 4C-3: a SEPARATE status-bar item for pending interventions ($(bell) Conductor: N
  // intervention(s)), shown only when the count > 0. The connection item above keeps
  // showing the link state; this one is the at-a-glance intervention read. Starts hidden.
  const interventionBar = vscode.window.createStatusBarItem();
  interventionBar.command = REVEAL_CONTAINER_COMMAND;
  let pendingInterventions = 0;

  // 4C-1b: a SEPARATE status-bar item for recently-emitted task diffs ($(git-compare)
  // Conductor: N diff(s)), shown only when N>0 (mirrors the 4C-3 bell). Its command opens
  // the most-recent diff (runShowDiff with one entry opens it directly). The bounded store
  // holds the rendered diffs; the content provider (registered in registerConductor) serves
  // them. Starts hidden.
  const diffBar = vscode.window.createStatusBarItem();
  diffBar.command = SHOW_DIFF_COMMAND;
  const diffStore = new DiffStore();

  // Q4.2 (Faz-Q): a native fleet-glance status-bar item — "$(server) N Conductors" — fed by the
  // SAME authed read client as the sessions tree (token in the header only). Clicking it reveals the
  // Conductor view. Shown only while connected with ≥1 project (updateFleetBar below); hidden
  // otherwise. The cockpit keeps its own in-surface bar; this is the NATIVE at-a-glance fleet read.
  const fleetBar = vscode.window.createStatusBarItem();
  fleetBar.command = REVEAL_CONTAINER_COMMAND;
  fleetBar.tooltip = "Conductor: open the Conductors view";

  // Faz-U: native at-a-glance claude-subscription usage (GET /usage; populated by the davinci
  // usage-probe from api.anthropic.com/api/oauth/usage). Compact 5-hour used % per subscription;
  // hover for 7-day + reset times. Hidden until the probe reports (and while disconnected).
  const usageBar = vscode.window.createStatusBarItem();
  usageBar.command = REVEAL_CONTAINER_COMMAND;

  // 4C-3: the host's OWN intervention-filtered WS subscription. The 4B-2 HostBridge only
  // forwards events to the webview while it's OPEN; this fires native notifications even
  // when the Conductor view is closed. TokenProvider reads SecretStorage per start (never
  // cached); wsBaseUrl is the ws-derived normalized base; the token rides only in the WS
  // URL — never in onIntervention (which carries project/task/reason) or a message.
  const notifier = new InterventionNotifier({
    wsBaseUrl: deriveWsUrl(normalizeBaseUrl(gatewayUrl)),
    tokenProvider: { getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)) },
    wsConnector,
    onIntervention: (intervention) => {
      pendingInterventions += 1;
      // 4C-3 upgrade: the toast's actions act on the intervention's own task — Approve it
      // in place (confirm-gated) or take it over by opening its native diff. `control` +
      // `diffStore` are in scope (defined in this activation); the closure runs only when an
      // intervention fires, by which point both are initialized.
      void handleIntervention(vscode, interventionBar, pendingInterventions, intervention, {
        approve: (project, task) => runApproveTask(vscode, control, project, task),
        openDiff: (project, task) => runShowDiffForTask(vscode, diffStore, project, task, fetchFullDiff),
      });
    },
  });

  // 4C-1b: the host's OWN diff-filtered WS subscription — same seams + same lifecycle as the
  // notifier. On each green-gate KindDiff it stores the rendered diff + bumps the
  // $(git-compare) count (QUIET — no toast; the status bar is the signal). Token rides only
  // in the WS URL; onDiff carries the token-free distilled TaskDiff.
  const diffObserver = new DiffObserver({
    wsBaseUrl: deriveWsUrl(normalizeBaseUrl(gatewayUrl)),
    tokenProvider: { getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)) },
    wsConnector,
    onDiff: (taskDiff) => handleDiff(diffStore, diffBar, taskDiff),
  });

  // N2: the native "Conductors" sessions tree. A host-side authed read client feeds it (the
  // token rides ONLY in the read client's Authorization header — never into the provider or a
  // TreeItem). Created before the ConnectionManager so onStateChange can refresh it on connect.
  const fleetReader = new FleetReadClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });
  const sessionsProvider = new SessionsTreeProvider(fleetReader, OPEN_COMMAND, OPEN_SESSION_COMMAND);

  // Q4.2: refresh the native fleet-glance bar from the SAME authed read the tree uses. listProjects
  // never throws (→ [] on any non-happy path), so an empty/failed read just hides the bar.
  const updateFleetBar = (): void => {
    void fleetReader.listProjects().then((projects) => {
      const label = fleetBarText(projects.length);
      if (label === "") {
        fleetBar.hide();
      } else {
        fleetBar.text = label;
        fleetBar.show();
      }
    });
  };

  // Faz-U: refresh the native usage bar from the SAME authed read client. getUsage never throws
  // (→ {} on any non-happy path), so an empty/failed read just hides the bar.
  const updateUsageBar = (): void => {
    void fleetReader.getUsage().then((usage) => {
      const { text, tooltip } = usageBarText(usage);
      if (text === "") {
        usageBar.hide();
      } else {
        usageBar.text = text;
        usageBar.tooltip = tooltip;
        usageBar.show();
      }
    });
  };

  // Q2 (ADR-0045): the native "Conductor Events" panel. A host-side watcher (REST backfill + live
  // WS, mirroring the notifier seams; token only in the Authorization header / `?token=` query)
  // feeds a flat TreeView in the panel area. Its onChange (ring change) refreshes the tree;
  // start()/stop() are tied to the link state below. eventsTree is assigned right after the
  // watcher, so the onChange closure reads it by reference (the watcher only fires after start()).
  const eventsWatcher = new EventsWatcher({
    restBaseUrl: normalizeBaseUrl(gatewayUrl),
    wsBaseUrl: deriveWsUrl(normalizeBaseUrl(gatewayUrl)),
    tokenProvider: { getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)) },
    wsConnector,
    // The closure reads eventsTree/activity surfaces only when a ring change fires (after
    // start()), by which point the consts below are initialized — forward reference is safe.
    onChange: () => {
      eventsTree.refresh();
      activityTree.refresh();
      updateNowBar();
      // PO: keep the "needs your review" badge live as decisions/merges arrive (in-flight guarded).
      updateReviewBadge();
    },
    // M2: an UNEXPECTED live-WS close while we believe we're connected means the gateway link
    // dropped (gateway restart / network blip / sleep). Kick the auto-reconnect loop instead of
    // sitting "disconnected" until a manual Reload. Guarded by manager.state so a deliberate
    // stop() (on disconnect) never self-triggers a reconnect (defense-in-depth with the watcher's
    // own stale-handle guard). reconnect/manager are forward refs, resolved at fire time.
    onClose: () => {
      if (manager.state === "connected") {
        reconnect?.kick();
      }
    },
  });
  const eventsTree = new EventsTreeProvider(eventsWatcher);

  const manager = new ConnectionManager({
    secrets: context.secrets,
    gateway: makeGatewayProbe(gatewayUrl),
    onStateChange: (state) => {
      updateConnectionStatusBar();
      // P3: publish the `conductor.connected` when-clause context key so the sessions-tree
      // context menus + the control keybindings only offer actions while the gateway is live.
      void vscode.commands.executeCommand("setContext", CONTEXT_CONNECTED, state === "connected");
      // 4C-3 + 4C-1b: tie the host WS subscriptions to the link state — open them on
      // "connected", tear them down otherwise. Extends the SAME onStateChange the status bar
      // uses (no second manager). start() is fire-and-forget (reads the token, never
      // throws/logs it); stop() is idempotent.
      if (state === "connected") {
        void notifier.start();
        void diffObserver.start();
        void eventsWatcher.start(); // Q2: backfill + live-WS the native events panel.
      } else {
        notifier.stop();
        diffObserver.stop();
        eventsWatcher.stop();
      }
      // N2: refetch the sessions tree on any link change (connected → projects/tasks appear;
      // otherwise it empties to the welcome view). The read client no-ops without a token.
      sessionsProvider.refresh();
      // Q4.2: refresh the native fleet glance with the tree — show the count when connected, hide
      // it otherwise (a disconnected fleet has nothing to glance at).
      if (state === "connected") {
        updateFleetBar();
        updateUsageBar();
      } else {
        fleetBar.hide();
        usageBar.hide();
      }
      // PO: refresh the "needs your review" activity-bar badge on every link change — it lights up
      // when connected and CLEARS on disconnect (the read no-ops to [] without a token → count 0).
      updateReviewBadge();
    },
  });

  // M2: the auto-reconnect loop. Its single job is to re-establish a DROPPED link: each attempt
  // re-validates the stored token via manager.restore(), which — on success — fires the
  // onStateChange("connected") path above and so restarts the watchers + refreshes the trees. It
  // is KICKED by the eventsWatcher's onClose (an unexpected WS drop) and by a failed restore-on-
  // activate while a token is stored; it backs off exponentially and stops once reconnected. With
  // NO stored token (the user disconnected) the attempt returns "stop", so it never spins.
  reconnect = new ReconnectController({
    attempt: async () => {
      if (!(await manager.hasStoredToken())) {
        return "stop";
      }
      await manager.restore();
      return manager.state === "connected" ? "connected" : "retry";
    },
    onPhase: () => updateConnectionStatusBar(),
  });
  context.subscriptions.push({ dispose: () => reconnect?.stop() });

  updateConnectionStatusBar();
  statusBar.command = CONNECT_COMMAND;
  statusBar.show();

  // 4C-2: the inline control commands' host-side authed client. Its TokenProvider reads
  // the bearer token from SecretStorage per request (never caches it); the normalized
  // gateway base URL matches the connect flow. The token rides only in the client's
  // Authorization header — it never reaches a message or a webview.
  const control = new ControlClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });

  // P2b (ADR-0041): the host-side authed client for a task's FULL-context diff. When the editor
  // opens a diff it first fetches this (whole-file native diff); on any miss (404 / pre-P2b task /
  // no token / network) the open flow keeps the bounded KindDiff patch (the P2a hunk view). Same
  // TokenProvider seam — the token rides only in the Authorization header, never to a webview.
  const diffContent = new DiffContentClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });
  const fetchFullDiff: FetchFullDiff = (project, task) => diffContent.fetchFullDiff(project, task);

  // L3 (ADR-0049): the host-side authed client that uploads/removes the claude credential in the
  // gateway's encrypted store. Same gateway-bearer TokenProvider seam (Authorization header only);
  // the claude token it uploads is read from SecretStorage by the command flow and sent only in
  // the request body — neither secret reaches a message or a webview.
  const credential = new CredentialClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });
  const pushCredential = vscode.commands.registerCommand(PUSH_CREDENTIAL_COMMAND, () => {
    void runPushCredential(vscode, context.secrets, credential);
  });
  const removeCredential = vscode.commands.registerCommand(REMOVE_CREDENTIAL_COMMAND, () => {
    void runRemoveCredential(vscode, credential);
  });

  // The Fleet view's bridge reads the token from SecretStorage (per request) and talks
  // to the gateway at the configured URL; the token never reaches the webview. The
  // extensionUri lets the provider build the cockpit bundle's webview resource URIs.
  const disposables = registerConductor(vscode, manager, gatewayUrl, control, diffStore, fetchFullDiff, {
    secrets: context.secrets,
    gatewayUrl,
    extensionUri: context.extensionUri,
  });

  // N2: register the native "Conductors" sessions TreeView + its refresh command (kept here, not
  // in registerConductor, so the tree's read client + refresh-on-connect wiring stay together).
  // createTreeView attaches to the package.json-contributed view.
  const sessionsView = vscode.window.createTreeView(SESSIONS_VIEW_ID, {
    treeDataProvider: sessionsProvider,
  });
  // PO "what needs me?" — set a numeric badge on the Conductor activity-bar icon = the count of
  // tasks awaiting approval or blocked (the board's Needs-Review lane). A guard coalesces the
  // rapid live-event triggers (one fetch at a time). Token rides only in the read client's header.
  let reviewBadgeInFlight = false;
  updateReviewBadge = (): void => {
    if (reviewBadgeInFlight) {
      return;
    }
    reviewBadgeInFlight = true;
    void fleetReader
      .listProjects()
      .then((projects) => Promise.all(projects.map((p) => fleetReader.listTasks(p.id))))
      .then((taskLists) => {
        const n = reviewBadgeValue(taskLists.flat());
        sessionsView.badge = n > 0 ? { value: n, tooltip: `${n} task${n === 1 ? "" : "s"} need your review` } : undefined;
      })
      .finally(() => {
        reviewBadgeInFlight = false;
      });
  };
  const refreshSessions = vscode.commands.registerCommand(REFRESH_SESSIONS_COMMAND, () => {
    sessionsProvider.refresh();
    updateFleetBar(); // Q4.2: keep the native fleet glance in step with a manual tree refresh.
    updateUsageBar();
    updateReviewBadge();
  });

  // Q2 (ADR-0045): the native "Conductor Events" panel tree. createTreeView attaches to the
  // package.json-contributed panel view; the watcher (started on connect) drives its content.
  const eventsView = vscode.window.createTreeView(EVENTS_VIEW_ID, {
    treeDataProvider: eventsTree,
  });

  // Native Diagnostics (Status) tree: a token-FREE, at-a-glance read of the editor↔gateway↔
  // credential wiring for debugging (why isn't the login reaching the gateway?). It gathers a
  // snapshot host-side — connection state, /healthz + /readyz reachability, bearer + claude-token
  // PRESENCE (booleans), and a token-free credential presence probe — then renders DiagRows. The
  // base URL is normalized once; all probes are best-effort (never throw into the tree).
  const diagBase = normalizeBaseUrl(gatewayUrl);
  const probe = async (path: string): Promise<"ok" | "fail" | "unknown"> => {
    if (!diagBase) {
      return "unknown";
    }
    try {
      const r = await fetch(`${diagBase}${path}`);
      return r.ok ? "ok" : "fail";
    } catch {
      return "fail";
    }
  };
  const collectSnapshot = async (): Promise<DiagSnapshot> => {
    const [healthz, readyz, bearer, claudeLocal, claudeOnGateway] = await Promise.all([
      probe("/healthz"),
      probe("/readyz"),
      context.secrets.get(GATEWAY_TOKEN_KEY).then((t) => t !== undefined && t !== ""),
      manager.hasClaudeToken(),
      credential.status(CLAUDE_CREDENTIAL_KIND),
    ]);
    return {
      gatewayUrl,
      connection: manager.state,
      healthz,
      readyz,
      bearerStored: bearer,
      claudeLocal,
      claudeOnGateway,
    };
  };
  const diagnosticsProvider = new DiagnosticsTreeProvider(async () =>
    buildDiagnosticRows(await collectSnapshot()),
  );
  const diagnosticsView = vscode.window.createTreeView(DIAGNOSTICS_VIEW_ID, {
    treeDataProvider: diagnosticsProvider,
  });
  const refreshDiagnostics = vscode.commands.registerCommand(REFRESH_DIAGNOSTICS_COMMAND, () => {
    diagnosticsProvider.refresh();
  });

  // "Now" live activity (digestible status): a status-bar headline + a compact Activity tree,
  // both summarizing the events-watcher ring into ONE plain-language line per active task
  // ("Geliştiriyor… 4dk · 2 dosya", "Onay bekliyor", "Tıkandı: …"). Updated on every events
  // change (the watcher's onChange above calls activityTree.refresh() + updateNowBar). Token-free
  // (the model reads only token-free FeedEvent fields). Clicking a row opens that task's session.
  const activityTree = new ActivityTreeProvider(() => summarizeActivity(eventsWatcher.events()));
  const activityView = vscode.window.createTreeView(ACTIVITY_VIEW_ID, { treeDataProvider: activityTree });
  const nowBar = vscode.window.createStatusBarItem();
  const updateNowBar = (): void => {
    const text = activityNowText(summarizeActivity(eventsWatcher.events()));
    if (text !== "") {
      nowBar.text = text;
      nowBar.tooltip = "Conductor — şu an ne yapıyor (tıkla: Command Center)";
      nowBar.command = OPEN_COMMAND;
      nowBar.show();
    } else {
      nowBar.hide();
    }
  };
  updateNowBar();

  // ── Live polling backbone (robustness) ────────────────────────────────────────────────────
  // The native surfaces are driven by WS push, but a WS can drop SILENTLY (no onClose to kick the
  // reconnect) and some state changes (e.g. a task removed out-of-band) emit no event at all — so
  // a purely event-driven tree goes stale until a manual Refresh / window reload. To make the UI
  // genuinely self-healing we POLL the gateway on a steady cadence WHILE CONNECTED and refresh
  // every native surface; WS push stays on top as a latency bonus. The reads are cheap authed JSON
  // GETs (and no-op without a token). This is what guarantees "everything flows correctly" with no
  // manual refresh — the UI converges to the gateway's truth within a few seconds.
  const LIVE_POLL_MS = 5000;
  let livePollTick = 0;
  const livePoll = setInterval(() => {
    if (manager.state !== "connected") {
      return; // nothing to poll when the link is down; reconnect drives the next "connected".
    }
    livePollTick++;
    sessionsProvider.refresh(); // tree converges (deleted tasks vanish, new/changed appear)
    updateFleetBar();
    updateReviewBadge();
    if (livePollTick % 3 === 0) {
      diagnosticsProvider.refresh(); // ~15s: re-probe /healthz, /readyz, token + claude login
    }
    if (livePollTick % 6 === 0) {
      updateUsageBar(); // ~30s: claude usage windows move slowly; no need for the 5s cadence
    }
  }, LIVE_POLL_MS);
  context.subscriptions.push({ dispose: () => clearInterval(livePoll) }, usageBar);

  // Push the intervention + diff status-bar items + dispose-wrappers that stop the host WS
  // subscriptions on deactivate (so the host sockets are torn down with the extension), plus the
  // N2 sessions tree (view + refresh command + the provider's change emitter).
  context.subscriptions.push(
    statusBar,
    interventionBar,
    diffBar,
    fleetBar,
    { dispose: () => notifier.stop() },
    { dispose: () => diffObserver.stop() },
    { dispose: () => eventsWatcher.stop() },
    sessionsView,
    refreshSessions,
    pushCredential,
    removeCredential,
    sessionsProvider,
    eventsView,
    eventsTree,
    diagnosticsView,
    refreshDiagnostics,
    diagnosticsProvider,
    activityView,
    activityTree,
    nowBar,
    ...disposables,
  );

  // N1 (ADR-0032 — Command Center = default surface): on startup, open the Command Center in the
  // MAIN editor area so the agent command center is the first surface the director sees (the Devin
  // Desktop model), not a collapsed sidebar. The `onStartupFinished` activation event + this
  // singleton open ARE the default-surface flip. Fire-and-forget — `conductor.open` is registered
  // above (in registerConductor); the panel is a singleton, so a later open just reveals it.
  void vscode.commands.executeCommand(OPEN_COMMAND);

  // N5 (Conductor layout default): on the FIRST launch only, also reveal the Conductor
  // activity-bar container so the native "Conductors" sessions rail is shown together with the
  // auto-opened Command Center — a fresh window lands on the Conductor layout, not a generic one.
  // Gated on globalState so later launches respect the user's last-used view (we never re-grab
  // the sidebar). Fire-and-forget; the flag is set first so a failed reveal still won't nag again.
  if (context.globalState.get(FIRST_LAUNCH_KEY) !== true) {
    void context.globalState.update(FIRST_LAUNCH_KEY, true);
    void vscode.commands.executeCommand(REVEAL_CONTAINER_COMMAND);
    // PO onboarding: on the very first launch, open the Getting Started tour so a new director is
    // GUIDED through connect → log in → create → watch → review, instead of facing an empty, silent
    // layout. One-time (same globalState gate); re-openable anytime via OPEN_TOUR_COMMAND.
    void vscode.commands.executeCommand("workbench.action.openWalkthrough", WALKTHROUGH_ID, false);
  }

  // Silent restore: re-validate a stored token (if any) and mirror the result onto the
  // status bar via onStateChange. Fire-and-forget; never throws, never logs the token.
  // M2: if restore leaves us NOT connected but a token IS stored, the gateway was down at startup
  // (a stored, still-valid token can't connect) — kick the auto-reconnect loop so the link comes
  // up on its own once the gateway is back, instead of waiting for a manual Connect/Reload.
  void manager.restore().then(async () => {
    if (manager.state !== "connected" && (await manager.hasStoredToken())) {
      reconnect?.kick();
    }
  });
}

/** Extension deactivation hook. Disposables are cleaned up via context.subscriptions. */
export function deactivate(): void {
  // Nothing to tear down explicitly; SecretStorage persists the token across sessions.
}

/**
 * Generates a 32-char alphanumeric nonce for the webview CSP from a CRYPTO RNG
 * (node:crypto randomBytes; the extension host is Node). A CSP nonce is a per-render
 * uniqueness token, not a secret, but using a crypto source (vs Math.random) is strictly
 * better hygiene and removes any predictability concern (review FAZ-4 finding). Kept
 * alphanumeric (modulo over the 62-char alphabet — bias is irrelevant for a CSP nonce).
 */
function makeNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const bytes = randomBytes(32);
  let nonce = "";
  for (let i = 0; i < 32; i++) {
    nonce += chars.charAt(bytes[i] % chars.length);
  }
  return nonce;
}
