// Conductor Platform — VS Code extension entry (Faz-4 DALGA 4B).
//
// SCOPE: 4B-0 contributed the "Conductor" activity-bar view container + a placeholder
// webview + a command skeleton. 4B-1 wires the gateway CONNECTION + AUTH: the
// `conductor.connect` flow prompts for a token, validates it against the gateway, and
// stores it in VS Code SecretStorage; `conductor.disconnect` forgets it; a status-bar
// item mirrors the connection state; restore-on-activate re-validates a stored token.
// 4B-2 added the host↔webview postMessage bridge (the fork transport seam). 4B-3 loads
// the SHARED 3B cockpit bundle into the Fleet view: the provider now serves a strict-CSP
// HTML page that nonce-loads dist/webview/main.js (the React bundle built from
// web/src/cockpit.ts via esbuild, ADR-0029) over that live bridge — the panels render
// real data, token-free (auth host-side).
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
import { randomBytes } from "node:crypto";
import { deriveWsUrl, makeGatewayProbe, normalizeBaseUrl, GATEWAY_TOKEN_KEY } from "./gateway";
import { ConnectionManager, type ConnectionState } from "./connection";
import { HostBridge, type WebviewLike } from "./bridge/hostBridge";
import { wsConnector } from "./bridge/wsConnector";
import { ControlClient, ControlListError, type ControlAction } from "./controlClient";
import { InterventionNotifier, type Intervention } from "./notifier";
import { DiffObserver, type TaskDiff } from "./diffObserver";

/** Command id for the gateway-connect action. */
export const CONNECT_COMMAND = "conductor.connect";

/** Command id for the gateway-disconnect action. */
export const DISCONNECT_COMMAND = "conductor.disconnect";

/** Command id for the pause-project control action (4C-2). */
export const PAUSE_COMMAND = "conductor.pause";

/** Command id for the resume-project control action (4C-2). */
export const RESUME_COMMAND = "conductor.resume";

/** Command id for the abort-running-task control action (4C-2). */
export const ABORT_COMMAND = "conductor.abort";

/** Command id for the approve-awaiting-task control action (4C-2). */
export const APPROVE_COMMAND = "conductor.approve";

/** Command id for the show-task-diff action (4C-1b). Opens the most-recent task diff (or a
 * quick-pick when several are pending) in a native read-only diff document. */
export const SHOW_DIFF_COMMAND = "conductor.showDiff";

/** Custom URI scheme for the read-only diff virtual documents (4C-1b). Registered with a
 * TextDocumentContentProvider; the diff body is served from a bounded in-memory store. */
export const DIFF_SCHEME = "conductor-diff";

/** View id of the placeholder Fleet webview view (contributed in package.json). */
export const FLEET_VIEW_ID = "conductor.fleet";

/** Activity-bar view CONTAINER id (package.json `viewsContainers.activitybar`). The 4C-3
 * notification's "Open Conductor" action reveals it via
 * `workbench.view.extension.<containerId>`. */
export const VIEW_CONTAINER_ID = "conductor";

/** The built-in VS Code command that reveals an activity-bar view container, suffixed with
 * the container id. Picking "Open Conductor" on an intervention toast runs this (4C-3). */
export const REVEAL_CONTAINER_COMMAND = `workbench.view.extension.${VIEW_CONTAINER_ID}`;

/** The action label on the intervention notification; picking it reveals the view (4C-3). */
export const OPEN_CONDUCTOR_ACTION = "Open Conductor";

/** Intervention-toast action: approve the held task in place (confirm-gated → a real merge).
 * The director acts on the gate-green task without first opening the panel (4C-3 upgrade). */
export const APPROVE_ACTION = "Approve";

/** Intervention-toast action: open THAT task's most-recent native diff (the take-over-into-
 * the-editor path) so the director can inspect the change hands-on (4C-3 upgrade / "take over"). */
export const OPEN_DIFF_ACTION = "Open diff";

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
    // 4C-3: the intervention notification's "Open Conductor" action reveals the activity-bar
    // container via the built-in `workbench.view.extension.conductor` command. Narrow
    // signature (the handler passes only the command id) so the flow stays assertable.
    executeCommand(command: string): Thenable<unknown>;
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    showErrorMessage(message: string): Thenable<string | undefined>;
    showInputBox(options?: vscode.InputBoxOptions): Thenable<string | undefined>;
    // 4C-2: the control commands let the user pick a project and confirm destructive
    // actions. Narrow signatures (the items the handlers actually pass) so the flows stay
    // assertable against the headless mock. The two showWarningMessage overloads cover the
    // confirm-modal form (4C-2: options + item) and the 4C-3 intervention toast (message +
    // action label, no options object) — both resolve the chosen item.
    showQuickPick(
      items: readonly string[],
      options?: vscode.QuickPickOptions,
    ): Thenable<string | undefined>;
    showWarningMessage(
      message: string,
      options: vscode.MessageOptions,
      item: string,
    ): Thenable<string | undefined>;
    // Rest form covers the single-action toast (4C-2) AND the 4C-3 multi-action
    // intervention toast (Approve / Open diff / Open Conductor).
    showWarningMessage(message: string, ...items: string[]): Thenable<string | undefined>;
    registerWebviewViewProvider(
      viewId: string,
      provider: vscode.WebviewViewProvider,
    ): vscode.Disposable;
  };
}

