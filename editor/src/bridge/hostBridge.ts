// Conductor Platform — host-side postMessage bridge (Faz-4 DALGA 4B, 4B-2).
//
// SCOPE (4B-2): the EXTENSION-HOST half of the fork transport seam. The webview asks
// (over postMessage) for a REST call or an event stream; THIS class attaches auth and
// performs the real I/O against the gateway, then posts the result back. It owns the
// trust boundary: the token lives host-side (read from SecretStorage via a
// TokenProvider) and is placed ONLY into the outgoing fetch Authorization header and the
// gateway WS `?token=` — it is NEVER included in any message posted to the webview.
//
// Everything is behind narrow injectable seams (WebviewLike / TokenProvider /
// WsConnector / fetchImpl) so vitest drives the whole class with fakes: no real network,
// no `vscode`, no `ws`. The real adapters are wired in extension.ts (WebviewLike =
// webviewView.webview, TokenProvider = context.secrets, WsConnector = wsConnector.ts).
//
// SECURITY CONTRACT (HARD — enforced + tested):
//   1. PATH GUARD (SSRF): the webview supplies only method+path; the host joins
//      baseUrl + path. A `path` that does not start with EXACTLY one "/" is rejected
//      with a rest-error and NO fetch — so a compromised webview can't redirect the
//      AUTHED request to another origin ("//evil", "http://evil", "no-slash").
//   2. TOKEN ISOLATION: the token rides only in the fetch Authorization header + the WS
//      URL; it appears in no posted HostMessage and no log. On a thrown fetch we post a
//      GENERIC message (never the raw error, which could echo a URL carrying creds).

import type { HostMessage, WebviewRequest } from "./protocol";
import { isWebviewRequest } from "./protocol";

/**
 * The slice of `vscode.WebviewView["webview"]` the bridge needs: post a message to the
 * webview and subscribe to messages from it. `webviewView.webview` satisfies this
 * structurally (its postMessage/onDidReceiveMessage match); tests pass a fake that
 * records posts and lets the test fire received messages.
 */
export interface WebviewLike {
  postMessage(message: unknown): Thenable<boolean> | void;
  onDidReceiveMessage(listener: (message: unknown) => void): { dispose(): void };
}

/**
 * Reads the bearer token from its at-rest home (SecretStorage). Returns `undefined` when
 * not connected. The host calls this PER request/subscribe (SecretStorage is the source
 * of truth — the bridge never caches the token on the instance). A
 * `context.secrets.get(GATEWAY_TOKEN_KEY)` thunk satisfies it; tests pass a fake.
 */
export interface TokenProvider {
  getToken(): Promise<string | undefined>;
}

/** A live WS connection the bridge can close. */
export interface WsHandle {
  close(): void;
}

/**
 * Opens a WebSocket to `url` and drives the given callbacks. The real impl
 * (wsConnector.ts) is a thin `ws` adapter; tests pass a fake that records the URL and
 * lets the test fire onOpen/onMessage/onClose. The URL already carries the token query.
 */
export interface WsConnector {
  open(
    url: string,
    h: { onOpen(): void; onMessage(data: string): void; onClose(): void },
  ): WsHandle;
}

/**
 * Dependencies for a HostBridge. `baseUrl`/`wsBaseUrl` are HOST-controlled (already
 * normalized; `wsBaseUrl` = deriveWsUrl(baseUrl)) so the webview's path/filter only ever
 * extends a base the host chose. `fetchImpl` defaults to the global fetch; it is
 * injectable so tests run offline.
 */
export interface HostBridgeDeps {
  webview: WebviewLike;
  baseUrl: string;
  wsBaseUrl: string;
  tokenProvider: TokenProvider;
  wsConnector: WsConnector;
  fetchImpl?: typeof fetch;
}

/**
 * Builds the gateway `/events`-style querystring from a filter, mirroring the web
 * `eventQueryString` (web/src/api/client.ts) so the fork WS URL matches the web one:
 * only known keys are emitted, empty values are dropped, and `intervention` emits "1"
 * only when truthy (the gateway's isTruthy parsing). The webview-supplied filter is
 * untrusted but already shape-checked by `isWebviewRequest`; unknown keys are ignored.
 */
function filterQueryString(filter: Record<string, string | number | boolean> | undefined): string {
  const q = new URLSearchParams();
  if (filter) {
    const set = (key: string): void => {
      const v = filter[key];
      if (v !== undefined && v !== "" && v !== false) {
        q.set(key, key === "intervention" ? "1" : String(v));
      }
    };
    set("project");
    set("task");
    set("phase");
    set("kind");
    set("intervention");
    set("since");
    if (filter.limit !== undefined && filter.limit !== "") {
      q.set("limit", String(filter.limit));
    }
  }
  const s = q.toString();
  return s.length > 0 ? `?${s}` : "";
}

/**
 * Validates a webview-supplied REST path. To prevent the webview from retargeting the
 * AUTHED request to another origin, the path MUST start with exactly one "/" and may not
 * be protocol-relative ("//host"). Absolute URLs ("http://", "https://") fail the
 * leading-slash check; "//evil" is rejected by the second-char guard. Pure + testable.
 */
function isSafePath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//");
}

/**
 * The host side of the bridge. Construct one per resolved webview (extension.ts), call
 * `attach()` to start listening, and `dispose()` on teardown. The token is read per
 * request from the TokenProvider and used only host-side.
 */
export class HostBridge {
  readonly #webview: WebviewLike;
  readonly #baseUrl: string;
  readonly #wsBaseUrl: string;
  readonly #tokenProvider: TokenProvider;
  readonly #wsConnector: WsConnector;
  readonly #fetch: typeof fetch;

