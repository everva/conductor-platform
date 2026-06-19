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
// no electron, no display. The webview HTML is the pure `placeholderHtml`.
import * as vscode from "vscode";
import { randomBytes } from "node:crypto";
import { deriveWsUrl, makeGatewayProbe, normalizeBaseUrl, GATEWAY_TOKEN_KEY } from "./gateway";
import { ConnectionManager, type ConnectionState } from "./connection";
import { HostBridge, type WebviewLike } from "./bridge/hostBridge";
import { wsConnector } from "./bridge/wsConnector";
import { ControlClient, ControlListError, type ControlAction } from "./controlClient";

/** Command id for the gateway-connect action. */
export const CONNECT_COMMAND = "conductor.connect";

/** Command id for the gateway-disconnect action. */
export const DISCONNECT_COMMAND = "conductor.disconnect";

/** Command id for the pause-project control action (4C-2). */
export const PAUSE_COMMAND = "conductor.pause";

/** Command id for the resume-project control action (4C-2). */
export const RESUME_COMMAND = "conductor.resume";

/** Command id for the abort-running-task control action (4C-2). */
export const ABORT_COMMAND = "conductor.abort";

/** Command id for the approve-awaiting-task control action (4C-2). */
export const APPROVE_COMMAND = "conductor.approve";

/** View id of the placeholder Fleet webview view (contributed in package.json). */
export const FLEET_VIEW_ID = "conductor.fleet";

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
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    showErrorMessage(message: string): Thenable<string | undefined>;
    showInputBox(options?: vscode.InputBoxOptions): Thenable<string | undefined>;
    // 4C-2: the control commands let the user pick a project and confirm destructive
    // actions. Narrow signatures (the items the handlers actually pass) so the flows stay
    // assertable against the headless mock.
    showQuickPick(
      items: readonly string[],
      options?: vscode.QuickPickOptions,
    ): Thenable<string | undefined>;
    showWarningMessage(
      message: string,
      options: vscode.MessageOptions,
      item: string,
    ): Thenable<string | undefined>;
    registerWebviewViewProvider(
      viewId: string,
      provider: vscode.WebviewViewProvider,
    ): vscode.Disposable;
  };
}

/** The arguments the live-cockpit HTML builder needs, all already resolved by the caller
 * (resolveWebviewView): the runtime `cspSource`, a fresh per-render `nonce`, and the
 * `asWebviewUri`-mapped script + style URLs of the bundled cockpit. */
export interface WebviewHtmlOptions {
  readonly cspSource: string;
  readonly nonce: string;
  readonly scriptUri: string;
  readonly styleUri: string;
}

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
  const { cspSource, nonce, scriptUri, styleUri } = opts;
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
    <div id="root"></div>
    <script nonce="${nonce}" src="${scriptUri}"></script>
  </body>
</html>`;
}

/**
 * Builds the no-config Fleet HTML. Pure (no vscode runtime needed) so tests can assert the
 * CSP + copy directly. Reached ONLY on the legacy bare-construction path (a
 * `new FleetViewProvider()` with no config + no extensionUri): without the extensionUri we
 * can't build the bundle's resource URIs, so we render a minimal strict-CSP "not connected"
 * page (no live script). The production path always supplies a config and serves
 * `webviewHtml` (the live cockpit bundle). The CSP mirrors the live page minus the script
 * directive: everything defaults to 'none', only our nonce'd <style> applies, and
 * `connect-src 'none'` still forbids any webview-originated network.
 */
export function placeholderHtml(cspSource: string): string {
  const nonce = makeNonce();
  return `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta
      http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src 'nonce-${nonce}' ${cspSource}; img-src ${cspSource}; script-src 'nonce-${nonce}'; connect-src 'none';"
    />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <style nonce="${nonce}">
      body {
        font-family: var(--vscode-font-family);
        color: var(--vscode-foreground);
        padding: 12px;
      }
      .status {
        opacity: 0.8;
        font-size: 12px;
      }
    </style>
    <title>Conductor</title>
  </head>
  <body>
    <h3>Conductor</h3>
    <p class="status">Not connected. Run "Conductor: Connect to Gateway" to connect.</p>
  </body>
