// Conductor Platform — host-side intervention observer (Faz-4 DALGA 4C, 4C-3).
//
// SCOPE (4C-3): the EDITOR-NATIVE value-add for human interventions. The 4B-2 HostBridge
// forwards gateway events to the WEBVIEW, and only while the webview is open/subscribed —
// so the in-panel InterventionBanner (3B-3) is silent when the Conductor view is closed.
// This class opens the host's OWN lightweight WS subscription, FILTERED to
// `intervention-needed`, so the extension can fire a native VS Code notification + reflect
// pending interventions in the status bar EVEN when the developer is looking at code.
//
// It deliberately REUSES the 4B-2 transport seams (WsConnector / WsHandle / TokenProvider
// from ./bridge/hostBridge) rather than duplicating the `ws` adapter — the real
// wsConnector is wired in extension.ts; tests pass a fake. It depends on no `vscode` and
// no real network, so vitest drives it headlessly.
//
// TOKEN DISCIPLINE (HARD): the token is read from the TokenProvider (SecretStorage,
// host-side) and placed ONLY into the gateway WS URL's `?token=` query — exactly mirroring
// hostBridge.ts. It NEVER appears in any value this observer emits (`onIntervention`
// carries only project/task/reason) and is NEVER logged. The notifier-leak-guard test in
// notifier.test.ts proves the sentinel token appears only in the WS URL.

import type { TokenProvider, WsConnector, WsHandle } from "./bridge/hostBridge";

/** The kind value the gateway tags a human-gate event with (mirrors web events.gen.ts's
 * KIND_INTERVENTION_NEEDED). Used both as the WS `kind=` filter and the inbound-frame
 * guard. Kept local so this module doesn't reach into web/. */
const INTERVENTION_KIND = "intervention-needed";

/** Generic reason surfaced when the gateway event carries no `payload.reason`. The event
 * always names the project/task, but the reason is best-effort. */
const DEFAULT_REASON = "needs attention";

/**
 * A pending human intervention, distilled from an `intervention-needed` gateway event for
 * the native notification + status bar. Deliberately token-FREE: the host passes this to
 * `onIntervention`, which feeds a user-facing message — so it carries only the locating
 * fields (`project`/`task`) and a human `reason`.
 */
export interface Intervention {
  project: string;
  task: string;
  reason: string;
}

/**
 * Dependencies for an InterventionNotifier. `wsBaseUrl` is HOST-controlled (already
 * normalized + ws-derived, i.e. deriveWsUrl(normalizeBaseUrl(gatewayUrl))) so the
 * subscription only ever extends the host's own base. `tokenProvider`/`wsConnector` reuse
 * the 4B-2 seams. `onIntervention` is invoked once per intervention-needed frame with the
 * token-free distilled shape.
 */
export interface InterventionNotifierDeps {
  wsBaseUrl: string;
  tokenProvider: TokenProvider;
  wsConnector: WsConnector;
  onIntervention: (i: Intervention) => void;
}

/**
 * Host-side intervention observer. Construct one per activation with the real wsConnector +
 * a TokenProvider reading SecretStorage; `start()`/`stop()` are tied to the
 * ConnectionManager's state in extension.ts (start on "connected", stop otherwise). A
 * single active WS handle at a time — restarting closes the prior one first (no leak, the
 * FAZ-4 review lesson).
 */
export class InterventionNotifier {
  readonly #wsBaseUrl: string;
  readonly #tokenProvider: TokenProvider;
  readonly #wsConnector: WsConnector;
  readonly #onIntervention: (i: Intervention) => void;

  /** The single active WS handle (undefined when stopped / never started). */
  #handle: WsHandle | undefined;

  constructor(deps: InterventionNotifierDeps) {
    this.#wsBaseUrl = deps.wsBaseUrl;
    this.#tokenProvider = deps.tokenProvider;
    this.#wsConnector = deps.wsConnector;
    this.#onIntervention = deps.onIntervention;
  }

  /**
   * Opens the host's intervention-only WS subscription. Reads the token from SecretStorage
   * (per start, never cached on the instance): with NO token this is a quiet no-op (not
   * started). Otherwise it builds the gateway WS URL filtered to `intervention-needed` with
   * the token in the `?token=` query (token ONLY in the URL — see TOKEN DISCIPLINE) and
   * opens it via the injected connector. Restart-safe: any prior handle is closed first so
   * a stale socket can never leak (single active handle).
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
    // stream light (interventions only); the gateway already excludes other kinds.
    const url = `${this.#wsBaseUrl}/ws?kind=${INTERVENTION_KIND}&token=${encodeURIComponent(token)}`;
    this.#handle = this.#wsConnector.open(url, {
      onOpen: () => {
        /* nothing to do on open — frames drive the notifications. */
      },
      onMessage: (data) => this.#onFrame(data),
      onClose: () => {
        /* server/error close: leave #handle as-is. stop() (or a restart) clears it. The
           notifier doesn't auto-reconnect — extension.ts restarts it on the next
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
   * throw); only an `intervention-needed` event drives `onIntervention` (non-intervention
   * frames are ignored even though the `kind=` filter should already exclude them). The
   * reason is read from `payload.reason`, falling back to a generic string.
   */
  #onFrame(data: string): void {
    let parsed: unknown;
    try {
      parsed = JSON.parse(data);
    } catch {
      return; // malformed frame — ignore (no throw, no callback).
    }
    const event = asInterventionEvent(parsed);
    if (event === undefined) {
      return; // not an intervention-needed event — ignore defensively.
    }
    this.#onIntervention({
      project: event.project,
      task: event.task,
      reason: event.reason,
    });
  }
}

/** The minimal gateway-event fields the notifier reads from a frame, after the
 * intervention-kind check + reason extraction. */
interface ParsedIntervention {
  project: string;
  task: string;
  reason: string;
}

/**
 * Narrows an already-JSON-parsed frame to an `intervention-needed` event, returning the
 * distilled `{ project, task, reason }` or `undefined` if it isn't one. Tolerant of the
 * full gateway event shape (`{ project, task, kind, payload }`, plus extra fields like
 * `ts`): requires `kind === "intervention-needed"` and string project/task, and reads
 * `payload.reason` if it's a non-empty string (else the generic fallback). Never throws.
 */
function asInterventionEvent(x: unknown): ParsedIntervention | undefined {
  if (x === null || typeof x !== "object") {
    return undefined;
  }
  const o = x as Record<string, unknown>;
  if (o.kind !== INTERVENTION_KIND) {
    return undefined;
  }
  if (typeof o.project !== "string" || typeof o.task !== "string") {
    return undefined;
  }
  return {
    project: o.project,
    task: o.task,
    reason: reasonOf(o.payload),
  };
}

/** Extracts a human reason from an intervention event's payload: `payload.reason` when it
 * is a non-empty string, else the generic fallback. Tolerant of a missing/non-object
 * payload. Pure; never throws. */
function reasonOf(payload: unknown): string {
  if (payload !== null && typeof payload === "object") {
    const reason = (payload as Record<string, unknown>).reason;
    if (typeof reason === "string" && reason.length > 0) {
      return reason;
    }
  }
  return DEFAULT_REASON;
}
