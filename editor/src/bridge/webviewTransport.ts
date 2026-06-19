// Conductor Platform — webview-side transports (Faz-4 DALGA 4B, 4B-2).
//
// SCOPE (4B-2): the WEBVIEW half of the fork transport seam. It implements the 4A-1
// contracts (HttpTransport + EventTransport) by speaking to the extension HOST over
// postMessage instead of touching the network directly. The webview never holds the
// token: it asks the host (which attaches auth) to perform each REST call / open each WS
// stream, and the host posts results back keyed by a correlation id.
//
// STRUCTURAL MATCH (NOT an import): these interfaces are defined LOCALLY to mirror
// web/src/api/client.ts (HttpResponse/HttpRequest/HttpTransport) and
// web/src/api/useEventStream.ts (EventQuery/EventSubscription/EventTransport). The 4A-1
// ApiClient/useEventStream accept an injected transport; wiring THESE into them (so the
// 3B React panels reuse the web ApiClient/hook unchanged) is 4B-3 — we deliberately do
// NOT import from web/ here.
//
// DETERMINISM: correlation ids are a monotonic "b1","b2",… counter — NO Math.random /
// Date — so tests are reproducible.

import type { HostMessage, WebviewRequest } from "./protocol";
import { isHostMessage } from "./protocol";

/**
 * The raw HTTP result the transport returns: the body is already read as text (the
 * ApiClient owns JSON parsing). Mirrors web HttpResponse.
 */
export interface HttpResponse {
  status: number;
  ok: boolean;
  body: string;
}

/**
 * A single request to perform. `path` is gateway-relative; `body` (if any) is already
 * serialized; `contentType` is sent when a body exists. Mirrors web HttpRequest.
 */
export interface HttpRequest {
  method: string;
  path: string;
  body?: string;
  contentType?: string;
}

/** The REST seam. Auth is the transport's responsibility — here the host attaches it.
 * Mirrors web HttpTransport. */
export interface HttpTransport {
  send(req: HttpRequest): Promise<HttpResponse>;
}

/** The event filter: a flat record of primitives (project/task/phase/kind/…). Mirrors
 * the web EventQuery's structural usage. */
export type EventQuery = Record<string, string | number | boolean>;

/** A live subscription handle; close() ends the stream. Mirrors web EventSubscription. */
export interface EventSubscription {
  close(): void;
}

/**
 * The event seam. `subscribe` opens a stream for the filter and drives the callbacks;
 * the returned handle's close() ends it. `onEvent` receives a PARSED event object (the
 * host posts the raw JSON string; this side parses it). Mirrors web EventTransport.
 */
export interface EventTransport {
  subscribe(opts: {
    filter?: EventQuery;
    onOpen: () => void;
    onEvent: (e: unknown) => void;
    onClose: () => void;
  }): EventSubscription;
}

/** Posts a message to the extension host. In the real webview this is the VS Code
 * `acquireVsCodeApi().postMessage`; tests pass a fake recorder. */
export interface WebviewPoster {
  postMessage(message: unknown): void;
}

/**
 * Subscribes to host→webview messages, returning an unsubscribe thunk. In the real
 * webview this wraps `window.addEventListener("message", e => listener(e.data))`; tests
 * drive the listener manually. Exported as a seam so the real wiring (4B-3) and tests
 * share one shape.
 */
export type SubscribeToMessages = (listener: (msg: unknown) => void) => () => void;

/**
 * Minimal structural shape of the webview `window`'s message-event wiring. Declared
 * locally (rather than pulling in the DOM lib — this module is compiled with the Node
 * lib only) so `subscribeToMessages` is well-typed without widening the project's libs.
 */
interface MessageWindow {
  addEventListener(type: "message", listener: (e: { data: unknown }) => void): void;
  removeEventListener(type: "message", listener: (e: { data: unknown }) => void): void;
}

/**
 * The real browser message subscription: forwards each message event's `data` to the
 * listener and returns a detach thunk. Only usable inside a webview (reaches `window` via
 * globalThis); it is the I/O edge and is therefore NOT exercised by the headless tests
 * (they pass their own fake subscribe seam). Throws if no message-window is present so a
 * misuse outside a webview fails loudly rather than silently no-op'ing.
 */
