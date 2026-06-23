// Conductor Platform — host-side authed CREDENTIAL client (Faz L3, ADR-0049).
//
// SCOPE (L3d): the vscode-FREE, host-side HTTP client that uploads the director's portable
// claude OAuth token to the gateway's ENCRYPTED credential store, and removes it (logout
// propagation). The gateway seals it at rest (credstore AES-256-GCM); each agent fetches the
// decrypted token at startup, so no secret-file lives on any performer host and the editor is
// the single login point. Mirrors controlClient.ts: kept free of the `vscode` module so vitest
// drives it with a stubbed `fetch`, headlessly.
//
// GATEWAY FACTS (cmd/conductor-api/agent.go + server.go — do NOT change the gateway):
//   PUT  /agent/credentials/{kind}  body {"token": "..."} → 204; 401 unauthorized; 503 no master
//        key (credential encryption not configured); 501 no credential store; 400 empty token.
//   DELETE /agent/credentials/{kind} → 204 (idempotent); 401; 501.
//   All routes are requireAuth ("Authorization: Bearer <gateway-token>").
//
// TOKEN DISCIPLINE (HARD — TWO secrets): the GATEWAY bearer rides ONLY in the Authorization
// header; the CLAUDE token (the upload payload) rides ONLY in the request body. NEITHER ever
// appears in a returned value (CredentialResult is a closed enum shape), a thrown error, or a
// log — this module logs nothing. A sentinel leak-guard test proves it.

import type { TokenProvider } from "./controlClient";

/**
 * Closed result of a credential call. Success carries nothing (the gateway returns no body and
 * we never echo a token). Failure is a closed enum so the host picks a token-free message;
 * `status` is the HTTP status (0 for the no-token short-circuit / a thrown network error).
 * Neither the gateway bearer nor the uploaded claude token is ever part of this value.
 */
export type CredentialResult =
  | { ok: true }
  | {
      ok: false;
      reason: "not-connected" | "unauthorized" | "not-configured" | "unreachable";
      status: number;
    };

/** Outcome of a presence probe ({@link CredentialClient.status}) — derived from the HTTP status
 * ONLY; the token body is never read. Drives the diagnostics panel's "claude on gateway" row. */
export type CredentialStatus =
  | "present"
  | "absent"
  | "unauthorized"
  | "not-configured"
  | "not-connected"
  | "unreachable";

/**
 * Host-side authed credential client. Construct one per activation with the normalized gateway
 * base URL + a TokenProvider reading the GATEWAY bearer from SecretStorage; tests pass a fake
 * provider + a stubbed `fetch`. The claude token to upload is passed per call (the command
 * reads it from SecretStorage host-side). Neither secret leaves the header/body.
 */
export class CredentialClient {
  readonly #baseUrl: string;
  readonly #tokens: TokenProvider;
  readonly #fetch: typeof fetch;

  constructor(baseUrl: string, tokenProvider: TokenProvider, fetchImpl: typeof fetch = fetch) {
    this.#baseUrl = baseUrl;
    this.#tokens = tokenProvider;
    this.#fetch = fetchImpl;
  }

  /**
   * Uploads (seals at the gateway) the claude OAuth token under `kind`:
   * `PUT {base}/agent/credentials/{kind}` with `{ token }`. The claude token is sent ONLY in
   * the body; the gateway bearer ONLY in the header.
   */
  upload(kind: string, token: string): Promise<CredentialResult> {
    return this.#send("PUT", kind, { token });
  }

  /** Removes the stored credential (logout propagation): `DELETE {base}/agent/credentials/{kind}`. */
  remove(kind: string): Promise<CredentialResult> {
    return this.#send("DELETE", kind);
  }

  /**
   * Presence probe for the diagnostics panel: `GET {base}/agent/credentials/{kind}`, mapping ONLY
   * the HTTP status → a CredentialStatus. TOKEN DISCIPLINE: the gateway returns the decrypted token
   * in the body, but this NEVER reads it (no `.json()`/`.text()`) — only `res.status` — so the
   * token never enters the editor here. Used to answer "is the claude login on the gateway?".
   */
  async status(kind: string): Promise<CredentialStatus> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return "not-connected";
    }
    const url = `${this.#baseUrl}/agent/credentials/${encodeURIComponent(kind)}`;
    let res: Response;
    try {
      res = await this.#fetch(url, { method: "GET", headers: { Authorization: `Bearer ${token}` } });
    } catch {
      return "unreachable";
    }
    // Body deliberately ignored — we read ONLY the status (the token must not enter the editor).
    if (res.ok) {
      return "present";
    }
    if (res.status === 404) {
      return "absent";
    }
    if (res.status === 401) {
      return "unauthorized";
    }
    if (res.status === 501 || res.status === 503) {
      return "not-configured";
    }
    return "unreachable";
  }

  /**
   * Performs an authed request and maps the outcome onto a CredentialResult:
   *   no gateway token → not-connected (status 0; no request sent)
   *   2xx              → ok
   *   401              → unauthorized
   *   501 / 503        → not-configured (no credential store / no master key on the gateway)
   *   other / throw    → unreachable
   * The gateway bearer goes ONLY in the Authorization header; the optional body (the claude
   * token) is JSON and never logged. Nothing here returns or logs either secret.
   */
  async #send(method: "PUT" | "DELETE", kind: string, body?: unknown): Promise<CredentialResult> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return { ok: false, reason: "not-connected", status: 0 };
    }
    const url = `${this.#baseUrl}/agent/credentials/${encodeURIComponent(kind)}`;
    const init: RequestInit =
      body === undefined
        ? { method, headers: { Authorization: `Bearer ${token}` } }
        : {
            method,
            headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
            body: JSON.stringify(body),
          };
    let res: Response;
    try {
      res = await this.#fetch(url, init);
    } catch {
      return { ok: false, reason: "unreachable", status: 0 };
    }
    if (res.ok) {
      return { ok: true };
    }
    if (res.status === 401) {
      return { ok: false, reason: "unauthorized", status: 401 };
    }
    if (res.status === 501 || res.status === 503) {
      return { ok: false, reason: "not-configured", status: res.status };
    }
    return { ok: false, reason: "unreachable", status: res.status };
  }
}