</html>`;
}

/**
 * Builds a HostBridge over a resolved webview. Injectable so the FleetViewProvider tests
 * can pass a fake (no real `ws`/fetch) and the production path uses the real wsConnector.
 * The TokenProvider reads the bearer token from SecretStorage per request (the bridge
 * never caches it); `baseUrl` is normalized + `wsBaseUrl` derived once here so the
 * webview's path/filter only ever extends the host's own (authed) base.
 */
export type BridgeFactory = (webview: WebviewLike) => { attach(): void; dispose(): void };

/** Configuration the FleetViewProvider needs to load the cockpit bundle + attach the
 * bridge on resolve. `extensionUri` is the installed extension root (context.extensionUri)
 * used to build the webview resource URIs of the bundled cockpit under dist/webview. */
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
 * The Fleet view provider. With a config (the production path) resolve (a) enables scripts
 * and scopes `localResourceRoots` to dist/webview so ONLY the bundle's assets load, (b)
 * serves `webviewHtml` — a strict-CSP page that nonce-loads the shared cockpit bundle
 * (4B-3) + its CSS via `asWebviewUri`, and (c) constructs + attaches a HostBridge over the
 * webview so the typed postMessage transport (the 4A-1 fork impl) is live for the bundle's
 * REST/WS. The bridge's dispose is registered on the view's onDidDispose so the WS handles
 * + listener are torn down.
 *
 * `config` is optional ONLY so a bare `new FleetViewProvider()` still constructs (used by
 * legacy unit tests of the static HTML); when absent, resolve renders the no-config
 * placeholder (scripts on, no bundle, no bridge — no extensionUri to build asset URIs).
 * The production path always supplies a config with the real factory + extensionUri.
 */
export class FleetViewProvider implements vscode.WebviewViewProvider {
  readonly #config: FleetViewConfig | undefined;
  // The bridge for the currently-resolved view. VS Code can call resolveWebviewView
  // more than once on one provider (a hidden view is torn down and re-resolved on
  // reveal when retainContextWhenHidden is off); dispose any predecessor on re-resolve
  // so an old HostBridge (with its WS handles + message listener) can never leak.
  #bridge: { dispose(): void } | undefined;

  constructor(config?: FleetViewConfig) {
    this.#config = config;
  }

  resolveWebviewView(webviewView: vscode.WebviewView): void {
    const config = this.#config;
    if (config === undefined) {
      // No config (bare construction in a legacy test): scripts on, placeholder HTML, no
      // bundle (no extensionUri for asset URIs), no bridge.
      webviewView.webview.options = { enableScripts: true };
      webviewView.webview.html = placeholderHtml(webviewView.webview.cspSource);
      return;
    }

    // Re-resolve safety: tear down the bridge from a previous resolve before building a
    // new one, so a re-resolve that didn't fire the prior view's onDidDispose can't leak.
    this.#bridge?.dispose();
    this.#bridge = undefined;

    const webview = webviewView.webview;
    const distRoot = vscode.Uri.joinPath(config.extensionUri, "dist", "webview");
    // Scope script/style loading to the bundle dir: the webview can't read arbitrary
    // extension files, only dist/webview (paired with the strict CSP).
    webview.options = { enableScripts: true, localResourceRoots: [distRoot] };

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
    // Tear the bridge down when the view goes away (closes WS handles + the listener).
    webviewView.onDidDispose(() => {
      bridge.dispose();
      if (this.#bridge === bridge) {
        this.#bridge = undefined;
      }
    });
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

/** The narrow control surface `runControl` drives — the slice of ControlClient it uses.
 * Declared as an interface so the unit tests pass a fake (no real fetch/token) while the
 * production path injects a real ControlClient. None of these carry the token out. */
export interface Control {
  listProjects(): Promise<{ id: string }[]>;
  pause(projectId: string): Promise<ControlResult>;
  resume(projectId: string): Promise<ControlResult>;
  abort(projectId: string): Promise<ControlResult>;
  approve(projectId: string, taskId?: string): Promise<ControlResult>;
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
): Promise<void> {
  const label = actionLabel(action);

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

  const pick = await api.window.showQuickPick(
    projects.map((p) => p.id),
    { placeHolder: "Select a project" },
  );
  if (pick === undefined) {
    return; // cancelled — quiet no-op.
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
 * Wires the extension's contributions onto the given (real or mocked) vscode API and
 * returns the created disposables. Kept separate from `activate` so unit tests can call
 * it with a mock + a manager and assert the registration + command behavior without a
 * running host. `gatewayUrl` is captured for the connect flow's URL.
 */
export function registerConductor(
  api: VscodeApi,
  manager: ConnectionManager,
  gatewayUrl: string,
  control: Control,
  fleetConfig?: FleetViewConfig,
): vscode.Disposable[] {
  const connect = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void runConnect(api, manager, gatewayUrl);
  });
  const disconnect = api.commands.registerCommand(DISCONNECT_COMMAND, () => {
    void runDisconnect(api, manager);
  });
  // 4C-2 inline control commands: each prompts for a project + (for abort/approve)
  // confirms, then fires the authed control POST host-side. The token stays in the
  // ControlClient's Authorization header — never in a message.
  const controlDisposables = (["pause", "resume", "abort", "approve"] as const).map((action) =>
    api.commands.registerCommand(controlCommandId(action), () => {
      void runControl(api, control, action);
    }),
  );
  // The Fleet view attaches the host↔webview bridge on resolve. When a config is
  // provided (the production path) the provider builds a real HostBridge; tests may omit
  // it (static-HTML provider) or pass one with a fake bridge factory.
  const provider = fleetConfig ? new FleetViewProvider(fleetConfig) : new FleetViewProvider();
  const fleetView = api.window.registerWebviewViewProvider(FLEET_VIEW_ID, provider);
  return [connect, disconnect, ...controlDisposables, fleetView];
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
  const manager = new ConnectionManager({
    secrets: context.secrets,
    gateway: makeGatewayProbe(gatewayUrl),
    onStateChange: (state) => {
      statusBar.text = statusBarText(state);
    },
  });
  statusBar.text = statusBarText(manager.state);
  statusBar.command = CONNECT_COMMAND;
  statusBar.show();

  // 4C-2: the inline control commands' host-side authed client. Its TokenProvider reads
  // the bearer token from SecretStorage per request (never caches it); the normalized
  // gateway base URL matches the connect flow. The token rides only in the client's
  // Authorization header — it never reaches a message or a webview.
  const control = new ControlClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });

  // The Fleet view's bridge reads the token from SecretStorage (per request) and talks
  // to the gateway at the configured URL; the token never reaches the webview. The
  // extensionUri lets the provider build the cockpit bundle's webview resource URIs.
  const disposables = registerConductor(vscode, manager, gatewayUrl, control, {
    secrets: context.secrets,
    gatewayUrl,
    extensionUri: context.extensionUri,
  });
  context.subscriptions.push(statusBar, ...disposables);

  // Silent restore: re-validate a stored token (if any) and mirror the result onto the
  // status bar via onStateChange. Fire-and-forget; never throws, never logs the token.
  void manager.restore();
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