  /** Open WS handles keyed by the webview's subscription id (for unsubscribe/dispose). */
  readonly #subscriptions = new Map<string, WsHandle>();

  /** The onDidReceiveMessage disposable, stored by attach() and cleared by dispose(). */
  #listener: { dispose(): void } | undefined;

  constructor(deps: HostBridgeDeps) {
    this.#webview = deps.webview;
    this.#baseUrl = deps.baseUrl;
    this.#wsBaseUrl = deps.wsBaseUrl;
    this.#tokenProvider = deps.tokenProvider;
    this.#wsConnector = deps.wsConnector;
    this.#fetch = deps.fetchImpl ?? fetch;
  }

  /** Registers the inbound-message listener. Idempotent-safe per instance (call once). */
  attach(): void {
    this.#listener = this.#webview.onDidReceiveMessage((message) => {
      // postMessage delivers untrusted `unknown`; ignore anything that isn't a
      // well-formed WebviewRequest so a foreign/malformed frame can't drive I/O.
      if (!isWebviewRequest(message)) {
        return;
      }
      void this.#handle(message);
    });
  }

  /** Closes every open WS handle and disposes the message listener. Safe to call once. */
  dispose(): void {
    for (const handle of this.#subscriptions.values()) {
      handle.close();
    }
    this.#subscriptions.clear();
    this.#listener?.dispose();
    this.#listener = undefined;
  }

  /** Routes a validated request to its handler. */
  async #handle(req: WebviewRequest): Promise<void> {
    switch (req.kind) {
      case "rest-request":
        await this.#handleRest(req);
        return;
      case "event-subscribe":
        await this.#handleSubscribe(req);
        return;
      case "event-unsubscribe":
        this.#handleUnsubscribe(req);
        return;
    }
  }

  /**
   * Performs an authed REST call on behalf of the webview. Order: validate the path
   * (SSRF guard) → read the token → fetch baseUrl+path with the Authorization header →
   * post the status/ok/body. Every failure posts a token-free rest-error and no body
   * leaks the token.
   */
  async #handleRest(
    req: Extract<WebviewRequest, { kind: "rest-request" }>,
  ): Promise<void> {
    if (!isSafePath(req.path)) {
      // Reject WITHOUT fetching: the webview must not retarget the authed request.
      this.#post({ kind: "rest-error", id: req.id, message: "invalid path" });
      return;
    }

    const token = await this.#tokenProvider.getToken();
    if (token === undefined) {
      this.#post({ kind: "rest-error", id: req.id, message: "not connected" });
      return;
    }

    // The token rides ONLY here, in the Authorization header. Content-Type is added only
    // when a body exists (exactOptionalPropertyTypes: build the headers/init by spread so
    // no optional key is ever assigned an explicit undefined).
    const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
    if (req.body !== undefined) {
      headers["Content-Type"] = req.contentType ?? "text/plain; charset=utf-8";
    }
    const init: RequestInit = {
      method: req.method,
      headers,
      ...(req.body !== undefined ? { body: req.body } : {}),
    };

    try {
      const res = await this.#fetch(`${this.#baseUrl}${req.path}`, init);
      const body = await res.text();
      this.#post({ kind: "rest-response", id: req.id, status: res.status, ok: res.ok, body });
    } catch {
      // GENERIC message only: the raw error could echo a URL carrying the token query.
      this.#post({ kind: "rest-error", id: req.id, message: "gateway request failed" });
    }
  }

  /**
   * Opens a gateway WS stream on behalf of the webview. Builds the URL =
   * wsBaseUrl + "/ws" + <filter querystring> + (&|?) + "token=<encoded>" (mirroring the
   * web wsUrl shape), opens it via the injected connector, and bridges open/message/close
   * back to the webview keyed by `id`. If there is no token, close immediately. The token
   * rides ONLY in the URL query — never in a posted message.
   */
  async #handleSubscribe(
    req: Extract<WebviewRequest, { kind: "event-subscribe" }>,
  ): Promise<void> {
    const token = await this.#tokenProvider.getToken();
    if (token === undefined) {
      this.#post({ kind: "event-close", id: req.id });
      return;
    }

    const filterQs = filterQueryString(req.filter);
    const sep = filterQs.length > 0 ? "&" : "?";
    const url = `${this.#wsBaseUrl}/ws${filterQs}${sep}token=${encodeURIComponent(token)}`;

    const handle = this.#wsConnector.open(url, {
      onOpen: () => this.#post({ kind: "event-open", id: req.id }),
      onMessage: (data) => this.#post({ kind: "event-message", id: req.id, data }),
      onClose: () => {
        // Forget the handle once the socket closes (server-side / error close) so a
        // later unsubscribe/dispose is a no-op for it.
        this.#subscriptions.delete(req.id);
        this.#post({ kind: "event-close", id: req.id });
      },
    });
    this.#subscriptions.set(req.id, handle);
  }

  /** Closes + forgets the WS handle for an id (no-op if already gone). */
  #handleUnsubscribe(req: Extract<WebviewRequest, { kind: "event-unsubscribe" }>): void {
    const handle = this.#subscriptions.get(req.id);
    if (handle) {
      this.#subscriptions.delete(req.id);
      handle.close();
    }
  }

  /** Posts a host→webview message. Centralized so it is the single egress to the webview;
   * by construction every `HostMessage` is token-free (see protocol.ts). */
  #post(message: HostMessage): void {
    void this.#webview.postMessage(message);
  }
}
