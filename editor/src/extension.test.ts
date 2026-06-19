// Deterministic, headless unit tests for the extension (4B-0 scaffold + 4B-1 connect/
// auth wiring). The `vscode` module is aliased to test/vscode-mock.ts (vitest.config),
// so these run with no electron and no display. Behavior is driven against the mock's
// recorded calls + a real ConnectionManager backed by an in-memory SecretStore and a
// fake gateway probe — no network. The token-leak proof for the manager lives in
// connection.test.ts; here we additionally assert no user-facing message carries it.
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  commands,
  window,
  __reset,
  __makeWebviewView,
  __makeSecretStorage,
  __makeExtensionUri,
  __setConfig,
  type Disposable,
  type ExtensionContext,
} from "../test/vscode-mock";
import {
  CONNECT_COMMAND,
  DISCONNECT_COMMAND,
  FLEET_VIEW_ID,
  type FleetViewConfig,
  FleetViewProvider,
  activate,
  deactivate,
  placeholderHtml,
  registerConductor,
  runConnect,
  runDisconnect,
  statusBarText,
  webviewHtml,
} from "./extension";
import { ConnectionManager, type SecretStore } from "./connection";
import { GATEWAY_TOKEN_KEY, type GatewayProbe, type TokenCheck } from "./gateway";

beforeEach(() => {
  __reset();
});

// Builds a REAL ConnectionManager backed by an in-memory secret map + a fake probe, so
// the command flows are exercised end-to-end without a network.
function makeManager(opts: {
  ready?: { ok: boolean; status: number };
  check?: TokenCheck;
  initial?: Record<string, string>;
}): { manager: ConnectionManager; map: Map<string, string> } {
  const map = new Map<string, string>(Object.entries(opts.initial ?? {}));
  const secrets: SecretStore = {
    get: (k) => Promise.resolve(map.get(k)),
    store: (k, v) => {
      map.set(k, v);
      return Promise.resolve();
    },
    delete: (k) => {
      map.delete(k);
      return Promise.resolve();
    },
  };
  const gateway: GatewayProbe = {
    pingReadyz: () => Promise.resolve(opts.ready ?? { ok: true, status: 200 }),
    validateToken: () => Promise.resolve(opts.check ?? "valid"),
  };
  return { manager: new ConnectionManager({ secrets, gateway }), map };
}

describe("registerConductor", () => {
  it("registers the connect + disconnect commands and the fleet view provider", () => {
    const { manager } = makeManager({});
    const disposables = registerConductor({ commands, window }, manager, "http://gw.test");

    expect(disposables).toHaveLength(3);
    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
  });
});

describe("runConnect", () => {
  it("prompts for the token with password:true and reports success (URL only, no token)", async () => {
    window.showInputBox.mockResolvedValueOnce("tok-secret");
    const { manager, map } = makeManager({ ready: { ok: true, status: 200 }, check: "valid" });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showInputBox).toHaveBeenCalledWith(expect.objectContaining({ password: true }));
    expect(map.get(GATEWAY_TOKEN_KEY)).toBe("tok-secret");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Connected to http://gw.test");
    // The success message must not contain the token.
    const msg = window.showInformationMessage.mock.calls[0]?.[0];
    expect(msg).not.toContain("tok-secret");
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("surfaces a token-free 'rejected' error on an invalid token and stores nothing", async () => {
    window.showInputBox.mockResolvedValueOnce("tok-bad");
    const { manager, map } = makeManager({ check: "unauthorized" });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showErrorMessage).toHaveBeenCalledWith("The gateway rejected that token.");
    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    const errMsg = window.showErrorMessage.mock.calls[0]?.[0];
    expect(errMsg).not.toContain("tok-bad");
  });

  it("surfaces an 'unreachable' error when the gateway is not ready", async () => {
    window.showInputBox.mockResolvedValueOnce("tok");
    const { manager } = makeManager({ ready: { ok: false, status: 503 } });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "Could not reach the gateway at http://gw.test.",
    );
  });

  it("is a quiet no-op when the token prompt is cancelled", async () => {
    window.showInputBox.mockResolvedValueOnce(undefined);
    const { manager, map } = makeManager({});

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).not.toHaveBeenCalled();
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("guides the user (and does NOT prompt) when the gateway URL is empty", async () => {
    const { manager } = makeManager({});

    await runConnect({ commands, window }, manager, "   ");

    expect(window.showInputBox).not.toHaveBeenCalled();
    expect(window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("conductor.gatewayUrl"),
    );
  });
});