/**
 * The ADDITIONAL narrow vscode surface the 4C-1b native-diff flow uses, kept SEPARATE from
 * {@link VscodeApi} so the connect/control/intervention flows (which only need commands +
 * window) stay assertable against `{ commands, window }` and don't have to supply these.
 * `registerConductor`/`activate` pass the real `vscode`, which satisfies both. Uses minimal
 * structural types (DiffUri/DiffDocument/DiffContentProvider) so BOTH the real vscode and the
 * headless mock satisfy it; the returns the flow ignores are typed `unknown`. NONE of these
 * carry a token (the diff render is token-free — see the diff section below).
 */
export interface DiffVscodeApi {
  readonly commands: {
    registerCommand(command: string, callback: (...args: unknown[]) => unknown): vscode.Disposable;
  };
  readonly window: {
    showInformationMessage(message: string): Thenable<string | undefined>;
    showQuickPick(
      items: readonly string[],
      options?: vscode.QuickPickOptions,
    ): Thenable<string | undefined>;
    // Open the rendered diff document. preview:true so diffs don't pile up tabs. The return
    // is ignored (the flow only awaits it) → `unknown`, keeping the real vscode (TextEditor)
    // and the headless mock structurally assignable.
    showTextDocument(
      document: DiffDocument,
      options?: vscode.TextDocumentShowOptions,
    ): Thenable<unknown>;
  };
  // The read-only virtual document: register the scheme's content provider + open a diff URI
  // (the provider fills it). Minimal structural types so both the real vscode + the mock fit.
  readonly workspace: {
    registerTextDocumentContentProvider(
      scheme: string,
      provider: DiffContentProvider,
    ): vscode.Disposable;
    openTextDocument(uri: DiffUri): Thenable<DiffDocument>;
  };
  // A `.diff`-suffixed URI already gets the `diff` language; this is the explicit belt-and-
  // braces call so highlighting is deterministic. The return is ignored.
  readonly languages: {
    setTextDocumentLanguage(document: DiffDocument, languageId: string): Thenable<unknown>;
  };
  // Build the per-diff `conductor-diff:` URI. Only `parse` is needed.
  readonly Uri: {
    parse(value: string): DiffUri;
  };
}

/** Minimal structural URI the diff flow needs: just `toString()` (the store keys on the
 * string form). The real `vscode.Uri` and the headless mock's UriLike both satisfy it. */
export interface DiffUri {
  toString(): string;
}

/** Minimal structural document the diff flow needs: just its `uri` (to assert WHICH diff was
 * opened; the body is supplied by the content provider). Real `vscode.TextDocument` (uri:
 * Uri) and the headless mock's `{ uri }` both satisfy it. */
export interface DiffDocument {
  readonly uri: DiffUri;
}

/** Minimal structural content provider for the `conductor-diff` scheme: the host calls
 * `provideTextDocumentContent(uri)` to fill the virtual document. Real
 * `vscode.TextDocumentContentProvider` (whose method takes an optional extra
 * CancellationToken) is assignable here, as is {@link makeDiffContentProvider}'s return. */
export interface DiffContentProvider {
  provideTextDocumentContent(uri: DiffUri): string | undefined | null;
}

/** The arguments the live-cockpit HTML builder needs, all already resolved by the caller
 * (resolveWebviewView): the runtime `cspSource`, a fresh per-render `nonce`, and the
 * `asWebviewUri`-mapped script + style URLs of the bundled cockpit. */
export interface WebviewHtmlOptions {
  readonly cspSource: string;
  readonly nonce: string;
  readonly scriptUri: string;
  readonly styleUri: string;
}

/**
 * Builds the Fleet view HTML that loads the SHARED cockpit bundle (4B-3). Pure (no vscode
 * runtime) so a test asserts the CSP + that it references the script/style URIs. The CSP
 * is strict: `default-src 'none'`; only our own nonce'd <script> runs and only nonce'd or
 * `cspSource` styles apply; images from `cspSource`/data:, fonts from `cspSource`. Crucially
 * `connect-src 'none'` forbids the webview from doing its OWN network — ALL data arrives
 * over the postMessage bridge (4B-2) from the authed host, so the token never reaches the
 * webview (ADR-0027/0029). There is NO inline script body and no 'unsafe-inline': the only
 * script is the nonce'd bundle <script src=…>. The bundle mounts FleetDashboard in fork
 * mode (token="", injected transports) — see webview/main.tsx.
 */
export function webviewHtml(opts: WebviewHtmlOptions): string {
  const { cspSource, nonce, scriptUri, styleUri } = opts;
  return `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta
      http-equiv="Content-Security-Policy"
      content="default-src 'none'; script-src 'nonce-${nonce}'; style-src 'nonce-${nonce}' ${cspSource}; img-src ${cspSource} data:; font-src ${cspSource}; connect-src 'none';"
    />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <link rel="stylesheet" nonce="${nonce}" href="${styleUri}" />
    <title>Conductor</title>
  </head>
  <body>
    <div id="root"></div>
    <script nonce="${nonce}" src="${scriptUri}"></script>
  </body>
</html>`;
}

