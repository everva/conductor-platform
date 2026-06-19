// Conductor Platform — gateway HTTP helpers (Faz-4 DALGA 4B, 4B-1).
//
// SCOPE (4B-1): the host-side, vscode-FREE HTTP helpers the ConnectionManager
// (connection.ts) uses to talk to the conductor-api gateway (cmd/conductor-api).
// Keeping these free of the `vscode` module is deliberate: vitest can drive them
// directly with a stubbed global `fetch` (Node 20 ships a global fetch; the CI runs
// node 20). The host↔webview postMessage bridge and the WS event client land in
// 4B-2 — `deriveWsUrl` is included now so 4B-2 reuses one canonical derivation.
//
// GATEWAY FACTS (verified from cmd/conductor-api/server.go — do NOT change the
// gateway): `GET /healthz` and `GET /readyz` are UNAUTHENTICATED (k8s liveness /
// readiness); `/readyz` → 200 when the store read succeeds, 503 otherwise. Every
// data route is wrapped by requireAuth: "Authorization: Bearer <token>" or 401.
// `GET /status` is the lightest authed read, so it doubles as a token-validity probe.
//
// TOKEN DISCIPLINE (HARD): `validateToken` puts the token ONLY into the outgoing
// `Authorization` header. The token NEVER appears in any returned value, thrown
// error, or log line here — the result is a closed enum ("valid"/"unauthorized"/
// "unreachable"). This module logs nothing.

/** Result of a token-validity probe. Deliberately a closed enum so the raw token
 * can never ride out in a returned value (see TOKEN DISCIPLINE above). */
export type TokenCheck = "valid" | "unauthorized" | "unreachable";

/** SecretStorage key the host stores the gateway bearer token under. Single source
 * of truth shared with connection.ts so connect/disconnect/restore agree. */
export const GATEWAY_TOKEN_KEY = "conductor.gatewayToken";

/**
 * Trims surrounding whitespace and strips a single trailing slash so callers can
 * safely build `${base}/readyz` etc. without doubling the separator. Pure.
 */
export function normalizeBaseUrl(url: string): string {
  return url.trim().replace(/\/+$/, "");
}

/**
 * Derives the WebSocket base URL from an http(s) base: http→ws, https→wss. Used by
 * the 4B-2 event client; included + tested now so there is one canonical derivation.
 * Only the scheme is rewritten; the (normalized) authority/path is preserved.
 */
export function deriveWsUrl(httpUrl: string): string {
  const base = normalizeBaseUrl(httpUrl);
  if (base.startsWith("https://")) {
    return `wss://${base.slice("https://".length)}`;
  }
  if (base.startsWith("http://")) {
    return `ws://${base.slice("http://".length)}`;
  }
  return base;
}

/**
 * Probes `GET {base}/readyz` (UNAUTHENTICATED). Answers "is the gateway up & ready?"
 * 200 → { ok: true, status: 200 }; 503 → { ok: false, status: 503 }. A network
 * failure (or any thrown error) is reported cleanly as { ok: false, status: 0 } —
 * this NEVER throws to the caller, so the connect flow can map it to a tidy reason.
 */
export async function pingReadyz(
  baseUrl: string,
  signal?: AbortSignal,
): Promise<{ ok: boolean; status: number }> {
  const url = `${normalizeBaseUrl(baseUrl)}/readyz`;
  try {
    const res = await fetch(url, signal ? { method: "GET", signal } : { method: "GET" });
    return { ok: res.ok, status: res.status };
  } catch {
    // Network error / DNS / abort: surface as not-ready with status 0. We deliberately
    // do not include the error (it could echo the URL but never a token) — the caller
    // only needs the up/down signal.
    return { ok: false, status: 0 };
  }
}

/**
 * Validates a bearer token against `GET {base}/status` (the lightest authed read).
 * 200 → "valid"; 401 → "unauthorized"; any other status or a thrown/network error →
 * "unreachable". CRITICAL: `token` is placed ONLY in the Authorization header; it is
 * never returned, thrown, or logged. The returned value is a closed enum.
 */
export async function validateToken(
  baseUrl: string,
  token: string,
  signal?: AbortSignal,
): Promise<TokenCheck> {
  const url = `${normalizeBaseUrl(baseUrl)}/status`;
  const headers = { Authorization: `Bearer ${token}` };
  try {
    const res = await fetch(url, signal ? { method: "GET", headers, signal } : { method: "GET", headers });
    if (res.status === 200) {
      return "valid";
    }
    if (res.status === 401) {
      return "unauthorized";
    }
    return "unreachable";
  } catch {
    return "unreachable";
  }
}

/**
 * The narrow gateway-probe surface the ConnectionManager depends on. `gateway.ts`'s
 * free functions, bound to a base url, satisfy it (see `makeGatewayProbe`); tests
 * pass a fake. Keeping it an interface (not the module) keeps connection.ts testable.
 */
export interface GatewayProbe {
  pingReadyz(signal?: AbortSignal): Promise<{ ok: boolean; status: number }>;
  validateToken(token: string, signal?: AbortSignal): Promise<TokenCheck>;
}

/**
 * Binds the free gateway functions to a base url, producing a `GatewayProbe`. The
 * host wires this from `conductor.gatewayUrl`; the ConnectionManager stays agnostic
 * of how the probe is built (real fetch here, fake map in tests).
 */
export function makeGatewayProbe(baseUrl: string): GatewayProbe {
  const base = normalizeBaseUrl(baseUrl);
  return {
    pingReadyz: (signal) => pingReadyz(base, signal),
    validateToken: (token, signal) => validateToken(base, token, signal),
  };
}