describe("runDisconnect", () => {
  it("forgets the stored token and reports", async () => {
    const { manager, map } = makeManager({ initial: { [GATEWAY_TOKEN_KEY]: "tok" } });

    await runDisconnect({ commands, window }, manager);

    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).toHaveBeenCalledWith("Disconnected.");
  });
});

describe("statusBarText", () => {
  it("renders a distinct label per connection state", () => {
    expect(statusBarText("connected")).toContain("connected");
    expect(statusBarText("connecting")).toContain("connecting");
    expect(statusBarText("disconnected")).toContain("disconnected");
    expect(statusBarText("error")).toContain("error");
  });
});

describe("activate", () => {
  it("creates a status bar, registers commands + view, and pushes all disposables", async () => {
    __setConfig({ "conductor.gatewayUrl": "http://localhost:8080" });
    const subscriptions: Disposable[] = [];
    const context = {
      subscriptions,
      secrets: __makeSecretStorage(),
      extensionUri: __makeExtensionUri(),
    } as ExtensionContext;

    activate(context as never);
    // Let the fire-and-forget restore() settle (no token stored → no fetch).
    await Promise.resolve();
    await Promise.resolve();

    expect(window.createStatusBarItem).toHaveBeenCalledTimes(1);
    const statusBar = window.createStatusBarItem.mock.results[0]?.value as {
      text: string;
      show: () => void;
    };
    expect(statusBar.show).toHaveBeenCalled();
    expect(statusBar.text).toContain("disconnected");

    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
    // status bar + connect + disconnect + fleet view = 4 disposables.
    expect(subscriptions).toHaveLength(4);
  });
});

describe("deactivate", () => {
  it("is a no-op that does not throw", () => {
    expect(() => deactivate()).not.toThrow();
  });
});

// A FleetViewConfig for the provider tests. extensionUri is the mock's UriLike (the real
// type is vscode.Uri; the headless mock is structurally compatible), so cast at the seam.
function makeFleetConfig(over: Partial<FleetViewConfig> = {}): FleetViewConfig {
  return {
    secrets: __makeSecretStorage(),
    gatewayUrl: "http://gw.test",
    extensionUri: __makeExtensionUri() as unknown as FleetViewConfig["extensionUri"],
    ...over,
  };
}

describe("FleetViewProvider", () => {
  it("enables scripts and renders the no-config placeholder HTML on resolve (bare, no bridge)", () => {
    const view = __makeWebviewView("vscode-resource:");
    // Bare construction (no config) renders the static placeholder: scripts on, no bundle
    // (no extensionUri to build asset URIs), and NO bridge attached.
    new FleetViewProvider().resolveWebviewView(view as never);
    expect(view.webview.options.enableScripts).toBe(true);
    expect(view.webview.html).toContain("Not connected");
    // The legacy placeholder carries no live <script> (no bundle on the no-config path).
    expect(view.webview.html).not.toMatch(/<script/i);
    // No config → no bridge → the host never subscribes to the webview's messages.
    expect(view.webview.onDidReceiveMessage).not.toHaveBeenCalled();
  });

  it("loads the cockpit bundle, scopes localResourceRoots, attaches a bridge, and disposes on view-dispose", () => {
    const view = __makeWebviewView("vscode-resource:");
    const attach = vi.fn();
    const dispose = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach, dispose }));

    new FleetViewProvider(makeFleetConfig({ bridgeFactory })).resolveWebviewView(view as never);

    // 4B-3: scripts on + localResourceRoots scoped to dist/webview; the HTML nonce-loads
    // the bundled cockpit script + CSS (asWebviewUri-mapped under dist/webview).
    expect(view.webview.options.enableScripts).toBe(true);
    expect(view.webview.options.localResourceRoots?.[0]?.path).toBe("/ext/dist/webview");
    expect(view.webview.html).toContain('src="vscode-resource:/ext/dist/webview/main.js"');
    expect(view.webview.html).toContain('href="vscode-resource:/ext/dist/webview/main.css"');
    expect(view.webview.html).toContain('<div id="root"></div>');

    // The factory was handed the webview and the bridge was attached.
    expect(bridgeFactory).toHaveBeenCalledTimes(1);
    expect(bridgeFactory).toHaveBeenCalledWith(view.webview);
    expect(attach).toHaveBeenCalledTimes(1);
    expect(dispose).not.toHaveBeenCalled();

    // Closing the view tears the bridge down.
    view.__fireDispose();
    expect(dispose).toHaveBeenCalledTimes(1);
  });
});