export function subscribeToMessages(listener: (msg: unknown) => void): () => void {
  const win = (globalThis as { window?: MessageWindow }).window;
  if (win === undefined) {
    throw new Error("subscribeToMessages requires a webview window");
  }
  const handler = (e: { data: unknown }): void => listener(e.data);
  win.addEventListener("message", handler);
  return () => win.removeEventListener("message", handler);
}

/** A pending REST call awaiting its host reply. */
interface PendingRest {
  resolve(res: HttpResponse): void;
  reject(err: Error): void;
}

/** A registered event subscription's callbacks. */
interface ActiveSubscription {
  onOpen: () => void;
  onEvent: (e: unknown) => void;
  onClose: () => void;
}

/**
 * Builds the webview-side `{ http, events }` transports over a poster + a message
 * subscription. Maintains a monotonic id counter, a pending-REST map, and an
 * active-subscription map; a single inbound-message router (guarded by `isHostMessage`)
 * dispatches rest-response/rest-error to the pending promise and event-* to the matching
 * subscription. The token never appears here — every authed detail is host-side.
 */
export function createBridgeTransports(
  poster: WebviewPoster,
  subscribe: SubscribeToMessages,
): { http: HttpTransport; events: EventTransport } {
  let counter = 0;
  const nextId = (): string => `b${++counter}`;

  const pending = new Map<string, PendingRest>();
  const subs = new Map<string, ActiveSubscription>();

  // Single inbound router. Ignores anything that isn't a well-formed HostMessage so a
  // foreign frame can't spoof a response/event into our maps.
  subscribe((msg) => {
    if (!isHostMessage(msg)) {
      return;
    }
    routeHostMessage(msg, pending, subs);
  });

  const http: HttpTransport = {
    send(req: HttpRequest): Promise<HttpResponse> {
      const id = nextId();
      return new Promise<HttpResponse>((resolve, reject) => {
        pending.set(id, { resolve, reject });
        // Build the message omitting absent optionals (exactOptionalPropertyTypes).
        const message: WebviewRequest = {
          kind: "rest-request",
          id,
          method: req.method,
          path: req.path,
          ...(req.body !== undefined ? { body: req.body } : {}),
          ...(req.contentType !== undefined ? { contentType: req.contentType } : {}),
        };
        poster.postMessage(message);
      });
    },
  };

  const events: EventTransport = {
    subscribe(opts): EventSubscription {
      const id = nextId();
      subs.set(id, { onOpen: opts.onOpen, onEvent: opts.onEvent, onClose: opts.onClose });
      const message: WebviewRequest = {
        kind: "event-subscribe",
        id,
        ...(opts.filter !== undefined ? { filter: opts.filter } : {}),
      };
      poster.postMessage(message);
      return {
        close() {
          // Drop the local subscription first so a late event-close from the host (the
          // host posts one on unsubscribe-driven socket close) is a quiet no-op.
          subs.delete(id);
          poster.postMessage({ kind: "event-unsubscribe", id } satisfies WebviewRequest);
        },
      };
    },
  };

  return { http, events };
}

/**
 * Routes one validated HostMessage to its waiting promise / subscription. REST replies
 * are removed from the pending map (one reply per request); event frames look up the
 * subscription and, for `event-close`, drop it after firing onClose. Parse errors on an
 * event frame are ignored (the web impl ignores malformed frames too).
 */
function routeHostMessage(
  msg: HostMessage,
  pending: Map<string, PendingRest>,
  subs: Map<string, ActiveSubscription>,
): void {
  switch (msg.kind) {
    case "rest-response": {
      const p = pending.get(msg.id);
      if (p) {
        pending.delete(msg.id);
        p.resolve({ status: msg.status, ok: msg.ok, body: msg.body });
      }
      return;
    }
    case "rest-error": {
      const p = pending.get(msg.id);
      if (p) {
        pending.delete(msg.id);
        p.reject(new Error(msg.message));
      }
      return;
    }
    case "event-open": {
      subs.get(msg.id)?.onOpen();
      return;
    }
    case "event-message": {
      const sub = subs.get(msg.id);
      if (!sub) {
        return;
      }
      let parsed: unknown;
      try {
        parsed = JSON.parse(msg.data);
      } catch {
        // Ignore malformed frames rather than crashing the stream (matches the web impl).
        return;
      }
      sub.onEvent(parsed);
      return;
    }
    case "event-close": {
      const sub = subs.get(msg.id);
      if (sub) {
        subs.delete(msg.id);
        sub.onClose();
      }
      return;
    }
  }
}
