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
  ABORT_COMMAND,
  APPROVE_COMMAND,
  CONNECT_COMMAND,
  DISCONNECT_COMMAND,
  FLEET_VIEW_ID,
  PAUSE_COMMAND,
  RESUME_COMMAND,
  type Control,
  type ControlResult,
  type FleetViewConfig,
  FleetViewProvider,
  activate,
  deactivate,
  placeholderHtml,
  registerConductor,
  runConnect,
  runControl,
  runDisconnect,
  statusBarText,
  webviewHtml,
} from "./extension";
import { ConnectionManager, type SecretStore } from "./connection";
import { ControlListError } from "./controlClient";
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

// A fake Control (the slice of ControlClient runControl uses). `listProjects` resolves a
// fixed list (or rejects when `listError` is set); each action records its calls and
// resolves a fixed ControlResult. No token ever flows through it.
function makeControl(opts: {
  projects?: { id: string }[];
  listError?: ControlListError;
  result?: ControlResult;
} = {}): Control & {
  pause: ReturnType<typeof vi.fn>;
  resume: ReturnType<typeof vi.fn>;
  abort: ReturnType<typeof vi.fn>;
  approve: ReturnType<typeof vi.fn>;
} {
  const result: ControlResult = opts.result ?? { ok: true, body: {} };
  const listProjects = vi.fn(() =>
    opts.listError ? Promise.reject(opts.listError) : Promise.resolve(opts.projects ?? [{ id: "p1" }]),
  );
  return {
    listProjects,
    pause: vi.fn(() => Promise.resolve(result)),
    resume: vi.fn(() => Promise.resolve(result)),
    abort: vi.fn(() => Promise.resolve(result)),
    approve: vi.fn(() => Promise.resolve(result)),
  };
}

