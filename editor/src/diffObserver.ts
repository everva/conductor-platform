// Conductor Platform — host-side diff observer (Faz-4 DALGA 4C, 4C-1b).
//
// SCOPE (4C-1b): the EDITOR-NATIVE value-add for a task's green-gate diff. At
// PhaseReview, when a task's INDEPENDENT verify gate passes, the conductor emits a
// bounded `KindDiff` event (4C-1a, ADR-0030) carrying a DiffSummary. The 4B-2 HostBridge
// forwards gateway events to the WEBVIEW, and only while the webview is open/subscribed —
// so the in-panel cockpit can't surface a diff when the Conductor view is closed. This
// class opens the host's OWN lightweight WS subscription, FILTERED to `diff`, so the
// extension can render the change in a NATIVE VS Code diff view EVEN when the developer is
// looking at code — exactly mirroring the 4C-3 InterventionNotifier.
//
// It deliberately REUSES the 4B-2 transport seams (WsConnector / WsHandle / TokenProvider
// from ./bridge/hostBridge) rather than duplicating the `ws` adapter — the real
// wsConnector is wired in extension.ts; tests pass a fake. It depends on no `vscode` and
// no real network, so vitest drives it headlessly.
//
// TOKEN DISCIPLINE (HARD): the token is read from the TokenProvider (SecretStorage,
// host-side) and placed ONLY into the gateway WS URL's `?token=` query — exactly mirroring
// hostBridge.ts / notifier.ts. It NEVER appears in any value this observer emits (`onDiff`
// carries only project/task + the bounded diff fields) and is NEVER logged. The
// diff-leak-guard test in diffObserver.test.ts proves the sentinel token appears only in
// the WS URL.

import type { TokenProvider, WsConnector, WsHandle } from "./bridge/hostBridge";

/** The kind value the gateway tags a code-change event with (mirrors web events.gen.ts's
 * "diff" kind / Go events.KindDiff). Used both as the WS `kind=` filter and the inbound-
 * frame guard. Kept local so this module doesn't reach into web/. */
export const DIFF_KIND = "diff";

/**
 * One changed file in a {@link TaskDiff}, distilled from a DiffSummary file entry: the repo-
 * relative path, the git name-status letter (A/M/D/R/…), and the added/deleted line counts.
 * Token-free by construction (carries only diff facts).
 */
export interface DiffFile {
  path: string;
  status: string;
  additions: number;
  deletions: number;
}

/**
 * A task's bounded diff, distilled from a `diff` gateway event for the native diff view.
 * Deliberately token-FREE: the host passes this to `onDiff`, which feeds a user-facing
 * document — so it carries only the locating fields (`project`/`task`) and the bounded
 * diff payload (branch/base/files/patch/truncated). Mirrors the Go events.DiffSummary wire
 * shape (ADR-0030), plus the envelope's project/task (which the payload deliberately omits
 * to avoid drift).
 */
export interface TaskDiff {
  project: string;
  task: string;
  branch: string;
  base: string;
  files: DiffFile[];
  patch: string;
  truncated: boolean;
}

/**
 * Dependencies for a DiffObserver. `wsBaseUrl` is HOST-controlled (already normalized +
 * ws-derived, i.e. deriveWsUrl(normalizeBaseUrl(gatewayUrl))) so the subscription only ever
 * extends the host's own base. `tokenProvider`/`wsConnector` reuse the 4B-2 seams. `onDiff`
 * is invoked once per `diff` frame with the token-free distilled shape.
 */
export interface DiffObserverDeps {
  wsBaseUrl: string;
  tokenProvider: TokenProvider;
  wsConnector: WsConnector;
  onDiff: (d: TaskDiff) => void;
}

/**
 * Host-side diff observer. Construct one per activation with the real wsConnector + a
 * TokenProvider reading SecretStorage; `start()`/`stop()` are tied to the
 * ConnectionManager's state in extension.ts (start on "connected", stop otherwise). A
 * single active WS handle at a time — restarting closes the prior one first (no leak, the
 * FAZ-4 review lesson).
 */
export class DiffObserver {
  readonly #wsBaseUrl: string;
  readonly #tokenProvider: TokenProvider;
  readonly #wsConnector: WsConnector;
  readonly #onDiff: (d: TaskDiff) => void;

  /** The single active WS handle (undefined when stopped / never started). */
  #handle: WsHandle | undefined;

  constructor(deps: DiffObserverDeps) {
    this.#wsBaseUrl = deps.wsBaseUrl;
    this.#tokenProvider = deps.tokenProvider;
    this.#wsConnector = deps.wsConnector;
    this.#onDiff = deps.onDiff;
  }

