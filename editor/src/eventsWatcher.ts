// Conductor Platform — host-side event watcher feeding the native "Conductor Events" panel
// (Faz-Q / Q2, ADR-0045).
//
// The cockpit used to bury the live feed in a webview "Events" TAB. Q2 promotes it to a
// dedicated NATIVE surface (a TreeView in the panel area). This watcher is its data layer:
// it keeps a bounded, newest-first ring of recent events, fed by
//   1. a one-shot REST BACKFILL (GET {rest}/events?limit=N) on start, so the panel is never
//      empty on open; and
//   2. a live WS subscription (GET {ws}/ws) for new events.
// It fires `onChange` whenever the ring changes so the tree provider can refetch.
//
// It deliberately MIRRORS notifier.ts / DiffObserver: same WsConnector/WsHandle/TokenProvider
// seams (no duplicate `ws` adapter), single active handle (restart closes the predecessor),
// defensive frame parsing, and start()/stop() tied to the ConnectionManager state in
// extension.ts. It depends on no `vscode` and no real network, so vitest drives it headlessly.
//
// TOKEN DISCIPLINE (HARD): the token is read from the TokenProvider (SecretStorage, host-side)
// and placed ONLY into the WS URL's `?token=` query and the REST `Authorization` header — never
// into a FeedEvent (which carries only id/ts/project/task/phase/kind), never logged, never
// returned. A sentinel-token leak-guard test in eventsWatcher.test.ts proves it.

import type { TokenProvider, WsConnector, WsHandle } from "./bridge/hostBridge";

/** Default cap on the in-memory ring (matches the cockpit's history window). */
const DEFAULT_CAP = 200;

/**
 * A distilled event the native panel renders — a tolerant projection of a gateway event
 * (`{ id, ts, project, task, phase, kind, payload }`, plus extras). Token-FREE by construction:
 * only the locating + classifying fields the tree shows.
 */
export interface FeedEvent {
  readonly id: string;
  readonly ts: string;
  readonly project: string;
  readonly task: string;
  readonly phase: string;
  readonly kind: string;
  // OPTIONAL, additive (for the "Now"/Activity view): a few token-FREE payload fields the live
  // status summary reads. Present only on the events that carry them; the events tree ignores
  // them. elapsedSeconds/filesChanged ride on kind=="progress"; result/summary on the verdict
  // (kind=="decision"). summary is the agent's secret-free description (build/git error text).
  readonly elapsedSeconds?: number;
  readonly filesChanged?: number;
  readonly result?: string;
  readonly summary?: string;
  // The granular progress step (provisioning|developing|verifying) — the event's coarse Phase is
  // a valid events.Phase (plan/develop/verify), so the precise step rides in the payload.
  readonly step?: string;
  // The ORIGINAL parsed event object (token-FREE: gateway events never carry a token), retained so
  // the live Stream can drill down to the full detailed JSON on demand. Not rendered by default.
  readonly raw?: unknown;
}

/**
 * Dependencies for an EventsWatcher. `restBaseUrl` is the normalized gateway base (for the
 * backfill GET); `wsBaseUrl` is the ws-derived base (for the live subscription) — both
 * HOST-controlled so the watcher only ever extends the host's own base. `tokenProvider`/
 * `wsConnector` reuse the 4B-2 seams. `fetchImpl` is injectable for tests. `onChange` fires
 * after any ring change. `cap` bounds the ring.
 */
export interface EventsWatcherDeps {
  restBaseUrl: string;
  wsBaseUrl: string;
  tokenProvider: TokenProvider;
  wsConnector: WsConnector;
  onChange: () => void;
  fetchImpl?: typeof fetch;
  cap?: number;
  // OPTIONAL (M2): called when the live WS closes (server/error close), so the host can drive
  // auto-reconnect. Token-free (no args). Fires only for the active handle's close — a `stop()`
  // closes the handle too, so the host guards on its own connection intent before reconnecting.
  onClose?: () => void;
}

/**
 * Host-side event watcher. Construct one per activation with the real wsConnector + a
 * TokenProvider reading SecretStorage; `start()`/`stop()` are tied to the ConnectionManager's
 * state in extension.ts (start on "connected", stop otherwise). Holds a single WS handle at a
 * time — restarting closes the prior one first (no leak, the FAZ-4 review lesson).
 */
export class EventsWatcher {
  readonly #restBaseUrl: string;
  readonly #wsBaseUrl: string;
  readonly #tokenProvider: TokenProvider;
  readonly #wsConnector: WsConnector;
  readonly #onChange: () => void;
  readonly #fetch: typeof fetch;
  readonly #cap: number;
  readonly #onClose: (() => void) | undefined;

  /** Newest-first ring of recent events (most-recent at index 0). */
  #ring: FeedEvent[] = [];
  /** The single active WS handle (undefined when stopped / never started). */
  #handle: WsHandle | undefined;
  /** Monotonic open generation. Bumped on every start()/stop() so a SUPERSEDED handle's onClose
   * (after a restart or deliberate stop) is recognized as stale and does NOT signal #onClose. */
  #generation = 0;

  constructor(deps: EventsWatcherDeps) {
    this.#restBaseUrl = deps.restBaseUrl;
    this.#wsBaseUrl = deps.wsBaseUrl;
    this.#tokenProvider = deps.tokenProvider;
    this.#wsConnector = deps.wsConnector;
    this.#onChange = deps.onChange;
    this.#fetch = deps.fetchImpl ?? fetch;
    this.#cap = deps.cap ?? DEFAULT_CAP;
    this.#onClose = deps.onClose;
  }

