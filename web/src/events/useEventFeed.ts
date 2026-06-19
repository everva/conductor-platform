// useEventFeed: the data layer for the full live event stream view (3B-2). It
// composes the two gateway surfaces (ADR-0025) so the feed is never empty AND stays
// live:
//   1. on mount (and whenever the filter changes) it BACKFILLS recent history via
//      ApiClient.listEvents({ ...filter, limit }) — ascending by TS, per events.go;
//   2. it attaches the live useEventStream WebSocket for new events with the SAME
//      filter so history and the live tail are consistent.
//
// Events from both sources are merged into ONE buffer, DEDUPED by event id (the
// gateway assigns ids; a history row and a live frame can be the same event), kept
// ascending by ts, and CAPPED (oldest dropped) so a long session can't grow without
// bound. A UI-side PAUSE freezes the displayed buffer (new live events are dropped
// from the display while paused — this is a display freeze, NOT the conductor pause).
//
// The history fetch is INJECTABLE (loadHistory) so tests drive it without a network
// and never duplicate client logic. The WebSocket is owned by useEventStream, which
// tears its socket down on filter change / unmount — we do not open a second socket.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiClient, ApiError } from "../api/client.ts";
import { useEventStream } from "../api/useEventStream.ts";
import type { EventTransport, StreamState } from "../api/useEventStream.ts";
import type { Event, EventQuery } from "../api/types.ts";

// EventFeedError mirrors fleet's FleetError: a surfaced backfill failure with a 401
// flag so the app can prompt re-auth.
export interface EventFeedError {
  message: string;
  status: number | null;
  unauthorized: boolean;
}

// HistoryLoader is the single history dependency the feed needs. ApiClient satisfies
// it (listEvents); tests pass a fake.
export interface HistoryLoader {
  listEvents(query?: EventQuery): Promise<Event[]>;
}

export interface UseEventFeedOptions {
  token: string;
  // filter drives BOTH the history backfill query and the live WS subscription.
  filter?: EventQuery;
  // historyLimit caps the backfill window (most-recent N). Defaults to 200.
  historyLimit?: number;
  // bufferCap bounds the in-memory buffer; the oldest are dropped past it. Default 1000.
  bufferCap?: number;
  // paused freezes the DISPLAY: live events arriving while paused are dropped (not
  // buffered) so resuming shows the current tail, not a backlog flush. Default false.
  paused?: boolean;
  // makeHistory builds the history loader (default: real ApiClient). Injectable for tests.
  makeHistory?: (token: string) => HistoryLoader;
  // enabled gates the backfill + live subscription. Defaults to `token.length > 0`
  // so readiness is decoupled from auth: an injected-transport host (the fork) can
  // pass `enabled: true` to run token-free. Behavior-identical for existing callers.
  enabled?: boolean;
  // eventTransport overrides the default WebSocketTransport for the live event
  // subscription (e.g. the fork postMessage bridge). When set, the stream is
  // token-agnostic and the transport owns auth.
  eventTransport?: EventTransport;
}

export interface EventFeed {
  // events is the merged, deduped, ascending-by-ts buffer (oldest→newest). The view
  // renders it newest-first.
  events: Event[];
  // state is the live WebSocket connection state.
  state: StreamState;
  loading: boolean;
  error: EventFeedError | null;
  // reload re-runs the history backfill with the current filter (clears + refills).
  reload: () => void;
  // clear empties the displayed buffer without touching the subscription.
  clear: () => void;
}

function toFeedError(err: unknown): EventFeedError {
  if (err instanceof ApiError) {
    return {
      message:
        err.status === 401 ? "Authentication failed." : `Gateway error (${err.status}).`,
      status: err.status,
      unauthorized: err.status === 401,
    };
  }
  return { message: "Could not reach the gateway.", status: null, unauthorized: false };
}

// mergeById appends `incoming` onto `base`, skipping ids already present, then sorts
// ascending by ts (history is ascending; live is newest-last; ties keep insertion
// order via a stable sort) and caps to the most-recent `cap` events.
export function mergeById(base: Event[], incoming: Event[], cap: number): Event[] {
  const seen = new Set<string>();
  const out: Event[] = [];
  for (const e of base) {
    if (!seen.has(e.id)) {
      seen.add(e.id);
      out.push(e);
    }
  }
  for (const e of incoming) {
    if (!seen.has(e.id)) {
      seen.add(e.id);
      out.push(e);
    }
  }
  out.sort((a, b) => (a.ts < b.ts ? -1 : a.ts > b.ts ? 1 : 0));
  return out.length > cap ? out.slice(out.length - cap) : out;
}

