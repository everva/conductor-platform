// Deterministic, headless unit tests for the extension scaffold (4B-0). The `vscode`
// module is aliased to test/vscode-mock.ts (see vitest.config.ts), so these run with
// no electron and no display. We assert the registration calls against the mock's
// recorded invocations and assert the placeholder webview HTML/CSP directly.
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  commands,
  window,
  __reset,
  __makeWebviewView,
  type Disposable,
  type ExtensionContext,
} from "../test/vscode-mock";
import {
  CONNECT_COMMAND,
  FLEET_VIEW_ID,
  FleetViewProvider,
  activate,
  deactivate,
  placeholderHtml,
  registerConductor,
} from "./extension";

beforeEach(() => {
  __reset();
});

describe("registerConductor", () => {
  it("registers the conductor.connect command", () => {
    registerConductor({ commands, window });
    expect(commands.registerCommand).toHaveBeenCalledTimes(1);
    expect(commands.registerCommand).toHaveBeenCalledWith(
      CONNECT_COMMAND,
      expect.any(Function),
    );
  });

  it("registers the conductor.fleet webview view provider", () => {
    registerConductor({ commands, window });
    expect(window.registerWebviewViewProvider).toHaveBeenCalledTimes(1);
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
  });

  it("returns the created disposables", () => {
    const disposables = registerConductor({ commands, window });
    expect(disposables).toHaveLength(2);
    for (const d of disposables) {
      expect(typeof d.dispose).toBe("function");
    }
  });

  it("the connect handler shows the not-wired-yet information message", () => {
    registerConductor({ commands, window });
    const handler = commands.registerCommand.mock.calls[0]?.[1];
    expect(handler).toBeTypeOf("function");
    handler?.();
    expect(window.showInformationMessage).toHaveBeenCalledWith(
      "Conductor: connect is not wired yet (4B-1).",
    );
  });
});

describe("activate", () => {
  it("pushes the disposables onto context.subscriptions", () => {
    const subscriptions: Disposable[] = [];
    const context = { subscriptions } as ExtensionContext;
    activate(context as never);
    expect(subscriptions).toHaveLength(2);
    expect(commands.registerCommand).toHaveBeenCalledWith(
      CONNECT_COMMAND,
      expect.any(Function),
    );
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
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
