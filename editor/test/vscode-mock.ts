// Headless mock of the `vscode` module for vitest unit tests. The real module only
// exists inside a running VS Code/electron host; aliasing `vscode` to this file (see
// vitest.config.ts) lets the unit tests drive the extension's wiring deterministically
// with no electron and no display. It records registration calls so tests can assert
// them, and provides the minimal type surface extension.ts imports.
//
// NOTE: this lives under test/ (excluded from the main src typecheck) and is only
// pulled in by vitest via the alias, so it never ships in the bundle.
import { vi } from "vitest";

export interface Disposable {
  dispose(): void;
}

export interface ExtensionContext {
  subscriptions: Disposable[];
}

export interface Webview {
  options: { enableScripts?: boolean };
  html: string;
  readonly cspSource: string;
}

export interface WebviewView {
  readonly webview: Webview;
}

export interface WebviewViewProvider {
  resolveWebviewView(webviewView: WebviewView): void;
}

/** A no-op disposable used as the return value of registration calls. */
function makeDisposable(): Disposable {
  return { dispose: vi.fn() };
}

export const commands = {
  registerCommand: vi
    .fn<(command: string, callback: (...args: unknown[]) => unknown) => Disposable>()
    .mockImplementation(makeDisposable),
};

export const window = {
  showInformationMessage: vi
    .fn<(message: string) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  registerWebviewViewProvider: vi
    .fn<(viewId: string, provider: WebviewViewProvider) => Disposable>()
    .mockImplementation(makeDisposable),
};

/** Resets all recorded calls between tests (call in beforeEach). */
export function __reset(): void {
  commands.registerCommand.mockClear();
  window.showInformationMessage.mockClear();
  window.registerWebviewViewProvider.mockClear();
}

/** Builds a fresh fake WebviewView with a controllable cspSource. */
export function __makeWebviewView(cspSource = "vscode-resource:"): WebviewView {
  const webview: Webview = {
    options: {},
    html: "",
    cspSource,
  };
  return { webview };
}

export default { commands, window };