  /** The current ring (newest-first). The tree provider reads this on getChildren. */
  events(): readonly FeedEvent[] {
    return this.#ring;
  }

  /**
   * Opens the watcher: reads the token (no token → quiet no-op), BACKFILLS recent history via
   * REST, then opens the live WS. Restart-safe: any prior handle is closed first. The token
   * rides only in the Authorization header (backfill) + the `?token=` query (WS).
   */
  async start(): Promise<void> {
    const token = await this.#tokenProvider.getToken();
    if (token === undefined || token === "") {
      return; // not connected → nothing to subscribe to.
    }

    // Close a predecessor BEFORE opening a new one (single active handle; no leak). Bump the
    // generation FIRST so the predecessor's onClose is recognized as stale (see the guard below).
    const gen = ++this.#generation;
    this.#handle?.close();
    this.#handle = undefined;

    // 1) REST backfill so the panel opens populated. Best-effort: any failure leaves the ring
    //    as-is (the live tail will fill it). The events arrive ascending; seed merges + sorts.
    try {
      const res = await this.#fetch(`${this.#restBaseUrl}/events?limit=${this.#cap}`, {
        method: "GET",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.ok) {
        const body = (await res.json()) as unknown;
        if (Array.isArray(body)) {
          this.#seed(body.map(toFeedEvent).filter((e): e is FeedEvent => e !== undefined));
        }
      }
    } catch {
      // best-effort backfill — ignore.
    }

    // 2) Live WS for new events (all kinds). Token only in the URL query.
    const url = `${this.#wsBaseUrl}/ws?token=${encodeURIComponent(token)}`;
    this.#handle = this.#wsConnector.open(url, {
      onOpen: () => {
        /* nothing to do on open — frames drive the ring. */
      },
      onMessage: (data) => this.#onFrame(data),
      onClose: () => {
        // An UNEXPECTED server/error close of THIS generation's handle drives auto-reconnect (M2).
        // A stop()/restart bumps the generation, so a superseded handle's close is ignored — the
        // host never sees a reconnect signal it caused itself.
        if (gen === this.#generation) {
          this.#onClose?.();
        }
      },
    });
  }

  /** Closes the active WS handle (idempotent; safe if never started). Leaves the ring intact.
   * Bumps the generation so the close is recognized as deliberate (no reconnect signal). */
  stop(): void {
    this.#generation++;
    this.#handle?.close();
    this.#handle = undefined;
  }

  /** Merges a backfill batch into the ring (dedupe by id, newest-first, capped), then notifies. */
  #seed(events: FeedEvent[]): void {
    const seen = new Set(this.#ring.map((e) => e.id));
    const merged = [...this.#ring];
    for (const e of events) {
      if (!seen.has(e.id)) {
        seen.add(e.id);
        merged.push(e);
      }
    }
    merged.sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0)); // newest-first
    this.#ring = merged.slice(0, this.#cap);
    this.#onChange();
  }

  /** Parses one inbound WS frame defensively and pushes a recognized event onto the ring. */
  #onFrame(data: string): void {
    let parsed: unknown;
    try {
      parsed = JSON.parse(data);
    } catch {
      return; // malformed frame — ignore (no throw, no callback).
    }
    const event = toFeedEvent(parsed);
    if (event === undefined) {
      return;
    }
    // Dedupe (a live frame can echo a backfilled row) + keep newest-first + cap.
    if (this.#ring.some((e) => e.id === event.id)) {
      return;
    }
    this.#ring = [event, ...this.#ring].slice(0, this.#cap);
    this.#onChange();
  }
}

/**
 * Distills an unknown gateway event (REST row or WS frame) to a FeedEvent, or undefined if it
 * isn't a usable event. Tolerant of the full event shape + extra fields; requires a string
 * `kind` (the one field every event has). `id` falls back to a synthesized key when absent so
 * dedupe + tree keys stay stable. Pure; never throws. Exported for tests.
 */
export function toFeedEvent(x: unknown): FeedEvent | undefined {
  if (x === null || typeof x !== "object") {
    return undefined;
  }
  const o = x as Record<string, unknown>;
  if (typeof o.kind !== "string" || o.kind === "") {
    return undefined;
  }
  const ts = typeof o.ts === "string" ? o.ts : "";
  const project = typeof o.project === "string" ? o.project : "";
  const task = typeof o.task === "string" ? o.task : "";
  const phase = typeof o.phase === "string" ? o.phase : "";
  const id =
    typeof o.id === "string" && o.id !== ""
      ? o.id
      : `${ts}:${project}:${task}:${phase}:${o.kind}`;
  // Pull the optional token-free payload fields the "Now" view uses, INCLUDING each key only when
  // defined (exactOptionalPropertyTypes forbids an explicit `undefined` on an optional field).
  const p = o.payload && typeof o.payload === "object" ? (o.payload as Record<string, unknown>) : {};
  return {
    id,
    ts,
    project,
    task,
    phase,
    kind: o.kind,
    ...(typeof p.elapsed_seconds === "number" ? { elapsedSeconds: p.elapsed_seconds } : {}),
    ...(typeof p.files_changed === "number" ? { filesChanged: p.files_changed } : {}),
    ...(typeof p.result === "string" && p.result !== "" ? { result: p.result } : {}),
    ...(typeof p.summary === "string" && p.summary !== "" ? { summary: p.summary } : {}),
    ...(typeof p.step === "string" && p.step !== "" ? { step: p.step } : {}),
    // Retain the original object for the Stream's JSON drill-down (token-free gateway event).
    raw: x,
  };
}
