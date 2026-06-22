// Conductor Platform — host-side authed client for the FULL-context task diff (Faz-P P2b).
//
// vscode-FREE (mirrors fleetReadClient.ts / controlClient.ts) so vitest drives it with a stubbed
// `fetch`, headlessly. It performs the AUTHED read the editor's full-file native diff needs:
//   GET /projects/{id}/tasks/{task}/diff → the persisted FULL-context patch (ADR-0041)
// wrapped by the gateway's requireAuth (server.go). The editor reconstructs that patch into
// whole-file before/after and opens a native vscode.diff. When it is ABSENT (404 / pre-P2b
// task / no token / a store without diff persistence), the caller falls back to the BOUNDED
// KindDiff patch already in the DiffStore — the P2a hunk view — so the diff still renders.
//
// TOKEN DISCIPLINE (HARD): the bearer token rides ONLY in the outgoing `Authorization` header
// (the single egress). It NEVER appears in a returned value, a thrown error, or a log — this
// module logs nothing and returns a tolerant projection, never the token. Any non-happy outcome
// (no token, non-2xx, network/parse error, empty patch) maps to `undefined`; it never throws.
// A sentinel-token leak-guard test in diffContentClient.test.ts proves it.
import type { TokenProvider } from "./controlClient";

/** The full-context diff the editor reconstructs into a full-file native vscode.diff — a tolerant
 * projection of the gateway taskDiffDTO (server.go). `patch` is the only load-bearing field. */
export interface FullTaskDiff {
  readonly patch: string;
  readonly base: string;
  readonly branch: string;
  readonly truncated: boolean;
}

/**
 * Host-side authed client for a task's full-context diff. Construct one per activation with the
 * configured (normalized) gateway base URL + a TokenProvider reading SecretStorage; tests pass a
 * fake provider + a stubbed `fetch`. The token flows solely into the Authorization header.
 */
export class DiffContentClient {
  readonly #baseUrl: string;
  readonly #tokens: TokenProvider;
  readonly #fetch: typeof fetch;

  constructor(baseUrl: string, tokenProvider: TokenProvider, fetchImpl: typeof fetch = fetch) {
    this.#baseUrl = baseUrl;
    this.#tokens = tokenProvider;
    this.#fetch = fetchImpl;
  }

  /**
   * Fetches the task's FULL-context diff: `GET {base}/projects/{id}/tasks/{task}/diff` (authed).
   * Returns `undefined` on ANY non-happy path — no stored token, non-2xx (404 = none persisted),
   * a network/parse error, or an empty patch — so the caller cleanly falls back to the bounded
   * KindDiff patch. The token rides only in the Authorization header.
   */
  async fetchFullDiff(projectId: string, taskId: string): Promise<FullTaskDiff | undefined> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return undefined;
    }
    const path = `/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(
      taskId,
    )}/diff`;
    let res: Response;
    try {
      res = await this.#fetch(`${this.#baseUrl}${path}`, {
        method: "GET",
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch {
      return undefined;
    }
    if (!res.ok) {
      return undefined;
    }
    let body: unknown;
    try {
      body = (await res.json()) as unknown;
    } catch {
      return undefined;
    }
    return toFullTaskDiff(body);
  }
}

/** Maps an unknown taskDiffDTO payload onto {@link FullTaskDiff}, tolerating the full DTO.
 * Returns `undefined` when there is no usable patch, so the caller falls back to the bounded
 * patch. Never throws. */
function toFullTaskDiff(body: unknown): FullTaskDiff | undefined {
  if (body === null || typeof body !== "object") {
    return undefined;
  }
  const rec = body as Record<string, unknown>;
  const patch = typeof rec["patch"] === "string" ? rec["patch"] : "";
  if (patch === "") {
    return undefined; // nothing to render full-file → the caller uses the bounded patch.
  }
  return {
    patch,
    base: typeof rec["base"] === "string" ? rec["base"] : "",
    branch: typeof rec["branch"] === "string" ? rec["branch"] : "",
    truncated: rec["truncated"] === true,
  };
}
