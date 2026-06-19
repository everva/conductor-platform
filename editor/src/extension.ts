// Conductor Platform — VS Code extension entry (Faz-4 DALGA 4B).
//
// SCOPE: 4B-0 contributed the "Conductor" activity-bar view container + a placeholder
// webview + a command skeleton. 4B-1 wires the gateway CONNECTION + AUTH: the
// `conductor.connect` flow prompts for a token, validates it against the gateway, and
// stores it in VS Code SecretStorage; `conductor.disconnect` forgets it; a status-bar
// item mirrors the connection state; restore-on-activate re-validates a stored token.
// The host↔webview postMessage bridge (4B-2) and the 3B cockpit panels (4B-3) are
// still NOT here — the Fleet view stays a placeholder.
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
import { deriveWsUrl, makeGatewayProbe, normalizeBaseUrl, GATEWAY_TOKEN_KEY } from "./gateway";
import { ConnectionManager, type ConnectionState } from "./connection";
import { HostBridge, type WebviewLike } from "./bridge/hostBridge";
import { wsConnector } from "./bridge/wsConnector";

/** Command id for the gateway-connect action. */
export const CONNECT_COMMAND = "conductor.connect";

/** Command id for the gateway-disconnect action. */
export const DISCONNECT_COMMAND = "conductor.disconnect";

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
    registerWebviewViewProvider(
      viewId: string,
      provider: vscode.WebviewViewProvider,
    ): vscode.Disposable;
  };
}

/**
 * Builds the Fleet webview HTML. Pure (no vscode runtime needed) so tests can assert the
 * CSP + copy directly. `cspSource` is the webview's `cspSource` at runtime. The CSP is
 * strict: everything defaults to 'none'; only our own nonce'd <style>/<script> + images
 * from `cspSource` are allowed, and `connect-src 'none'` forbids the webview from doing
 * its OWN network — all data must arrive over the postMessage bridge (4B-2) from the
 * authed host. For 4B-2 there is still NO live <script> (the React bundle is 4B-3), so
 * the body stays a static "not connected" placeholder; the bridge is attached host-side
 * regardless, ready for the panels.
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
    <p class="status">Not connected. Run "Conductor: Connect to Gateway" to connect; live panels arrive in 4B-3.</p>
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

/** Configuration the FleetViewProvider needs to attach the bridge on resolve. */
export interface FleetViewConfig {
  readonly secrets: SecretStore;
  readonly gatewayUrl: string;
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
 * The Fleet view provider. On resolve it (a) enables scripts (the React panels in 4B-3
 * need them), (b) renders the strict-CSP placeholder (no live script yet — 4B-2 wires
 * only the transport), and (c) constructs + attaches a HostBridge over the webview so the
 * typed postMessage transport (the 4A-1 fork impl) is live. The bridge's dispose is
 * registered on the view's onDidDispose so the WS handles + listener are torn down.
 *
 * `config` is optional ONLY so a bare `new FleetViewProvider()` still constructs (used by
 * legacy unit tests of the static HTML); when absent, resolve renders the HTML but
 * attaches no bridge. The production path always supplies a config with the real factory.
 */
export class FleetViewProvider implements vscode.WebviewViewProvider {
  readonly #config: FleetViewConfig | undefined;

  constructor(config?: FleetViewConfig) {
    this.#config = config;
  }

  resolveWebviewView(webviewView: vscode.WebviewView): void {
    webviewView.webview.options = { enableScripts: true };
    webviewView.webview.html = placeholderHtml(webviewView.webview.cspSource);

    if (this.#config === undefined) {
      return; // No config (bare construction in a legacy test): HTML only, no bridge.
    }

    const factory =
      this.#config.bridgeFactory ??
      makeBridgeFactory(this.#config.secrets, this.#config.gatewayUrl);
    const bridge = factory(webviewView.webview);
    bridge.attach();
    // Tear the bridge down when the view goes away (closes WS handles + the listener).
    webviewView.onDidDispose(() => bridge.dispose());
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
  fleetConfig?: FleetViewConfig,
): vscode.Disposable[] {
  const connect = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void runConnect(api, manager, gatewayUrl);
  });
  const disconnect = api.commands.registerCommand(DISCONNECT_COMMAND, () => {
    void runDisconnect(api, manager);
  });
  // The Fleet view attaches the host↔webview bridge on resolve. When a config is
  // provided (the production path) the provider builds a real HostBridge; tests may omit
  // it (static-HTML provider) or pass one with a fake bridge factory.
  const provider = fleetConfig ? new FleetViewProvider(fleetConfig) : new FleetViewProvider();
  const fleetView = api.window.registerWebviewViewProvider(FLEET_VIEW_ID, provider);
  return [connect, disconnect, fleetView];
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

  // The Fleet view's bridge reads the token from SecretStorage (per request) and talks
  // to the gateway at the configured URL; the token never reaches the webview.
  const disposables = registerConductor(vscode, manager, gatewayUrl, {
    secrets: context.secrets,
    gatewayUrl,
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
 * Generates a 32-char alphanumeric nonce for the webview CSP. Uses Math.random — this
 * is a per-render uniqueness token for CSP, NOT a security secret (no token/secret is
 * handled by this function; the bearer token lives in SecretStorage, see connection.ts).
 */
function makeNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let nonce = "";
  for (let i = 0; i < 32; i++) {
    nonce += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return nonce;
}