describe("registerConductor", () => {
  it("registers connect + disconnect + the 4 control commands and the fleet view provider", () => {
    const { manager } = makeManager({});
    const disposables = registerConductor(
      { commands, window },
      manager,
      "http://gw.test",
      makeControl(),
    );

    // connect + disconnect + pause + resume + abort + approve + fleet view = 7.
    expect(disposables).toHaveLength(7);
    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(PAUSE_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(RESUME_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(ABORT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(APPROVE_COMMAND, expect.any(Function));
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

describe("runControl", () => {
  // Asserts NO recorded vscode message (info/warn/error) contains the given needle.
  function expectNoMessageContains(needle: string): void {
    const all = [
      ...window.showInformationMessage.mock.calls,
      ...window.showWarningMessage.mock.calls,
      ...window.showErrorMessage.mock.calls,
    ];
    for (const call of all) {
      expect(String(call[0] ?? "")).not.toContain(needle);
    }
  }

  it("pause: picks a project then calls control.pause and reports — NO confirm prompt", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ projects: [{ id: "p1" }, { id: "p2" }] });

    await runControl({ commands, window }, control, "pause");

    expect(window.showQuickPick).toHaveBeenCalledWith(["p1", "p2"], expect.any(Object));
    expect(control.pause).toHaveBeenCalledWith("p1");
    expect(window.showWarningMessage).not.toHaveBeenCalled(); // pause is not confirm-gated.
    expect(window.showInformationMessage).toHaveBeenCalledWith("Pause requested for p1.");
  });

  it("resume: happy path calls control.resume with no confirm", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl();

    await runControl({ commands, window }, control, "resume");

    expect(control.resume).toHaveBeenCalledWith("p1");
    expect(window.showWarningMessage).not.toHaveBeenCalled();
    expect(window.showInformationMessage).toHaveBeenCalledWith("Resume requested for p1.");
  });

  it("abort: is confirm-gated — 'Yes' fires the action", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl();

    await runControl({ commands, window }, control, "abort");

    expect(window.showWarningMessage).toHaveBeenCalledWith(
      "Abort p1?",
      expect.objectContaining({ modal: true }),
      "Yes",
    );
    expect(control.abort).toHaveBeenCalledWith("p1");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Abort requested for p1.");
  });

  it("approve: is confirm-gated — cancelling the confirm does NOT fire the action", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce(undefined); // dismissed.
    const control = makeControl();

    await runControl({ commands, window }, control, "approve");

    expect(window.showWarningMessage).toHaveBeenCalledTimes(1);
    expect(control.approve).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("abort: a 'conflict' result surfaces the abort-specific warning", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes"); // confirm
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runControl({ commands, window }, control, "abort");

    expect(control.abort).toHaveBeenCalledWith("p1");
    // The confirm warning + the conflict warning were both shown; assert the conflict copy.
    const warnings = window.showWarningMessage.mock.calls.map((c) => String(c[0]));
    expect(warnings).toContain("No task is running to abort.");
  });

  it("approve: a 'conflict' result surfaces the approve-specific warning", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runControl({ commands, window }, control, "approve");

    const warnings = window.showWarningMessage.mock.calls.map((c) => String(c[0]));
    expect(warnings).toContain("No single task is awaiting approval (none or multiple).");
  });

  it("pause: cancelling the quick-pick is a quiet no-op (no action, no message)", async () => {
    window.showQuickPick.mockResolvedValueOnce(undefined);
    const control = makeControl();

    await runControl({ commands, window }, control, "pause");

    expect(control.pause).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
    expect(window.showWarningMessage).not.toHaveBeenCalled();
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("shows an info message and does nothing else when there are no projects", async () => {
    const control = makeControl({ projects: [] });

    await runControl({ commands, window }, control, "pause");

    expect(window.showInformationMessage).toHaveBeenCalledWith("No projects.");
    expect(window.showQuickPick).not.toHaveBeenCalled();
    expect(control.pause).not.toHaveBeenCalled();
  });

  it("guides to Connect (no token in the message) when listProjects is not-connected", async () => {
    const control = makeControl({ listError: new ControlListError("not-connected", 0) });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Connect to the gateway first.");
    expect(window.showQuickPick).not.toHaveBeenCalled();
  });

  it("guides to reconnect when listProjects is unauthorized", async () => {
    const control = makeControl({ listError: new ControlListError("unauthorized", 401) });

    await runControl({ commands, window }, control, "resume");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "The gateway rejected the stored token. Reconnect.",
    );
  });

  it("reports an unreachable gateway when listProjects is unreachable", async () => {
    const control = makeControl({ listError: new ControlListError("unreachable", 0) });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Could not reach the gateway.");
  });

  it("maps an 'unauthorized' action result to a reconnect error", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ result: { ok: false, reason: "unauthorized", status: 401 } });

    await runControl({ commands, window }, control, "resume");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "The gateway rejected the stored token. Reconnect.",
    );
  });

  it("maps an 'unreachable' action result to a gateway error", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ result: { ok: false, reason: "unreachable", status: 0 } });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Could not reach the gateway.");
  });

  it("never puts a token in any control message (leak guard across paths)", async () => {
    const TOKEN = "tok-MUST-NOT-LEAK";
    // Happy path.
    window.showQuickPick.mockResolvedValueOnce("p1");
    await runControl({ commands, window }, makeControl(), "pause");
    // Conflict path (confirm + conflict warning).
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    await runControl(
      { commands, window },
      makeControl({ result: { ok: false, reason: "conflict", status: 409 } }),
      "abort",
    );
    // Auth-failure paths.
    await runControl(
      { commands, window },
      makeControl({ listError: new ControlListError("unauthorized", 401) }),
      "pause",
    );
    expectNoMessageContains(TOKEN);
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
    // 4C-2 also registers the pause/resume/abort/approve commands.
    expect(commands.registerCommand).toHaveBeenCalledWith(PAUSE_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(APPROVE_COMMAND, expect.any(Function));
    // status bar + connect + disconnect + pause + resume + abort + approve + fleet view = 8.
    expect(subscriptions).toHaveLength(8);
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