export function useEventFeed(opts: UseEventFeedOptions): EventFeed {
  const {
    token,
    filter,
    historyLimit = 200,
    bufferCap = 1000,
    paused = false,
    makeHistory,
    eventTransport,
  } = opts;
  // Readiness defaults to "has a token" but is overridable (see UseEventFeedOptions):
  // byte-for-byte for existing callers; a token-free fork mount passes enabled: true.
  const enabled = opts.enabled ?? (token.length > 0);

  const [events, setEvents] = useState<Event[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<EventFeedError | null>(null);
  // reloadNonce forces a fresh backfill (reload button) without changing the filter.
  const [reloadNonce, setReloadNonce] = useState(0);

  // Serialize the filter so effects key off a stable string, not a fresh object
  // literal each render (mirrors useEventStream's own filterKey).
  const filterKey = JSON.stringify(filter ?? {});

  // Keep the (possibly inline / default) factory in a ref so the loader's identity is
  // stable across renders. If we depended on `makeHistory` directly, an inline default
  // (`(t) => new ApiClient(...)`) would get a fresh identity every render and re-run
  // the backfill on each WS-driven re-render — clearing then refetching events and
  // making the feed flicker. Rebuild the loader ONLY when the token changes.
  const makeHistoryRef = useRef(makeHistory);
  makeHistoryRef.current = makeHistory;
  const history = useMemo<HistoryLoader>(() => {
    const factory = makeHistoryRef.current ?? ((t: string) => new ApiClient({ token: t }));
    return factory(token);
  }, [token]);

  const capRef = useRef(bufferCap);
  capRef.current = bufferCap;
  const pausedRef = useRef(paused);
  pausedRef.current = paused;

  // Guard against state updates after unmount (a slow backfill resolving late).
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // History backfill: runs on mount, on filter change, and on an explicit reload. It
  // RESETS the buffer to the freshly-fetched window so a filter change doesn't leave
  // stale rows from the previous query mixed in.
  useEffect(() => {
    if (!enabled) {
      setEvents([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const parsed = JSON.parse(filterKey) as EventQuery;
    const query: EventQuery = { ...parsed, limit: historyLimit };
    history
      .listEvents(query)
      .then((rows) => {
        if (cancelled || !mountedRef.current) {
          return;
        }
        setEvents(mergeById([], rows, capRef.current));
        setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled || !mountedRef.current) {
          return;
        }
        setError(toFeedError(err));
      })
      .finally(() => {
        if (!cancelled && mountedRef.current) {
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [history, filterKey, historyLimit, enabled, token, reloadNonce]);

  // Live subscription. useEventStream owns the WebSocket and tears it down when these
  // inputs change (filter/token/enabled) or on unmount — no second socket, no leak.
  const streamFilter = useMemo<EventQuery | undefined>(() => {
    const parsed = JSON.parse(filterKey) as EventQuery;
    return Object.keys(parsed).length > 0 ? parsed : undefined;
  }, [filterKey]);
  const stream = useEventStream({
    token,
    // Resolved enabled already incorporates the token check via its default, so
    // forward it directly (an injected eventTransport can run token-free).
    enabled,
    // Match the buffer cap so the stream's own retention doesn't silently drop below it.
    maxEvents: bufferCap,
    ...(streamFilter ? { filter: streamFilter } : {}),
    ...(eventTransport ? { transport: eventTransport } : {}),
  });

  // Fold the live stream buffer into the merged display buffer. We fold the WHOLE
  // stream.events tail (not just `latest`) so that even if several frames land in one
  // React batch none are missed; mergeById dedupes against what's already shown, so
  // re-folding the same events is a no-op. While paused, drop the update entirely
  // (display freeze): on resume the next frame folds in the then-current tail.
  const liveEvents = stream.events;
  useEffect(() => {
    if (liveEvents.length === 0 || pausedRef.current) {
      return;
    }
    setEvents((prev) => mergeById(prev, liveEvents, capRef.current));
  }, [liveEvents]);

  const reload = useCallback(() => {
    setReloadNonce((n) => n + 1);
  }, []);

  const clear = useCallback(() => {
    setEvents([]);
  }, []);

  return { events, state: stream.state, loading, error, reload, clear };
}