/**
 * Builds the no-config Fleet HTML. Pure (no vscode runtime needed) so tests can assert the
 * CSP + copy directly. Reached ONLY on the legacy bare-construction path (a
 * `new FleetViewProvider()` with no config + no extensionUri): without the extensionUri we
 * can't build the bundle's resource URIs, so we render a minimal strict-CSP "not connected"
 * page (no live script). The production path always supplies a config and serves
 * `webviewHtml` (the live cockpit bundle). The CSP mirrors the live page minus the script
 * directive: everything defaults to 'none', only our nonce'd <style> applies, and
 * `connect-src 'none'` still forbids any webview-originated network.
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
    <p class="status">Not connected. Run "Conductor: Connect to Gateway" to connect.</p>
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

/** Configuration the FleetViewProvider needs to load the cockpit bundle + attach the
 * bridge on resolve. `extensionUri` is the installed extension root (context.extensionUri)
 * used to build the webview resource URIs of the bundled cockpit under dist/webview. */
export interface FleetViewConfig {
  readonly secrets: SecretStore;
  readonly gatewayUrl: string;
  readonly extensionUri: vscode.Uri;
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
 * The Fleet view provider. With a config (the production path) resolve (a) enables scripts
 * and scopes `localResourceRoots` to dist/webview so ONLY the bundle's assets load, (b)
 * serves `webviewHtml` — a strict-CSP page that nonce-loads the shared cockpit bundle
 * (4B-3) + its CSS via `asWebviewUri`, and (c) constructs + attaches a HostBridge over the
 * webview so the typed postMessage transport (the 4A-1 fork impl) is live for the bundle's
 * REST/WS. The bridge's dispose is registered on the view's onDidDispose so the WS handles
 * + listener are torn down.
 *
 * `config` is optional ONLY so a bare `new FleetViewProvider()` still constructs (used by
 * legacy unit tests of the static HTML); when absent, resolve renders the no-config
 * placeholder (scripts on, no bundle, no bridge — no extensionUri to build asset URIs).
 * The production path always supplies a config with the real factory + extensionUri.
 */
export class FleetViewProvider implements vscode.WebviewViewProvider {
  readonly #config: FleetViewConfig | undefined;
  // The bridge for the currently-resolved view. VS Code can call resolveWebviewView
  // more than once on one provider (a hidden view is torn down and re-resolved on
  // reveal when retainContextWhenHidden is off); dispose any predecessor on re-resolve
  // so an old HostBridge (with its WS handles + message listener) can never leak.
  #bridge: { dispose(): void } | undefined;

  constructor(config?: FleetViewConfig) {
    this.#config = config;
  }

