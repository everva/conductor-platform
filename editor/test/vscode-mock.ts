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

export interface SecretStorageLike {
  get(key: string): Thenable<string | undefined>;
  store(key: string, value: string): Thenable<void>;
  delete(key: string): Thenable<void>;
}

export interface ExtensionContext {
  subscriptions: Disposable[];
  secrets: SecretStorageLike;
}

export interface Webview {
  options: { enableScripts?: boolean };
  html: string;
  readonly cspSource: string;
  // 4B-2 bridge surface: the host posts messages to the webview and listens for messages
  // from it. The fake records posts and lets a test fire a received message via the
  // returned helper (see __makeWebviewView).
  postMessage(message: unknown): Thenable<boolean>;
  onDidReceiveMessage(listener: (message: unknown) => void): Disposable;
}

export interface WebviewView {
  readonly webview: Webview;
  // The view fires onDidDispose when closed; the provider tears the bridge down here.
  onDidDispose(listener: () => void): Disposable;
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

/** Minimal StatusBarItem surface the extension drives (text + show/hide/dispose). */
export interface StatusBarItem {
  text: string;
  tooltip: string | undefined;
  command: string | undefined;
  show(): void;
  hide(): void;
  dispose(): void;
}

export const window = {
  showInformationMessage: vi
    .fn<(message: string) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  showErrorMessage: vi
    .fn<(message: string) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  // showInputBox returns undefined by default (user cancelled); tests override the
  // resolved value to simulate a typed token. The token is a return value only —
  // the mock never records it anywhere a leak-guard test would inspect.
  showInputBox: vi
    .fn<(options?: unknown) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  registerWebviewViewProvider: vi
    .fn<(viewId: string, provider: WebviewViewProvider) => Disposable>()
    .mockImplementation(makeDisposable),
  createStatusBarItem: vi
    .fn<(...args: unknown[]) => StatusBarItem>()
    .mockImplementation(makeStatusBarItem),
};

/** Config values the mocked workspace.getConfiguration().get() reads. Keyed by the
 * full dotted id ("conductor.gatewayUrl") or the bare key ("gatewayUrl"). */
let configValues: Record<string, unknown> = {};

/** Sets the values the mocked configuration returns (call in a test before activate). */
export function __setConfig(values: Record<string, unknown>): void {
  configValues = values;
}

export const workspace = {
  getConfiguration: vi.fn((section?: string) => ({
    get<T>(key: string, dflt?: T): T | undefined {
      const full = section ? `${section}.${key}` : key;
      if (full in configValues) {
        return configValues[full] as T;
      }
      if (key in configValues) {
        return configValues[key] as T;
      }
      return dflt;
    },
  })),
};

/** Builds a fresh fake StatusBarItem with spy-able lifecycle methods. */
function makeStatusBarItem(): StatusBarItem {
  return {
    text: "",
    tooltip: undefined,
    command: undefined,
    show: vi.fn(),
    hide: vi.fn(),
    dispose: vi.fn(),
  };
}

/** In-memory SecretStorage (structurally satisfies connection.ts's SecretStore and
 * the slice of vscode.SecretStorage the host uses). Backed by a Map so a test can
 * pre-seed a stored token and assert store/delete happened. */
export function __makeSecretStorage(initial: Record<string, string> = {}): {
  get: (key: string) => Thenable<string | undefined>;
  store: (key: string, value: string) => Thenable<void>;
  delete: (key: string) => Thenable<void>;
  _map: Map<string, string>;
} {
  const map = new Map<string, string>(Object.entries(initial));
  return {
    get: (key) => Promise.resolve(map.get(key)),
    store: (key, value) => {
      map.set(key, value);
      return Promise.resolve();
    },
    delete: (key) => {
      map.delete(key);
      return Promise.resolve();
    },
    _map: map,
  };
}

/** Resets all recorded calls between tests (call in beforeEach). */
export function __reset(): void {
  commands.registerCommand.mockClear();
  window.showInformationMessage.mockClear();
  window.showErrorMessage.mockClear();
  window.showInputBox.mockClear();
  window.registerWebviewViewProvider.mockClear();
  window.createStatusBarItem.mockClear();
  workspace.getConfiguration.mockClear();
  configValues = {};
}

/**
 * Builds a fresh fake WebviewView with a controllable cspSource plus the 4B-2 bridge
 * surface. `postMessage`/`onDidReceiveMessage`/`onDidDispose` are spies; the returned
 * `__fire*` helpers let a test drive an inbound message / a dispose so the provider
 * wiring can be exercised headlessly. Posts are recorded on `webview.postMessage.mock`.
 */
export function __makeWebviewView(cspSource = "vscode-resource:"): WebviewView & {
  __fireMessage(message: unknown): void;
  __fireDispose(): void;
} {
  const messageListeners: ((message: unknown) => void)[] = [];
  const disposeListeners: (() => void)[] = [];
  const webview: Webview = {
    options: {},
    html: "",
    cspSource,
    postMessage: vi.fn<(message: unknown) => Thenable<boolean>>().mockResolvedValue(true),
    onDidReceiveMessage: vi.fn((listener: (message: unknown) => void) => {
      messageListeners.push(listener);
      return makeDisposable();
    }),
  };
  return {
    webview,
    onDidDispose: vi.fn((listener: () => void) => {
      disposeListeners.push(listener);
      return makeDisposable();
    }),
    __fireMessage(message: unknown) {
      for (const l of messageListeners) {
        l(message);
      }
    },
    __fireDispose() {
      for (const l of disposeListeners) {
        l();
      }
    },
  };
}

export default { commands, window, workspace };