describe("placeholderHtml", () => {
  it("contains a strict CSP meta with default-src 'none'", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toMatch(/Content-Security-Policy/);
    expect(html).toMatch(/default-src 'none'/);
  });

  it("contains the not-connected copy", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toContain("Not connected");
  });

  it("threads the cspSource into the style/img directives", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toContain("img-src vscode-resource:");
    expect(html).toContain("style-src 'nonce-");
  });

  it("does NOT contain any inline <script> (no script without a nonce)", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).not.toMatch(/<script/i);
  });

  it("uses a nonce on its inline <style> rather than allowing 'unsafe-inline'", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toMatch(/<style nonce="[A-Za-z0-9]{32}">/);
    expect(html).not.toContain("unsafe-inline");
  });

  it("produces a fresh nonce per render", () => {
    const randomSpy = vi.spyOn(Math, "random");
    const a = placeholderHtml("vscode-resource:");
    const b = placeholderHtml("vscode-resource:");
    randomSpy.mockRestore();
    const nonceA = /<style nonce="([A-Za-z0-9]{32})">/.exec(a)?.[1];
    const nonceB = /<style nonce="([A-Za-z0-9]{32})">/.exec(b)?.[1];
    expect(nonceA).toBeDefined();
    expect(nonceB).toBeDefined();
    expect(nonceA).not.toEqual(nonceB);
  });
});

describe("webviewHtml", () => {
  // A representative opts bundle (the runtime values resolveWebviewView passes).
  const opts = {
    cspSource: "vscode-resource:",
    nonce: "ABCDEF0123456789ABCDEF0123456789",
    scriptUri: "vscode-resource:/ext/dist/webview/main.js",
    styleUri: "vscode-resource:/ext/dist/webview/main.css",
  };

  it("locks default-src and connect-src to 'none' (webview does NO direct network)", () => {
    const html = webviewHtml(opts);
    expect(html).toMatch(/Content-Security-Policy/);
    expect(html).toContain("default-src 'none'");
    // connect-src 'none' is the enforcement that all data flows over the host bridge.
    expect(html).toContain("connect-src 'none'");
  });

  it("allows only a nonce'd script (script-src 'nonce-…', no 'unsafe-inline')", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`script-src 'nonce-${opts.nonce}'`);
    expect(html).not.toContain("unsafe-inline");
  });

  it("references the bundle script + style URIs and a #root mount, with no inline script body", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`src="${opts.scriptUri}"`);
    expect(html).toContain(`href="${opts.styleUri}"`);
    expect(html).toContain('<div id="root"></div>');
    // The ONLY <script> is the nonce'd external bundle — no inline JS between script tags.
    expect(html).toMatch(new RegExp(`<script nonce="${opts.nonce}" src="[^"]+"></script>`));
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)/i);
  });

  it("nonce-gates the stylesheet and threads cspSource into style/img/font directives", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`<link rel="stylesheet" nonce="${opts.nonce}"`);
    expect(html).toContain(`style-src 'nonce-${opts.nonce}' vscode-resource:`);
    expect(html).toContain("img-src vscode-resource: data:");
    expect(html).toContain("font-src vscode-resource:");
  });
});
