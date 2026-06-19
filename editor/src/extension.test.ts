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
  __setConfig,
  type Disposable,
  type ExtensionContext,
} from "../test/vscode-mock";
import {
  CONNECT_COMMAND,
  DISCONNECT_COMMAND,
  FLEET_VIEW_ID,
  FleetViewProvider,
  activate,
  deactivate,
  placeholderHtml,
  registerConductor,
  runConnect,
  runDisconnect,
  statusBarText,
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

describe("FleetViewProvider", () => {
  it("disables scripts and renders the placeholder HTML on resolve", () => {
    const view = __makeWebviewView("vscode-resource:");
    new FleetViewProvider().resolveWebviewView(view as never);
    expect(view.webview.options.enableScripts).toBe(false);
    expect(view.webview.html).toContain("Not connected");
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