  resolveWebviewView(webviewView: vscode.WebviewView): void {
    const config = this.#config;
    if (config === undefined) {
      // No config (bare construction in a legacy test): scripts on, placeholder HTML, no
      // bundle (no extensionUri for asset URIs), no bridge.
      webviewView.webview.options = { enableScripts: true };
      webviewView.webview.html = placeholderHtml(webviewView.webview.cspSource);
      return;
    }

    // Re-resolve safety: tear down the bridge from a previous resolve before building a
    // new one, so a re-resolve that didn't fire the prior view's onDidDispose can't leak.
    this.#bridge?.dispose();
    this.#bridge = undefined;

    const webview = webviewView.webview;
    const distRoot = vscode.Uri.joinPath(config.extensionUri, "dist", "webview");
    // Scope script/style loading to the bundle dir: the webview can't read arbitrary
    // extension files, only dist/webview (paired with the strict CSP).
    webview.options = { enableScripts: true, localResourceRoots: [distRoot] };

    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.js")).toString();
    const styleUri = webview.asWebviewUri(vscode.Uri.joinPath(distRoot, "main.css")).toString();
    webview.html = webviewHtml({
      cspSource: webview.cspSource,
      nonce: makeNonce(),
      scriptUri,
      styleUri,
    });

    const factory =
      config.bridgeFactory ?? makeBridgeFactory(config.secrets, config.gatewayUrl);
    const bridge = factory(webview);
    this.#bridge = bridge;
    bridge.attach();
    // Tear the bridge down when the view goes away (closes WS handles + the listener).
    webviewView.onDidDispose(() => {
      bridge.dispose();
      if (this.#bridge === bridge) {
        this.#bridge = undefined;
      }
    });
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
 * Renders the dedicated intervention status-bar label for a pending count (4C-3). Pure so a
 * test asserts the 0/1/N renderings. Returns "" for 0 (the caller hides the item then);
 * singular/plural for ≥1. Carries no token — only the count. Distinct codicon ($(bell))
 * from the connection item so the two reads don't collide.
 */
export function interventionStatusText(count: number): string {
  if (count <= 0) {
    return "";
  }
  const noun = count === 1 ? "intervention" : "interventions";
  return `$(bell) Conductor: ${count} ${noun}`;
}

/** The slice of the intervention status-bar item `handleIntervention` updates: set its
 * text + show/hide it. The mock's StatusBarItem (and the real vscode.StatusBarItem) satisfy
 * it. Declared narrowly so the helper stays assertable. */
export interface InterventionStatusBar {
  text: string;
  show(): void;
  hide(): void;
}

/**
 * The task-scoped actions the intervention toast can take, injected so `handleIntervention`
 * stays pure of the control/diff wiring (a test passes spies; activate passes the real
 * flows). Both are scoped to the intervention's own project/task. Token-free by contract.
 */
export interface InterventionActions {
  /** Approve THIS held task in place (confirm-gated → a real merge). */
  approve: (project: string, task: string) => void | Promise<void>;
  /** Open THIS task's most-recent native diff (take-over-into-the-editor). */
  openDiff: (project: string, task: string) => void | Promise<void>;
}

/**
 * The testable core of the intervention handler (4C-3, upgraded): given the new pending
 * COUNT and the distilled (token-free) intervention, (1) update + show the dedicated
 * status-bar item, and (2) show a warning toast naming `project/task: reason` with THREE
 * one-click actions — Approve (act on the gate-green task in place), Open diff (take over
 * the change in the native editor), Open Conductor (reveal the panel). The choice routes to
 * the injected action (or the reveal command) so the director never has to dig for it. Pure
 * of the count bookkeeping (the caller owns the counter) so a test drives it with a fixed
 * count + a mock api + spy actions.
 *
 * TOKEN NOTE: `intervention` carries only project/task/reason (the notifier guarantees no
 * token in it), and neither the toast nor the status text echoes anything else — so no
 * token can reach a vscode message here.
 */
export async function handleIntervention(
  api: VscodeApi,
  statusBar: InterventionStatusBar,
  count: number,
  intervention: Intervention,
  actions: InterventionActions,
): Promise<void> {
  statusBar.text = interventionStatusText(count);
  statusBar.show();

  const { project, task, reason } = intervention;
  const choice = await api.window.showWarningMessage(
    `Intervention needed — ${project}/${task}: ${reason}`,
    APPROVE_ACTION,
    OPEN_DIFF_ACTION,
    OPEN_CONDUCTOR_ACTION,
  );
  if (choice === OPEN_CONDUCTOR_ACTION) {
    await api.commands.executeCommand(REVEAL_CONTAINER_COMMAND);
  } else if (choice === APPROVE_ACTION) {
    await actions.approve(project, task);
  } else if (choice === OPEN_DIFF_ACTION) {
    await actions.openDiff(project, task);
  }
}

// ─────────────────────────────────────────────────────────────────────────────────────
// 4C-1b — native diff render (ADR-0030): the conductor emits a bounded KindDiff at the green
// gate; the host renders it in a NATIVE VS Code diff view. The render is a READ-ONLY VIRTUAL
// DOCUMENT: each diff gets a unique, stable `conductor-diff:` URI whose path ends in `.diff`
// (so VS Code auto-applies the `diff` language for highlighting), and the immutable body is
// served by a TextDocumentContentProvider from a small bounded store. Content is immutable
// per URI → no EventEmitter/onDidChange is needed. Mirrors the 4C-3 quiet bell pattern: the
// signal is a SEPARATE status-bar item, not a per-diff toast (interventions already toast —
// avoid double-noise; auto-surfacing held-task diffs is a possible later enhancement).
// ─────────────────────────────────────────────────────────────────────────────────────

/** Max diffs the bounded store retains (oldest evicted past this). Keeps the green-gate
 * stream from growing memory without bound; ~20 recent diffs is plenty for review. */
export const DIFF_STORE_CAP = 20;

/**
 * Renders the dedicated diff status-bar label for the count of retained diffs (4C-1b). Pure
 * so a test asserts the 0/1/N renderings. Returns "" for 0 (the caller hides the item then);
 * singular/plural for ≥1. Carries no token — only the count. Distinct codicon ($(git-compare))
 * from the connection ($(plug)) and intervention ($(bell)) items so the three reads don't
 * collide.
 */
export function diffStatusText(count: number): string {
  if (count <= 0) {
    return "";
  }
  const noun = count === 1 ? "diff" : "diffs";
  return `$(git-compare) Conductor: ${count} ${noun}`;
}

/**
 * Builds the read-only diff document body for a {@link TaskDiff}. PURE (no vscode runtime) so
 * a test asserts it directly. Layout: a short human header (project/task, `<base>...<branch>`,
 * the file count, and the word `truncated` when the producer capped the diff), then one stat
 * line per file (`M  path  +3 -1`), then a blank line, then the unified `patch` verbatim.
 *
 * TOKEN NOTE: `taskDiff` is the notifier-distilled, token-free shape; this only reorders its
 * own fields into text, so no token can reach the rendered document.
 */
export function renderDiffDocument(taskDiff: TaskDiff): string {
  const { project, task, branch, base, files, patch, truncated } = taskDiff;
  const range = `${base || "?"}...${branch || "?"}`;
  const fileWord = files.length === 1 ? "file" : "files";
  const header = [
    `# Conductor diff — ${project}/${task}`,
    `# ${range} · ${files.length} ${fileWord}${truncated ? " · truncated" : ""}`,
  ];
  const stats = files.map((f) => `# ${(f.status || "?").padEnd(2)} ${f.path}  +${f.additions} -${f.deletions}`);
  // Header + per-file stats, a blank separator, then the patch (which may be "" when the
  // producer dropped it / there's no textual change — the stats still convey the change).
  return [...header, ...stats, "", patch].join("\n");
}

/**
 * One retained diff in the {@link DiffStore}: enough to (a) serve the content provider and
 * (b) label a quick-pick item. `uri` is the stable string key; `task` + `fileCount` build the
 * quick-pick label. Token-free (derived from a TaskDiff).
 */
export interface StoredDiff {
  uri: string;
  project: string;
  task: string;
  fileCount: number;
  content: string;
}

/**
 * A small BOUNDED store of recently-emitted diffs, keyed by their `conductor-diff:` URI
 * string. Mints a unique URI per diff from a monotonic counter (so re-diffing the same task
 * never collides with a stale document), caps the retained set at {@link DIFF_STORE_CAP}
 * (evicting the OLDEST), serves the content provider, and lists the recents (newest first)
 * for the quick-pick. No vscode runtime → unit-tested directly. Token-free by construction
 * (every entry is built from a token-free TaskDiff via {@link renderDiffDocument}).
 */
export class DiffStore {
  // Insertion-ordered: Map preserves insertion order, so the first key is the oldest.
  readonly #byUri = new Map<string, StoredDiff>();
  #counter = 0;

  /** Renders + stores a diff, returning its fresh unique URI string. The path ends in
   * `.diff` so VS Code applies the `diff` language; segments are encoded so a task id with
   * odd characters can't break the URI. Evicts the oldest entry past the cap. */
  add(taskDiff: TaskDiff): string {
    const n = (this.#counter += 1);
    const uri = `${DIFF_SCHEME}:/${encodeURIComponent(taskDiff.project)}/${encodeURIComponent(
      taskDiff.task,
    )}/${n}.diff`;
    this.#byUri.set(uri, {
      uri,
      project: taskDiff.project,
      task: taskDiff.task,
      fileCount: taskDiff.files.length,
      content: renderDiffDocument(taskDiff),
    });
    // Evict oldest while over the cap (normally a single eviction per add).
    while (this.#byUri.size > DIFF_STORE_CAP) {
      const oldest = this.#byUri.keys().next().value;
      if (oldest === undefined) {
        break;
      }
      this.#byUri.delete(oldest);
    }
    return uri;
  }

  /** The rendered content for a URI, or undefined if it's unknown/evicted (the content
   * provider maps that to a placeholder). */
  content(uri: string): string | undefined {
    return this.#byUri.get(uri)?.content;
  }

  /** The retained diffs, NEWEST first (the most recent is `recents()[0]`). */
  recents(): StoredDiff[] {
    return [...this.#byUri.values()].reverse();
  }

  /** The most-recent retained diff for a specific project/task, or undefined if none is
   * retained (never emitted, or evicted past the cap). Drives the task-scoped "Open diff"
   * take-over from the intervention toast. */
  latestForTask(project: string, task: string): StoredDiff | undefined {
    return this.recents().find((d) => d.project === project && d.task === task);
  }

  /** The number of retained diffs (drives the status-bar count). */
  get size(): number {
    return this.#byUri.size;
  }
}

/** The placeholder body a diff document shows once its entry has been evicted from the
 * bounded store (the URI outlived the cap). Plain text; carries no token. */
export const DIFF_EVICTED_PLACEHOLDER = "(diff no longer available)";

/**
 * Builds the read-only `TextDocumentContentProvider` for the `conductor-diff` scheme, backed
 * by the given store. VS Code calls `provideTextDocumentContent(uri)` to fill the virtual
 * document; we return the stored body, or a clear placeholder for an unknown/evicted URI.
 * Content is immutable per URI, so no onDidChange emitter is needed. Pure of vscode runtime
 * apart from the structural return type → unit-tested directly.
 */
export function makeDiffContentProvider(store: DiffStore): DiffContentProvider {
  return {
    provideTextDocumentContent(uri: DiffUri): string {
      return store.content(uri.toString()) ?? DIFF_EVICTED_PLACEHOLDER;
    },
  };
}

/**
 * Opens a stored diff (by its URI string) in a native read-only diff document: parse the
 * URI → `openTextDocument` (the content provider fills it) → `showTextDocument` (preview so
 * diffs don't stack tabs) → explicitly set the `diff` language (the `.diff` suffix already
 * does, this is belt-and-braces). Pure of the picking logic so both the status-bar click and
 * the quick-pick reuse it.
 */
async function openDiff(api: DiffVscodeApi, uri: string): Promise<void> {
  const doc = await api.workspace.openTextDocument(api.Uri.parse(uri));
  await api.window.showTextDocument(doc, { preview: true });
  await api.languages.setTextDocumentLanguage(doc, "diff");
}

/**
 * The show-diff flow (4C-1b), the `conductor.showDiff` command + the diff status-bar click.
 * With several retained diffs it offers a quick-pick labelled `<task> (<n> files)` and opens
 * the chosen; with exactly one it opens it directly; with none it shows an info message. The
 * quick-pick label is index-aligned to the recents list so the pick maps back to a URI
 * (task ids aren't unique across diffs). Token-free throughout (the store is token-free).
 */
export async function runShowDiff(api: DiffVscodeApi, store: DiffStore): Promise<void> {
  const recents = store.recents();
  if (recents.length === 0) {
    await api.window.showInformationMessage("No task diffs yet.");
    return;
  }
  if (recents.length === 1) {
    await openDiff(api, recents[0]!.uri);
    return;
  }
  const labels = recents.map((d) => diffQuickPickLabel(d));
  const picked = await api.window.showQuickPick(labels, { placeHolder: "Select a task diff" });
  if (picked === undefined) {
    return; // cancelled — quiet no-op.
  }
  const idx = labels.indexOf(picked);
  if (idx < 0) {
    return; // defensive: the picked label isn't one we offered.
  }
  await openDiff(api, recents[idx]!.uri);
}

/** Labels a stored diff for the quick-pick: `<task> (<n> file[s])`. Pure; token-free. */
function diffQuickPickLabel(d: StoredDiff): string {
  const noun = d.fileCount === 1 ? "file" : "files";
  return `${d.task} (${d.fileCount} ${noun})`;
}

/**
 * Opens a SPECIFIC task's most-recent native diff (4C-3 "take over"): the intervention toast's
 * "Open diff" routes here so the director lands on the change for the task that needs them —
 * not a quick-pick of everything. With no retained diff for that task (never emitted / evicted)
 * it shows a clear info message rather than opening a stale one. Token-free (the store is).
 */
export async function runShowDiffForTask(
  api: DiffVscodeApi,
  store: DiffStore,
  project: string,
  task: string,
): Promise<void> {
  const d = store.latestForTask(project, task);
  if (d === undefined) {
    await api.window.showInformationMessage(`No diff yet for ${project}/${task}.`);
    return;
  }
  await openDiff(api, d.uri);
}

/**
 * Approves a SPECIFIC held task in place (4C-3 toast "Approve"): confirm-gate it (approve
 * triggers a real merge), fire the authed task-level control POST, and surface a token-FREE
 * result. Mirrors runControl's approve branch but is task-scoped (no project pick) so the
 * director acts on the exact gate-green task the intervention named. A declined confirm is a
 * quiet no-op. The token lives only in the ControlClient header — never in a message here.
 */
export async function runApproveTask(
  api: VscodeApi,
  control: Pick<Control, "approve">,
  project: string,
  task: string,
): Promise<void> {
  const choice = await api.window.showWarningMessage(
    `Approve ${project}/${task}? This triggers a real merge.`,
    { modal: true },
    "Yes",
  );
  if (choice !== "Yes") {
    return; // declined / dismissed — quiet no-op.
  }
  const result = await control.approve(project, task);
  if (result.ok) {
    await api.window.showInformationMessage(`Approve requested for ${project}/${task}.`);
    return;
  }
  switch (result.reason) {
    case "conflict":
      await api.window.showWarningMessage(
        "No single task is awaiting approval (none or multiple).",
        {},
        "OK",
      );
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/**
 * The testable core of the diff handler (4C-1b): given the new diff, store + render it, then
 * reflect the retained-diff COUNT on the dedicated status-bar item (show it; the count is
 * always ≥1 here). Deliberately QUIET — NO toast (the status bar is the signal; interventions
 * already toast, so a per-diff toast would be double-noise; auto-surfacing held-task diffs is
 * a possible later enhancement). The status-bar item's `command` (set at activation) opens
 * the most-recent diff. Token-free: `taskDiff` carries no token and neither the store nor the
 * status text echoes anything else.
 */
export function handleDiff(
  store: DiffStore,
  statusBar: InterventionStatusBar,
  taskDiff: TaskDiff,
): void {
  store.add(taskDiff);
  statusBar.text = diffStatusText(store.size);
  statusBar.show();
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

/** The narrow control surface `runControl` drives — the slice of ControlClient it uses.
 * Declared as an interface so the unit tests pass a fake (no real fetch/token) while the
 * production path injects a real ControlClient. None of these carry the token out. */
export interface Control {
  listProjects(): Promise<{ id: string }[]>;
  pause(projectId: string): Promise<ControlResult>;
  resume(projectId: string): Promise<ControlResult>;
  abort(projectId: string): Promise<ControlResult>;
  approve(projectId: string, taskId?: string): Promise<ControlResult>;
}

/** Re-exported from controlClient so callers/tests reference one shape. */
export type ControlResult = Awaited<ReturnType<ControlClient["pause"]>>;

/** Human label for a control action, used in confirm prompts + result messages (e.g.
 * "Abort"). Pure; carries no token. */
function actionLabel(action: ControlAction): string {
  switch (action) {
    case "pause":
      return "Pause";
    case "resume":
      return "Resume";
    case "abort":
      return "Abort";
    case "approve":
      return "Approve";
  }
}

/** Whether an action needs an explicit modal confirm before firing. abort (cancels a
 * running task) and approve (a decisive human gate) are confirm-gated; pause/resume are
 * cheap + idempotent, so they fire directly. */
function needsConfirm(action: ControlAction): boolean {
  return action === "abort" || action === "approve";
}

/**
 * The inline-control flow (4C-2) for `conductor.pause`/`resume`/`abort`/`approve`. Lists
 * projects (authed, host-side), lets the user pick one, confirms destructive/decisive
 * actions (abort/approve) via a modal, fires the authed control POST, and surfaces a
 * token-FREE, action-specific result message. The token lives only in the ControlClient's
 * Authorization header — it never reaches a message here. Cancelling the pick (or the
 * confirm) is a quiet no-op. Reuses the existing control API; independent of the 4C-0
 * diff source (ADR-0030).
 */
export async function runControl(
  api: VscodeApi,
  control: Control,
  action: ControlAction,
): Promise<void> {
  const label = actionLabel(action);

  let projects: { id: string }[];
  try {
    projects = await control.listProjects();
  } catch (err) {
    // listProjects failed before any action — branch on the closed reason (no token in
    // the message). "not-connected"/"unauthorized" guide the user to Connect; anything
    // else is an unreachable gateway.
    if (err instanceof ControlListError && err.reason === "not-connected") {
      await api.window.showErrorMessage("Connect to the gateway first.");
    } else if (err instanceof ControlListError && err.reason === "unauthorized") {
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
    } else {
      await api.window.showErrorMessage("Could not reach the gateway.");
    }
    return;
  }

  if (projects.length === 0) {
    await api.window.showInformationMessage("No projects.");
    return;
  }

  const pick = await api.window.showQuickPick(
    projects.map((p) => p.id),
    { placeHolder: "Select a project" },
  );
  if (pick === undefined) {
    return; // cancelled — quiet no-op.
  }

  if (needsConfirm(action)) {
    const choice = await api.window.showWarningMessage(
      `${label} ${pick}?`,
      { modal: true },
      "Yes",
    );
    if (choice !== "Yes") {
      return; // declined / dismissed — quiet no-op.
    }
  }

  const result = await control[action](pick);
  if (result.ok) {
    await api.window.showInformationMessage(`${label} requested for ${pick}.`);
    return;
  }
  switch (result.reason) {
    case "conflict":
      await api.window.showWarningMessage(conflictMessage(action), {}, "OK");
      return;
    case "unauthorized":
      await api.window.showErrorMessage("The gateway rejected the stored token. Reconnect.");
      return;
    case "not-connected":
      await api.window.showErrorMessage("Connect to the gateway first.");
      return;
    case "unreachable":
      await api.window.showErrorMessage("Could not reach the gateway.");
      return;
  }
}

/** Action-specific guidance for a 409 conflict from the gateway. abort: nothing is
 * running; approve: no SINGLE task is awaiting (zero or multiple). pause/resume are
 * idempotent and never 409, but the union is total so they get a generic fallback.
 * Carries no token. */
function conflictMessage(action: ControlAction): string {
  switch (action) {
    case "abort":
      return "No task is running to abort.";
    case "approve":
      return "No single task is awaiting approval (none or multiple).";
    case "pause":
    case "resume":
      return "The gateway could not apply that action.";
  }
}

/**
 * Wires the extension's contributions onto the given (real or mocked) vscode API and
 * returns the created disposables. Kept separate from `activate` so unit tests can call
 * it with a mock + a manager and assert the registration + command behavior without a
 * running host. `gatewayUrl` is captured for the connect flow's URL.
 */
export function registerConductor(
  api: VscodeApi & DiffVscodeApi,
  manager: ConnectionManager,
  gatewayUrl: string,
  control: Control,
  diffStore: DiffStore,
  fleetConfig?: FleetViewConfig,
): vscode.Disposable[] {
  const connect = api.commands.registerCommand(CONNECT_COMMAND, () => {
    void runConnect(api, manager, gatewayUrl);
  });
  const disconnect = api.commands.registerCommand(DISCONNECT_COMMAND, () => {
    void runDisconnect(api, manager);
  });
  // 4C-2 inline control commands: each prompts for a project + (for abort/approve)
  // confirms, then fires the authed control POST host-side. The token stays in the
  // ControlClient's Authorization header — never in a message.
  const controlDisposables = (["pause", "resume", "abort", "approve"] as const).map((action) =>
    api.commands.registerCommand(controlCommandId(action), () => {
      void runControl(api, control, action);
    }),
  );
  // 4C-1b: the read-only diff scheme + the show-diff command. The content provider serves the
  // bounded store; the command opens the most-recent diff (or quick-picks when several are
  // retained). The DiffObserver (wired in activate) feeds the store on each green-gate diff.
  const diffProvider = api.workspace.registerTextDocumentContentProvider(
    DIFF_SCHEME,
    makeDiffContentProvider(diffStore),
  );
  const showDiff = api.commands.registerCommand(SHOW_DIFF_COMMAND, () => {
    void runShowDiff(api, diffStore);
  });
  // The Fleet view attaches the host↔webview bridge on resolve. When a config is
  // provided (the production path) the provider builds a real HostBridge; tests may omit
  // it (static-HTML provider) or pass one with a fake bridge factory.
  const provider = fleetConfig ? new FleetViewProvider(fleetConfig) : new FleetViewProvider();
  const fleetView = api.window.registerWebviewViewProvider(FLEET_VIEW_ID, provider);
  return [connect, disconnect, ...controlDisposables, diffProvider, showDiff, fleetView];
}

/** Maps a control action to its contributed command id (kept in lockstep with the
 * package.json `contributes.commands` entries). */
function controlCommandId(action: ControlAction): string {
  switch (action) {
    case "pause":
      return PAUSE_COMMAND;
    case "resume":
      return RESUME_COMMAND;
    case "abort":
      return ABORT_COMMAND;
    case "approve":
      return APPROVE_COMMAND;
  }
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

  // 4C-3: a SEPARATE status-bar item for pending interventions ($(bell) Conductor: N
  // intervention(s)), shown only when the count > 0. The connection item above keeps
  // showing the link state; this one is the at-a-glance intervention read. Starts hidden.
  const interventionBar = vscode.window.createStatusBarItem();
  interventionBar.command = REVEAL_CONTAINER_COMMAND;
  let pendingInterventions = 0;

  // 4C-1b: a SEPARATE status-bar item for recently-emitted task diffs ($(git-compare)
  // Conductor: N diff(s)), shown only when N>0 (mirrors the 4C-3 bell). Its command opens
  // the most-recent diff (runShowDiff with one entry opens it directly). The bounded store
  // holds the rendered diffs; the content provider (registered in registerConductor) serves
  // them. Starts hidden.
  const diffBar = vscode.window.createStatusBarItem();
  diffBar.command = SHOW_DIFF_COMMAND;
  const diffStore = new DiffStore();

  // 4C-3: the host's OWN intervention-filtered WS subscription. The 4B-2 HostBridge only
  // forwards events to the webview while it's OPEN; this fires native notifications even
  // when the Conductor view is closed. TokenProvider reads SecretStorage per start (never
  // cached); wsBaseUrl is the ws-derived normalized base; the token rides only in the WS
  // URL — never in onIntervention (which carries project/task/reason) or a message.
  const notifier = new InterventionNotifier({
    wsBaseUrl: deriveWsUrl(normalizeBaseUrl(gatewayUrl)),
    tokenProvider: { getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)) },
    wsConnector,
    onIntervention: (intervention) => {
      pendingInterventions += 1;
      // 4C-3 upgrade: the toast's actions act on the intervention's own task — Approve it
      // in place (confirm-gated) or take it over by opening its native diff. `control` +
      // `diffStore` are in scope (defined in this activation); the closure runs only when an
      // intervention fires, by which point both are initialized.
      void handleIntervention(vscode, interventionBar, pendingInterventions, intervention, {
        approve: (project, task) => runApproveTask(vscode, control, project, task),
        openDiff: (project, task) => runShowDiffForTask(vscode, diffStore, project, task),
      });
    },
  });

  // 4C-1b: the host's OWN diff-filtered WS subscription — same seams + same lifecycle as the
  // notifier. On each green-gate KindDiff it stores the rendered diff + bumps the
  // $(git-compare) count (QUIET — no toast; the status bar is the signal). Token rides only
  // in the WS URL; onDiff carries the token-free distilled TaskDiff.
  const diffObserver = new DiffObserver({
    wsBaseUrl: deriveWsUrl(normalizeBaseUrl(gatewayUrl)),
    tokenProvider: { getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)) },
    wsConnector,
    onDiff: (taskDiff) => handleDiff(diffStore, diffBar, taskDiff),
  });

  const manager = new ConnectionManager({
    secrets: context.secrets,
    gateway: makeGatewayProbe(gatewayUrl),
    onStateChange: (state) => {
      statusBar.text = statusBarText(state);
      // 4C-3 + 4C-1b: tie the host WS subscriptions to the link state — open them on
      // "connected", tear them down otherwise. Extends the SAME onStateChange the status bar
      // uses (no second manager). start() is fire-and-forget (reads the token, never
      // throws/logs it); stop() is idempotent.
      if (state === "connected") {
        void notifier.start();
        void diffObserver.start();
      } else {
        notifier.stop();
        diffObserver.stop();
      }
    },
  });
  statusBar.text = statusBarText(manager.state);
  statusBar.command = CONNECT_COMMAND;
  statusBar.show();

  // 4C-2: the inline control commands' host-side authed client. Its TokenProvider reads
  // the bearer token from SecretStorage per request (never caches it); the normalized
  // gateway base URL matches the connect flow. The token rides only in the client's
  // Authorization header — it never reaches a message or a webview.
  const control = new ControlClient(normalizeBaseUrl(gatewayUrl), {
    getToken: () => Promise.resolve(context.secrets.get(GATEWAY_TOKEN_KEY)),
  });

  // The Fleet view's bridge reads the token from SecretStorage (per request) and talks
  // to the gateway at the configured URL; the token never reaches the webview. The
  // extensionUri lets the provider build the cockpit bundle's webview resource URIs.
  const disposables = registerConductor(vscode, manager, gatewayUrl, control, diffStore, {
    secrets: context.secrets,
    gatewayUrl,
    extensionUri: context.extensionUri,
  });
  // Push the intervention + diff status-bar items + dispose-wrappers that stop the host WS
  // subscriptions on deactivate (so the host sockets are torn down with the extension).
  context.subscriptions.push(
    statusBar,
    interventionBar,
    diffBar,
    { dispose: () => notifier.stop() },
    { dispose: () => diffObserver.stop() },
    ...disposables,
  );

  // Silent restore: re-validate a stored token (if any) and mirror the result onto the
  // status bar via onStateChange. Fire-and-forget; never throws, never logs the token.
  void manager.restore();
}

/** Extension deactivation hook. Disposables are cleaned up via context.subscriptions. */
export function deactivate(): void {
  // Nothing to tear down explicitly; SecretStorage persists the token across sessions.
}

/**
 * Generates a 32-char alphanumeric nonce for the webview CSP from a CRYPTO RNG
 * (node:crypto randomBytes; the extension host is Node). A CSP nonce is a per-render
 * uniqueness token, not a secret, but using a crypto source (vs Math.random) is strictly
 * better hygiene and removes any predictability concern (review FAZ-4 finding). Kept
 * alphanumeric (modulo over the 62-char alphabet — bias is irrelevant for a CSP nonce).
 */
function makeNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const bytes = randomBytes(32);
  let nonce = "";
  for (let i = 0; i < 32; i++) {
    nonce += chars.charAt(bytes[i] % chars.length);
  }
  return nonce;
}
