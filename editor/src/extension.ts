// Conductor Platform — VS Code extension entry (Faz-4 DALGA 4B, scaffold 4B-0).
//
// SCOPE (4B-0): an installable, activatable skeleton only. It contributes the
// "Conductor" activity-bar view container (package.json), registers a placeholder
// `conductor.connect` command, and registers a WebviewViewProvider for the
// `conductor.fleet` view that renders a minimal "not connected" placeholder. The
// real gateway connection + token SecretStorage (4B-1), the host↔webview postMessage
// bridge (4B-2), and the 3B cockpit panels (4B-3) are NOT implemented here.
//
// TESTABILITY: `activate` stays thin and delegates to `registerConductor`, which is
// written against a narrow structural slice of the vscode API (VscodeApi) so vitest
// can drive it with a mock — deterministically and headlessly, with no electron and
// no display. The webview HTML is produced by the pure `placeholderHtml` function so
// its CSP/copy can be asserted directly.
import * as vscode from "vscode";

/** Command id for the (placeholder) gateway-connect action. */
export const CONNECT_COMMAND = "conductor.connect";

/** View id of the placeholder Fleet webview view (contributed in package.json). */
export const FLEET_VIEW_ID = "conductor.fleet";

/**
 * Narrow structural slice of the vscode API surface this scaffold uses. Declaring
 * it explicitly (rather than depending on the full module shape) lets the unit tests
 * pass a small mock and lets us keep the wiring logic pure and assertable.
 */
export interface VscodeApi {
  readonly commands: {
    registerCommand(command: string, callback: (...args: unknown[]) => unknown): vscode.Disposable;
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    registerWebviewViewProvider(
      viewId: string,
      provider: vscode.WebviewViewProvider,
    ): vscode.Disposable;
  };
}

/**
 * Builds the placeholder webview HTML. Pure (no vscode runtime needed) so tests can
 * assert the CSP + copy directly. `cspSource` is the webview's `cspSource` value at
 * runtime (an opaque scheme the host trusts for resources); for the placeholder we
 * only allow our own nonce'd <style> and otherwise lock everything to 'none'. There
 * is intentionally NO script here yet — content/data arrive over the postMessage
 * bridge in 4B-2/4B-3.
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
    <p class="status">Not connected. Run "Conductor: Connect to Gateway" to connect (coming in 4B-1).</p>
  </body>
</html>`;
}

/**
 * The placeholder Fleet view provider. For 4B-0 it only enables scripts-off webview
 * options and renders the static "not connected" placeholder. The typed postMessage
 * transport (4A-1 fork impl) and the real cockpit panels (3B reuse) land in 4B-2/4B-3.
 */
export class FleetViewProvider implements vscode.WebviewViewProvider {
  resolveWebviewView(webviewView: vscode.WebviewView): void {
    webviewView.webview.options = { enableScripts: false };
    webviewView.webview.html = placeholderHtml(webviewView.webview.cspSource);
  }
}

/**
 * Wires the extension's contributions onto the given (real or mocked) vscode API and
 * returns the created disposables. Kept separate from `activate` so unit tests can
 * call it with a mock and assert the registration calls without a running host.
 */
export function registerConductor(api: VscodeApi): vscode.Disposable[] {
  const connectCommand = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void api.window.showInformationMessage(
      "Conductor: connect is not wired yet (4B-1).",
    );
  });

  const fleetView = api.window.registerWebviewViewProvider(
    FLEET_VIEW_ID,
    new FleetViewProvider(),
  );

  return [connectCommand, fleetView];
}

/** Extension activation entry point (manifest `main` → esbuild bundle). */
export function activate(context: vscode.ExtensionContext): void {
  const disposables = registerConductor(vscode);
  context.subscriptions.push(...disposables);
}

/** Extension deactivation hook. Disposables are cleaned up via context.subscriptions. */
export function deactivate(): void {
  // Nothing to tear down explicitly in 4B-0.
}

/**
 * Generates a 32-char alphanumeric nonce for the webview CSP. Uses Math.random — this
 * is a per-render uniqueness token for CSP, NOT a security secret (no token/secret is
 * handled in 4B-0; SecretStorage discipline arrives in 4B-1).
 */
function makeNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let nonce = "";
  for (let i = 0; i < 32; i++) {
    nonce += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return nonce;
}
