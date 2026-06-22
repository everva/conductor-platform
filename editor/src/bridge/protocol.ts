// Conductor Platform — typed host↔webview postMessage protocol (Faz-4 DALGA 4B, 4B-2).
//
// SCOPE (4B-2): the wire contract for the "fork implementation" of the 4A-1 transport
// seam. The extension HOST owns auth + does the real REST/WS against the gateway; the
// WEBVIEW only asks the host to perform a request / open an event stream over
// postMessage. This module defines BOTH message directions (REST + event kinds) as
// discriminated unions plus DEFENSIVE runtime type guards: postMessage hands each side
// an untrusted `unknown` (a compromised/foreign frame could post junk), so the host and
// the webview MUST validate every inbound message before acting on it.
//
// TOKEN DISCIPLINE (HARD): NO message kind on either direction carries the bearer
// token. The token is attached host-side only (the REST Authorization header + the WS
// `?token=` query, see hostBridge.ts) and never appears in a `HostMessage`. There is
// nothing token-shaped to define here, by design — the webview supplies only
// `method`+`path`(+body/filter) and the host joins it onto its own (authed) base URL.

/**
 * Webview→Host messages. The webview can only ask the host to (a) perform a REST call
 * by `method`+`path` (the host joins its own baseUrl + validates the path), (b) open an
 * event stream for an optional filter, or (c) close one. Each carries a webview-minted
 * correlation `id` so responses/event frames route back to the right caller.
 *
 * NOTE: `path` is gateway-RELATIVE (e.g. "/projects"); the host validates it must start
 * with exactly one "/" and then joins it onto baseUrl, so the webview can never retarget
 * the authed request to another origin. `body` (if present) is ALREADY serialized.
 */
export type WebviewRequest =
  | {
      kind: "rest-request";
      id: string;
      method: string;
      path: string;
      body?: string;
      contentType?: string;
    }
  | { kind: "event-subscribe"; id: string; filter?: Record<string, string | number | boolean> }
  | { kind: "event-unsubscribe"; id: string };

/**
 * Host→Webview messages. The REST reply is either a `rest-response` (the gateway's
 * status/ok + the body read as text) or a `rest-error` (a GENERIC, token-free message —
 * the raw error/URL is never forwarded). The event stream surfaces as open/message/close
 * frames keyed by the subscription `id`. None of these carry the token.
 */
export type HostMessage =
  | { kind: "rest-response"; id: string; status: number; ok: boolean; body: string }
  | { kind: "rest-error"; id: string; message: string }
  | { kind: "event-open"; id: string }
  | { kind: "event-message"; id: string; data: string }
  | { kind: "event-close"; id: string };

// ── Selection control channel (host↔webview), SEPARATE from the REST/event bridge ────────
// The native sessions tree (host) OWNS the current selection; the cockpit webview is a
// PROJECTION of it. The host posts `select` to push the selected project (and optionally a
// task) onto the cockpit. These carry NO correlation `id`, so the bridge routers (isHostMessage
// / isWebviewRequest, both of which require a string `id`) reject them and the REST/event maps
// are untouched. The fork webview's app + the CommandCenterPanel listen for them directly.
// TOKEN-FREE (only project/task ids / a readiness ping) — like every other message here, by
// design. (Faz-Q / Q0: GENERALIZES the N3 `navigate-session` — which was task-only — into the
// single selection channel the tree drives; `select` WITH a `task` IS the old deep-link-to-session.)

/**
 * Host→Webview control: SELECT a project, or a specific task within it. `task` omitted → select
 * the project (the cockpit scopes its board / surfaces to it); `task` present → ALSO open that
 * task's SessionView (the deep-link a sessions-tree task click drives, formerly `navigate-session`).
 * Carries no token — only the project / task ids.
 */
export type HostSelectMessage = { kind: "select"; project: string; task?: string };

/** Webview→Host control: the fork app has mounted + is listening, so the host can flush a
 * selection buffered during the cold-start window (avoids a lost first selection). */
export type WebviewControlMessage = { kind: "webview-ready" };

/**
 * Defensive guard: is `x` a host→webview `select` control message? The fork app calls this on
 * every inbound window message and acts only on a well-formed one (a foreign frame can't spoof
 * a selection). Requires a string `project`; `task` is optional but, if present, must be a
 * string. Carries no token.
 */
export function isSelect(x: unknown): x is HostSelectMessage {
  return isObject(x) && x.kind === "select" && hasString(x, "project") && optString(x, "task");
}

/** Defensive guard: is `x` the webview-ready control ping? The CommandCenterPanel calls this on
 * inbound webview messages to flush a buffered selection. */
export function isWebviewReady(x: unknown): x is WebviewControlMessage {
  return isObject(x) && x.kind === "webview-ready";
}

/** True if `x` is a non-null object — the precondition for every guard below. */
function isObject(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null;
}

/** True if `o[key]` is a string. */
function hasString(o: Record<string, unknown>, key: string): boolean {
  return typeof o[key] === "string";
}

/** True if `o[key]` is absent (undefined / missing) OR a string. Used for optionals. */
function optString(o: Record<string, unknown>, key: string): boolean {
  return o[key] === undefined || typeof o[key] === "string";
}

/**
 * True if `o.filter` is an acceptable event filter: absent, or an object whose every
 * value is a string/number/boolean (the shape the host turns into a querystring).
 * Defensive — a webview could post a nested/function-valued filter.
 */
function optFilter(o: Record<string, unknown>): boolean {
  const f = o.filter;
  if (f === undefined) {
    return true;
  }
  if (!isObject(f)) {
    return false;
  }
  for (const v of Object.values(f)) {
    const t = typeof v;
    if (t !== "string" && t !== "number" && t !== "boolean") {
      return false;
    }
  }
  return true;
}

/**
 * Defensive runtime guard: is `x` a well-formed `WebviewRequest`? The HOST calls this on
 * every inbound message (postMessage delivers `unknown`) and ignores anything that fails,
 * so a malformed/foreign frame can never drive a fetch or a socket.
 */
export function isWebviewRequest(x: unknown): x is WebviewRequest {
  if (!isObject(x) || !hasString(x, "id")) {
    return false;
  }
  switch (x.kind) {
    case "rest-request":
      return (
        hasString(x, "method") &&
        hasString(x, "path") &&
        optString(x, "body") &&
        optString(x, "contentType")
      );
    case "event-subscribe":
      return optFilter(x);
    case "event-unsubscribe":
      return true;
    default:
      return false;
  }
}

/**
 * Defensive runtime guard: is `x` a well-formed `HostMessage`? The WEBVIEW calls this on
 * every inbound message and ignores anything that fails, so a foreign frame can't spoof
 * a response/event into the pending map.
 */
export function isHostMessage(x: unknown): x is HostMessage {
  if (!isObject(x) || !hasString(x, "id")) {
    return false;
  }
  switch (x.kind) {
    case "rest-response":
      return typeof x.status === "number" && typeof x.ok === "boolean" && hasString(x, "body");
    case "rest-error":
      return hasString(x, "message");
    case "event-open":
    case "event-close":
      return true;
    case "event-message":
      return hasString(x, "data");
    default:
      return false;
  }
}
