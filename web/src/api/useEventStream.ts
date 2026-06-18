// useEventStream: a React hook that opens a WebSocket to the gateway's /ws
// endpoint (ADR-0025) and exposes the live event stream. Because the browser
// WebSocket API cannot set an Authorization header, the bearer token is passed as
// a ?token= query param (per ADR-0025/0026); the same query also carries the
// optional event filter (project/task/phase/kind/intervention). Incoming frames
// are parsed as Event JSON. The socket is torn down on unmount or when the inputs
// change.
import { useEffect, useRef, useState } from "react";
import { eventQueryString } from "./client.ts";
import type { Event, EventQuery } from "./types.ts";

export type StreamState = "connecting" | "open" | "closed";

export interface EventStream {
  // events holds the received events, oldest→newest, capped at maxEvents.
  events: Event[];
  // latest is the most recently received event (or null before any arrive).
  latest: Event | null;
  state: StreamState;
}

export interface UseEventStreamOptions {
  // wsBase is the WebSocket origin (e.g. ws://localhost:8080). Empty string =
  // derive from the current page origin (http→ws, https→wss).
  wsBase?: string;
  token: string;
  filter?: EventQuery;
  // maxEvents caps the retained buffer so a long-lived stream can't grow without
  // bound. Defaults to 200.
  maxEvents?: number;
  // enabled gates the connection; when false no socket is opened (e.g. before
  // auth). Defaults to true.
  enabled?: boolean;
}

// wsUrl builds the full ws(s):// URL with the token + filter query. The token is
// URL-encoded by URLSearchParams in eventQueryString's sibling logic; here we
// append it explicitly so it is never confused with a filter field.
export function wsUrl(opts: {
  wsBase?: string | undefined;
  token: string;
  filter?: EventQuery | undefined;
}): string {
  const base = (opts.wsBase ?? deriveWsBase()).replace(/\/$/, "");
  const filterQs = eventQueryString(opts.filter ?? {});
  const sep = filterQs.length > 0 ? "&" : "?";
  return `${base}/ws${filterQs}${sep}token=${encodeURIComponent(opts.token)}`;
}

// deriveWsBase converts the page's HTTP origin into a ws(s) origin for
// same-origin deployments. Guards against a non-browser (test) environment.
function deriveWsBase(): string {
  if (typeof window === "undefined" || !window.location) {
    return "";
  }
  const { protocol, host } = window.location;
  const wsProto = protocol === "https:" ? "wss:" : "ws:";
  return `${wsProto}//${host}`;
}

export function useEventStream(opts: UseEventStreamOptions): EventStream {
  const { wsBase, token, filter, maxEvents = 200, enabled = true } = opts;
  const [events, setEvents] = useState<Event[]>([]);
  const [latest, setLatest] = useState<Event | null>(null);
  const [state, setState] = useState<StreamState>("closed");

  // Serialize the filter so the effect only re-runs when it actually changes,
  // not on every render (filter is usually a fresh object literal).
  const filterKey = JSON.stringify(filter ?? {});
  const maxRef = useRef(maxEvents);
  maxRef.current = maxEvents;

  useEffect(() => {
    if (!enabled || token.length === 0) {
      setState("closed");
      return;
    }

    setState("connecting");
    const url = wsUrl({ wsBase, token, filter: JSON.parse(filterKey) as EventQuery });
    const ws = new WebSocket(url);

    ws.onopen = () => setState("open");

    ws.onmessage = (ev: MessageEvent) => {
      let parsed: Event;
      try {
        parsed = JSON.parse(String(ev.data)) as Event;
      } catch {
        // Ignore malformed frames rather than crashing the stream.
        return;
      }
      setLatest(parsed);
      setEvents((prev) => {
        const next = [...prev, parsed];
        return next.length > maxRef.current
          ? next.slice(next.length - maxRef.current)
          : next;
      });
    };

    ws.onclose = () => setState("closed");
    ws.onerror = () => setState("closed");

    return () => {
      // Detach handlers before closing so a late event can't update unmounted state.
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
    };
    // filterKey captures the filter; wsBase/token/enabled are primitives.
  }, [wsBase, token, filterKey, enabled]);

  return { events, latest, state };
}
