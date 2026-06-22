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

/** The slice of vscode.Memento the extension uses (N5 first-launch flag): get + update. */
export interface GlobalStateLike {
  get(key: string): unknown;
  update(key: string, value: unknown): Thenable<void>;
}

export interface ExtensionContext {
  subscriptions: Disposable[];
  secrets: SecretStorageLike;
  // The installed extension root; the FleetViewProvider (4B-3) joins it to build the
  // cockpit bundle's webview resource URIs. A fake Uri suffices for the headless tests.
  extensionUri: UriLike;
  // N5: the globalState memento backing the first-launch layout-reveal flag.
  globalState: GlobalStateLike;
}

// Minimal structural stand-in for vscode.Uri: just enough for joinPath + asWebviewUri +
// toString in the FleetViewProvider. The real Uri carries more; tests only need the path.
export interface UriLike {
  readonly path: string;
  toString(): string;
}

export interface Webview {
  options: { enableScripts?: boolean; localResourceRoots?: readonly UriLike[] };
  html: string;
  readonly cspSource: string;
  // 4B-3: maps an on-disk extension Uri to a webview-loadable URI. The fake returns a
  // stable `vscode-resource:`-prefixed Uri so the HTML can be asserted deterministically.
  asWebviewUri(uri: UriLike): UriLike;
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

/** Minimal structural WebviewPanel (the createWebviewPanel return). Like a WebviewView but it
 * lives in the editor area, can be `reveal`ed to the front, and is `dispose`d explicitly. */
export interface WebviewPanelLike {
  readonly webview: Webview;
  reveal(column?: number): void;
  onDidDispose(listener: () => void): Disposable;
  dispose(): void;
}

/** vscode.ViewColumn stand-in (numeric enum). The Command Center opens in `One` (the main
 * editor area); only the members the extension references are provided. */
export const ViewColumn = { Active: -1, Beside: -2, One: 1, Two: 2, Three: 3 } as const;

// ── N2 native TreeView primitives ──────────────────────────────────────────────────────

/** Minimal vscode.EventEmitter stand-in: `event` registers a listener (returns a disposable);
 * `fire` invokes all current listeners. Enough for the tree's onDidChangeTreeData. */
export class EventEmitter<T> {
  #listeners: ((e: T) => void)[] = [];
  readonly event = (listener: (e: T) => void): Disposable => {
    this.#listeners.push(listener);
    return {
      dispose: () => {
        this.#listeners = this.#listeners.filter((l) => l !== listener);
      },
    };
  };
  fire(data: T): void {
    for (const l of this.#listeners) {
      l(data);
    }
  }
  dispose(): void {
    this.#listeners = [];
  }
}

/** vscode.ThemeColor stand-in: just the id (a test reads `.id`). */
export class ThemeColor {
  readonly id: string;
  constructor(id: string) {
    this.id = id;
  }
}

/** vscode.ThemeIcon stand-in: the codicon id + optional color (a test reads both). */
export class ThemeIcon {
  readonly id: string;
  readonly color?: ThemeColor | undefined;
  constructor(id: string, color?: ThemeColor) {
    this.id = id;
    this.color = color;
  }
}

/** vscode.TreeItemCollapsibleState stand-in (numeric enum). */
export const TreeItemCollapsibleState = { None: 0, Collapsed: 1, Expanded: 2 } as const;

/** vscode.TreeItem stand-in: the settable fields the SessionsTreeProvider assigns. */
export class TreeItem {
  label: string;
  collapsibleState: number;
  iconPath?: ThemeIcon;
  description?: string;
  tooltip?: string;
  contextValue?: string;
  command?: { command: string; title: string };
  constructor(label: string, collapsibleState: number = TreeItemCollapsibleState.None) {
    this.label = label;
    this.collapsibleState = collapsibleState;
  }
}

/** A no-op disposable used as the return value of registration calls. */
function makeDisposable(): Disposable {
  return { dispose: vi.fn() };
}

export const commands = {
  registerCommand: vi
    .fn<(command: string, callback: (...args: unknown[]) => unknown) => Disposable>()
    .mockImplementation(makeDisposable),
  // 4C-3: reveals the activity-bar container via `workbench.view.extension.conductor`.
  // P2a: also opens a native diff via the built-in `vscode.diff` (left, right, title, options).
  // Variadic so both call shapes record; resolves undefined by default. No token flows here.
  executeCommand: vi
    .fn<(command: string, ...args: unknown[]) => Thenable<unknown>>()
    .mockResolvedValue(undefined),
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
  // 4C-2 control commands: showQuickPick resolves a picked string (default undefined =
  // user cancelled); tests override the resolved value to simulate a selected project.
  showQuickPick: vi
    .fn<(items: readonly string[], options?: unknown) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  // showWarningMessage resolves the chosen item (default undefined = dismissed); tests
  // override it to "Yes" to confirm a destructive 4C-2 action, or to "Open Conductor" for
  // the 4C-3 intervention toast (which calls it as `(message, "Open Conductor")` — no
  // options object). The variadic signature accepts both forms. No token ever flows here.
  showWarningMessage: vi
    .fn<(message: string, options?: unknown, ...items: string[]) => Thenable<string | undefined>>()
    .mockResolvedValue(undefined),
  registerWebviewViewProvider: vi
    .fn<(viewId: string, provider: WebviewViewProvider) => Disposable>()
    .mockImplementation(makeDisposable),
  // N0: opens an editor-area WebviewPanel (the Command Center). Records the call so a test can
  // assert the viewType/title/column/options, and returns a fresh fake panel; the singleton
  // logic lives in CommandCenterPanel, driven against this.
  createWebviewPanel: vi
    .fn<
      (viewType: string, title: string, showOptions: unknown, options?: unknown) => WebviewPanelLike
    >()
    .mockImplementation(() => __makeWebviewPanel()),
  // N2: registers the native "Conductors" sessions TreeView. Records the call so a test asserts
  // the viewId + provider; returns a disposable (the real TreeView is disposable).
  createTreeView: vi
    .fn<(viewId: string, options: { treeDataProvider: unknown }) => { dispose(): void }>()
    .mockImplementation(() => ({ dispose: vi.fn() })),
  createStatusBarItem: vi
    .fn<(...args: unknown[]) => StatusBarItem>()
    .mockImplementation(makeStatusBarItem),
  // 4C-1b: opening a rendered diff document. Resolves a fake TextEditor by default; tests
  // assert it was called with the opened document + preview options. No token flows here.
  showTextDocument: vi
    .fn<(document: TextDocumentLike, options?: unknown) => Thenable<TextEditorLike>>()
    .mockImplementation((document) => Promise.resolve({ document })),
};

/** Minimal structural stand-in for vscode.TextDocument: the diff flow only reads `uri`
 * (to assert which diff was opened) + passes the doc through to showTextDocument /
 * setTextDocumentLanguage. The content provider supplies the body the real host would. */
export interface TextDocumentLike {
  readonly uri: UriLike;
}

/** Minimal structural stand-in for vscode.TextEditor: the diff flow ignores the returned
 * editor (it just awaits it), so it only needs to carry the document. */
export interface TextEditorLike {
  readonly document: TextDocumentLike;
}

/** 4C-1b: the read-only diff render. `registerTextDocumentContentProvider` records the
 * scheme + provider (a test can call provideTextDocumentContent directly);
 * `openTextDocument(uri)` resolves a fake document carrying that uri so the flow can be
 * asserted (the real host would fill it from the provider). */
export const languages = {
  // The diff flow calls this to force the `diff` language; resolves the doc unchanged.
  setTextDocumentLanguage: vi
    .fn<(document: TextDocumentLike, languageId: string) => Thenable<TextDocumentLike>>()
    .mockImplementation((document) => Promise.resolve(document)),
};

/** Config values the mocked workspace.getConfiguration().get() reads. Keyed by the
 * full dotted id ("conductor.gatewayUrl") or the bare key ("gatewayUrl"). */
let configValues: Record<string, unknown> = {};

/** Sets the values the mocked configuration returns (call in a test before activate). */
export function __setConfig(values: Record<string, unknown>): void {
  configValues = values;
}

/** Minimal structural stand-in for vscode.TextDocumentContentProvider (4C-1b): the diff
 * render registers one of these and the host calls provideTextDocumentContent(uri). */
export interface TextDocumentContentProviderLike {
  provideTextDocumentContent(uri: UriLike): string | undefined | null;
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
  // 4C-1b: records the scheme + provider so a test can drive provideTextDocumentContent.
  registerTextDocumentContentProvider: vi
    .fn<(scheme: string, provider: TextDocumentContentProviderLike) => Disposable>()
    .mockImplementation(makeDisposable),
  // 4C-1b: resolves a fake document carrying the requested uri so the diff flow can assert
  // WHICH diff URI was opened (the real host fills the body from the registered provider).
  openTextDocument: vi
    .fn<(uri: UriLike) => Thenable<TextDocumentLike>>()
    .mockImplementation((uri) => Promise.resolve({ uri })),
};

/** Builds a fake UriLike with a given path (toString() returns the path verbatim). */
function makeUri(path: string): UriLike {
  return { path, toString: () => path };
}

/** Minimal `vscode.Uri` surface the FleetViewProvider uses: `joinPath` appends path
 * segments. Enough for the headless HTML/URI assertions; the real Uri is richer. */
export const Uri = {
  joinPath(base: UriLike, ...segments: string[]): UriLike {
    const joined = [base.path, ...segments].join("/");
    return makeUri(joined);
  },
  // 4C-1b: parse a `conductor-diff:` URI string. The fake keeps the full string verbatim as
  // both `path` and toString() so the diff store's string key round-trips (the store keys on
  // toString()); a real Uri would split scheme/path, but the diff flow only needs the key.
  parse(value: string): UriLike {
    return makeUri(value);
  },
};

/** A fresh fake extension-root Uri for tests that build a FleetViewConfig. */
export function __makeExtensionUri(path = "/ext"): UriLike {
  return makeUri(path);
}

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

/** In-memory globalState memento (Map-backed) for the N5 first-launch flag. Pre-seed via
 * `initial` to simulate a returning user; assert state via `_map`. */
export function __makeGlobalState(initial: Record<string, unknown> = {}): GlobalStateLike & {
  _map: Map<string, unknown>;
} {
  const map = new Map<string, unknown>(Object.entries(initial));
  return {
    get: (key) => map.get(key),
    update: (key, value) => {
      map.set(key, value);
      return Promise.resolve();
    },
    _map: map,
  };
}

/** Resets all recorded calls between tests (call in beforeEach). */
export function __reset(): void {
  commands.registerCommand.mockClear();
  commands.executeCommand.mockClear();
  window.showInformationMessage.mockClear();
  window.showErrorMessage.mockClear();
  window.showInputBox.mockClear();
  window.showQuickPick.mockClear();
  window.showWarningMessage.mockClear();
  window.registerWebviewViewProvider.mockClear();
  window.createWebviewPanel.mockClear();
  window.createTreeView.mockClear();
  window.createStatusBarItem.mockClear();
  window.showTextDocument.mockClear();
  workspace.getConfiguration.mockClear();
  workspace.registerTextDocumentContentProvider.mockClear();
  workspace.openTextDocument.mockClear();
  languages.setTextDocumentLanguage.mockClear();
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
    // Map an on-disk Uri to a deterministic webview URI so the HTML is assertable:
    // `${cspSource}${path}` (e.g. "vscode-resource:/ext/dist/webview/main.js").
    asWebviewUri: vi.fn((uri: UriLike) => makeUri(`${cspSource}${uri.path}`)),
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

/**
 * Builds a fresh fake WebviewPanel (createWebviewPanel return) carrying the 4B-2 bridge
 * surface. `dispose()` and the returned `__fireDispose()` both run the onDidDispose listeners
 * so a test can drive a panel close; `reveal`/`dispose` are spies. Mirrors __makeWebviewView
 * but for the editor-area panel.
 */
export function __makeWebviewPanel(cspSource = "vscode-resource:"): WebviewPanelLike & {
  reveal: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  __fireDispose(): void;
  __fireMessage(message: unknown): void;
} {
  const messageListeners: ((message: unknown) => void)[] = [];
  const disposeListeners: (() => void)[] = [];
  const webview: Webview = {
    options: {},
    html: "",
    cspSource,
    asWebviewUri: vi.fn((uri: UriLike) => makeUri(`${cspSource}${uri.path}`)),
    postMessage: vi.fn<(message: unknown) => Thenable<boolean>>().mockResolvedValue(true),
    onDidReceiveMessage: vi.fn((listener: (message: unknown) => void) => {
      messageListeners.push(listener);
      return makeDisposable();
    }),
  };
  const fire = (): void => {
    for (const l of disposeListeners) {
      l();
    }
  };
  return {
    webview,
    reveal: vi.fn(),
    dispose: vi.fn(fire),
    onDidDispose: vi.fn((listener: () => void) => {
      disposeListeners.push(listener);
      return makeDisposable();
    }),
    __fireDispose: fire,
    __fireMessage(message: unknown) {
      for (const l of messageListeners) {
        l(message);
      }
    },
  };
}

export default {
  commands,
  window,
  workspace,
  languages,
  Uri,
  ViewColumn,
  EventEmitter,
  ThemeColor,
  ThemeIcon,
  TreeItem,
  TreeItemCollapsibleState,
};