  /**
   * Opens the host's diff-only WS subscription. Reads the token from SecretStorage (per
   * start, never cached on the instance): with NO token this is a quiet no-op (not started).
   * Otherwise it builds the gateway WS URL filtered to `diff` with the token in the
   * `?token=` query (token ONLY in the URL — see TOKEN DISCIPLINE) and opens it via the
   * injected connector. Restart-safe: any prior handle is closed first so a stale socket can
   * never leak (single active handle).
   */
  async start(): Promise<void> {
    const token = await this.#tokenProvider.getToken();
    if (token === undefined || token === "") {
      return; // not connected → nothing to subscribe to.
    }

    // Close a predecessor BEFORE opening a new one: a second start() (e.g. a reconnect)
    // must not leak the prior socket. Single active handle by construction.
    this.#handle?.close();
    this.#handle = undefined;

    // The token rides ONLY here, in the WS URL query. The kind filter keeps the host
    // stream light (diffs only); the gateway already excludes other kinds.
    const url = `${this.#wsBaseUrl}/ws?kind=${DIFF_KIND}&token=${encodeURIComponent(token)}`;
    this.#handle = this.#wsConnector.open(url, {
      onOpen: () => {
        /* nothing to do on open — frames drive the diff renders. */
      },
      onMessage: (data) => this.#onFrame(data),
      onClose: () => {
        /* server/error close: leave #handle as-is. stop() (or a restart) clears it. The
           observer doesn't auto-reconnect — extension.ts restarts it on the next
           "connected" transition. */
      },
    });
  }

  /** Closes the active WS handle (idempotent; safe if never started). */
  stop(): void {
    this.#handle?.close();
    this.#handle = undefined;
  }

  /**
   * Handles one inbound WS frame. Defensive, mirroring the web useEventStream parse:
   * JSON.parse in a try/catch (a malformed/non-JSON frame is ignored — no callback, no
   * throw); only a `diff` event drives `onDiff` (non-diff frames are ignored even though the
   * `kind=` filter should already exclude them). The payload is read defensively (it may be
   * partial — see {@link asDiffEvent}).
   */
  #onFrame(data: string): void {
    let parsed: unknown;
    try {
      parsed = JSON.parse(data);
    } catch {
      return; // malformed frame — ignore (no throw, no callback).
    }
    const diff = asDiffEvent(parsed);
    if (diff === undefined) {
      return; // not a diff event — ignore defensively.
    }
    this.#onDiff(diff);
  }
}

/**
 * Narrows an already-JSON-parsed frame to a `diff` event, returning the distilled
 * (token-free) {@link TaskDiff} or `undefined` if it isn't one. Tolerant of the full gateway
 * event shape (`{ project, task, kind, payload }`, plus extra fields like `ts`/`phase`):
 * requires `kind === "diff"` and string project/task. The payload (events.DiffSummary) may
 * be partial on the wire — branch/base/patch default to "", truncated to false, files to
 * [] — so a defensive consumer still renders something. Never throws.
 */
function asDiffEvent(x: unknown): TaskDiff | undefined {
  if (x === null || typeof x !== "object") {
    return undefined;
  }
  const o = x as Record<string, unknown>;
  if (o.kind !== DIFF_KIND) {
    return undefined;
  }
  if (typeof o.project !== "string" || typeof o.task !== "string") {
    return undefined;
  }
  const payload =
    o.payload !== null && typeof o.payload === "object"
      ? (o.payload as Record<string, unknown>)
      : {};
  return {
    project: o.project,
    task: o.task,
    branch: stringOr(payload.branch, ""),
    base: stringOr(payload.base, ""),
    files: filesOf(payload.files),
    patch: stringOr(payload.patch, ""),
    truncated: payload.truncated === true,
  };
}

/** Returns `v` when it is a string, else the fallback. Pure; never throws. */
function stringOr(v: unknown, fallback: string): string {
  return typeof v === "string" ? v : fallback;
}

/** Returns `v` when it is a finite number, else 0 (a JSON round-trip through PG yields a
 * number; a missing/binary count is treated as 0, matching the producer). Pure. */
function numberOr0(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

/**
 * Distills a DiffSummary `files` value into the token-free {@link DiffFile} list. Tolerant of
 * a missing/non-array value (→ []) and drops any entry that isn't an object with a string
 * `path` (the one load-bearing field a diff row needs); status defaults to "", the counts to
 * 0. Pure; never throws.
 */
function filesOf(v: unknown): DiffFile[] {
  if (!Array.isArray(v)) {
    return [];
  }
  const out: DiffFile[] = [];
  for (const entry of v) {
    if (entry === null || typeof entry !== "object") {
      continue; // non-object row — drop.
    }
    const f = entry as Record<string, unknown>;
    if (typeof f.path !== "string") {
      continue; // a row with no path can't be rendered — drop.
    }
    out.push({
      path: f.path,
      status: stringOr(f.status, ""),
      additions: numberOr0(f.additions),
      deletions: numberOr0(f.deletions),
    });
  }
  return out;
}
