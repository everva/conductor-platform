// Fork-mode client wiring for the webview cockpit (Faz-4 DALGA 4B, 4B-3).
//
// A tiny, React-free, alias-free helper so it can be unit-tested in vitest without
// pulling in React or the `@cockpit` source tree. It builds the three zero-arg client
// factories the FleetDashboard takes in FORK MODE — each ignores the token (auth is
// host-side, ADR-0027) and constructs a client bound to the postMessage HTTP transport
// (4B-2). The `ApiClient` constructor is injected (not imported) precisely so this module
// stays decoupled from the web/src barrel; main.tsx passes the real `@cockpit` ApiClient.

/** The HTTP seam the fork transport implements (4B-2 webviewTransport). Re-declared
 * structurally (not imported) to keep this module alias-free; the bridge's `http` and the
 * web `HttpTransport` both satisfy it. */
export interface HttpTransportLike {
  send(req: { method: string; path: string; body?: string; contentType?: string }): Promise<{
    status: number;
    ok: boolean;
    body: string;
  }>;
}

/** Constructs a client from an injected transport. Matches `new ApiClient({ transport })`
 * (web/src/api/client.ts) without importing it, so vitest can exercise this with a stub. */
export type TransportClientCtor<C> = new (config: { transport: HttpTransportLike }) => C;

/**
 * Builds the three FORK-MODE client factories FleetDashboard accepts
 * (makeClient/makeControlClient/makeIntakeClient). Each is a zero-arg `() => new
 * ApiClient({ transport })`: the token is intentionally ignored (the host attaches auth
 * when it forwards the REST call over the bridge), so the factories are assignable to the
 * dashboard's `(token: string) => …` props. One transport, three clients — the dashboard
 * builds a fresh client per factory call exactly as in web mode.
 */
export function forkClientFactories<C>(
  ApiClientCtor: TransportClientCtor<C>,
  http: HttpTransportLike,
): {
  makeClient: () => C;
  makeControlClient: () => C;
  makeIntakeClient: () => C;
  makeHistory: () => C;
} {
  const make = (): C => new ApiClientCtor({ transport: http });
  // makeHistory is the SAME bridge client: EventStreamView backfills /events through it so
  // the history fetch rides the postMessage bridge too (the webview CSP blocks a direct
  // fetch). Without it the Events tab would error "Could not reach the gateway".
  return { makeClient: make, makeControlClient: make, makeIntakeClient: make, makeHistory: make };
}
