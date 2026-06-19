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
import { makeGatewayProbe } from "./gateway";
import { ConnectionManager, type ConnectionState } from "./connection";

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
 * Builds the placeholder webview HTML. Pure (no vscode runtime needed) so tests can
 * assert the CSP + copy directly. `cspSource` is the webview's `cspSource` at runtime;
 * for the placeholder we only allow our own nonce'd <style> and lock everything else to
 * 'none'. There is intentionally NO script yet — content/data arrive over the
 * postMessage bridge in 4B-2/4B-3.
 */
export function placeholderHtml(cspSource: string): string {
  const nonce = makeNonce();
  return `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta
      http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src 'nonce-${nonce}' ${cspSource}; img-src ${cspSource};"
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
 * The placeholder Fleet view provider. For 4B-0/4B-1 it only enables scripts-off
 * webview options and renders the static "not connected" placeholder. The typed
 * postMessage transport (4A-1 fork impl) and the real cockpit panels (3B reuse) land
 * in 4B-2/4B-3.
 */
export class FleetViewProvider implements vscode.WebviewViewProvider {
  resolveWebviewView(webviewView: vscode.WebviewView): void {
    webviewView.webview.options = { enableScripts: false };
    webviewView.webview.html = placeholderHtml(webviewView.webview.cspSource);
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
): vscode.Disposable[] {
  const connect = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void runConnect(api, manager, gatewayUrl);
  });
  const disconnect = api.commands.registerCommand(DISCONNECT_COMMAND, () => {
    void runDisconnect(api, manager);
  });
  const fleetView = api.window.registerWebviewViewProvider(FLEET_VIEW_ID, new FleetViewProvider());
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

  const disposables = registerConductor(vscode, manager, gatewayUrl);
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
