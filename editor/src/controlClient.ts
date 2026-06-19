// Conductor Platform — host-side authed CONTROL client (Faz-4 DALGA 4C, 4C-2).
//
// SCOPE (4C-2): the vscode-FREE, host-side HTTP client the inline control commands
// (`conductor.pause`/`resume`/`abort`/`approve`) drive. It performs the authed control
// POSTs against the conductor-api gateway (cmd/conductor-api/control.go) and lists
// projects for the command's quick-pick. Keeping it free of the `vscode` module is
// deliberate (mirrors gateway.ts): vitest drives it directly with a stubbed `fetch`,
// headlessly, no electron. This REUSES the existing control API — it does NOT depend on
// the 4C-0 diff source (see ADR-0030 "bağımsız işler").
//
// GATEWAY FACTS (verified from cmd/conductor-api/control.go + server.go — do NOT change
// the gateway): every control route is wrapped by requireAuth ("Authorization: Bearer
// <token>" or 401). `POST /projects/{id}/pause` and `/resume` are idempotent → 200.
// `POST /projects/{id}/abort` → 200, or 409 when no task is running. `POST
// /projects/{id}/approve` takes OPTIONAL JSON {"task_id":"…"} → 200, or 409 when zero or
// multiple tasks await approval. `GET /projects` lists projects (each has an `id`).
//
// TOKEN DISCIPLINE (HARD): the bearer token rides ONLY in the outgoing `Authorization`
// header. It NEVER appears in any returned value (ControlResult is a closed shape with an
// enum `reason`), thrown error, or log line — this module logs nothing. A sentinel-token
// leak-guard test in controlClient.test.ts proves it.

/** Reads the bearer token from its at-rest home (SecretStorage) per request. Mirrors the
 * bridge/gateway TokenProvider seam: injected so the token source stays out of this file
 * and tests pass a fake. `undefined` means "not connected" (no stored token). */
export interface TokenProvider {
  getToken(): Promise<string | undefined>;
}

/**
 * Closed result of a control call. On success `body` carries the parsed JSON the gateway
 * returned (best-effort; `undefined` if there was no/invalid JSON) — handy for callers but
 * it NEVER contains the token. On failure `reason` is a closed enum so the host picks a
 * token-free user-facing message, and `status` is the HTTP status (0 for a thrown/network
 * error, and for the no-token short-circuit). The raw token is never part of this value.
 */
export type ControlResult =
  | { ok: true; body: unknown }
  | {
      ok: false;
      reason: "not-connected" | "conflict" | "unauthorized" | "unreachable";
      status: number;
    };

/** The minimal project shape the quick-pick needs: just the `id`. The gateway returns a
 * far richer project DTO; we deliberately read ONLY `id` (tolerating the full shape). */
export interface ProjectRef {
  readonly id: string;
}

/**
 * Host-side authed control client. Construct one per activation with the configured
 * (normalized) gateway base URL + a TokenProvider reading SecretStorage; tests pass a fake
 * provider + a stubbed `fetch`. `fetchImpl` is injectable ONLY for tests (defaults to the
 * global `fetch`, present on Node 20 / the CI). The token flows solely into the
 * Authorization header of each request — never to a returned value, a log, or a webview.
 */
export class ControlClient {
  readonly #baseUrl: string;
  readonly #tokens: TokenProvider;
  readonly #fetch: typeof fetch;

  constructor(baseUrl: string, tokenProvider: TokenProvider, fetchImpl: typeof fetch = fetch) {
    this.#baseUrl = baseUrl;
    this.#tokens = tokenProvider;
    this.#fetch = fetchImpl;
  }

  /**
   * Lists projects for the quick-pick: `GET {base}/projects` (authed). Returns each
   * project's `id` only (tolerating the full project DTO). Maps no-token → "not-connected",
   * 401 → "unauthorized", and any other non-2xx / thrown error → "unreachable" — surfaced
   * as a thrown ControlListError so the caller (runControl) can branch on `reason` while
   * the happy path stays a plain array. The token rides only in the header.
   */
  async listProjects(): Promise<ProjectRef[]> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      throw new ControlListError("not-connected", 0);
    }
    const url = `${this.#baseUrl}/projects`;
    let res: Response;
    try {
      res = await this.#fetch(url, { method: "GET", headers: authHeader(token) });
    } catch {
      throw new ControlListError("unreachable", 0);
    }
    if (res.status === 401) {
      throw new ControlListError("unauthorized", 401);
    }
    if (!res.ok) {
      throw new ControlListError("unreachable", res.status);
    }
    const body = await readJsonSafe(res);
    return toProjectRefs(body);
  }

  /** `POST {base}/projects/{id}/pause` (authed, idempotent). */
  pause(projectId: string): Promise<ControlResult> {
    return this.#post(projectId, "pause");
  }

  /** `POST {base}/projects/{id}/resume` (authed, idempotent). */
  resume(projectId: string): Promise<ControlResult> {
    return this.#post(projectId, "resume");
  }

  /** `POST {base}/projects/{id}/abort` (authed). 409 → "conflict" (no task running). */
  abort(projectId: string): Promise<ControlResult> {
    return this.#post(projectId, "abort");
  }

  /**
   * `POST {base}/projects/{id}/approve` (authed). Sends a JSON body `{ task_id }` ONLY when
   * `taskId` is provided (else no body — the gateway auto-resolves the unique awaiting
   * task). 409 → "conflict" (zero or multiple tasks awaiting approval).
   */
  approve(projectId: string, taskId?: string): Promise<ControlResult> {
    if (taskId !== undefined) {
      return this.#post(projectId, "approve", { task_id: taskId });
    }
    return this.#post(projectId, "approve");
  }

  /**
   * Performs an authed control POST and maps the outcome onto a ControlResult:
   *   no token        → not-connected (status 0; no request sent)
   *   2xx             → ok (with best-effort parsed body)
   *   401             → unauthorized
   *   409             → conflict
   *   other / throw   → unreachable
   * `body` is sent as JSON only when provided (so optional-body endpoints get no body).
   * The token is placed ONLY in the Authorization header; it never leaves this method.
   */
  async #post(projectId: string, action: ControlAction, body?: unknown): Promise<ControlResult> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return { ok: false, reason: "not-connected", status: 0 };
    }
    // Project ids come from the gateway's own listProjects, but encode anyway so the path
    // is well-formed regardless of the id's characters.
    const url = `${this.#baseUrl}/projects/${encodeURIComponent(projectId)}/${action}`;
    const init: RequestInit =
      body === undefined
        ? { method: "POST", headers: authHeader(token) }
        : {
            method: "POST",
            headers: { ...authHeader(token), "Content-Type": "application/json" },
            body: JSON.stringify(body),
          };
    let res: Response;
    try {
      res = await this.#fetch(url, init);
    } catch {
      return { ok: false, reason: "unreachable", status: 0 };
    }
    if (res.ok) {
      return { ok: true, body: await readJsonSafe(res) };
    }
    if (res.status === 401) {
      return { ok: false, reason: "unauthorized", status: 401 };
    }
    if (res.status === 409) {
      return { ok: false, reason: "conflict", status: 409 };
    }
    return { ok: false, reason: "unreachable", status: res.status };
  }
}

/** The four control actions, mirrored 1:1 onto the gateway's `/projects/{id}/<action>`
 * POST routes. Shared with extension.ts's `runControl` action union. */
export type ControlAction = "pause" | "resume" | "abort" | "approve";

/**
 * Thrown by `listProjects` on a non-happy outcome so the caller can branch on the same
 * closed `reason` enum the control POSTs use (minus "conflict", which a list can't return),
 * with the HTTP `status` (0 for no-token / network error). It carries NO token — only the
 * enum + status — and a fixed message so even a logged error can't leak the token.
 */
export class ControlListError extends Error {
  readonly reason: "not-connected" | "unauthorized" | "unreachable";
  readonly status: number;

  constructor(reason: "not-connected" | "unauthorized" | "unreachable", status: number) {
    super(`listProjects failed: ${reason}`);
    this.name = "ControlListError";
    this.reason = reason;
    this.status = status;
  }
}

/** Builds the single-header Authorization object. Isolated so the token's ONLY egress is
 * this one place (every request path routes through it). Never logged. */
function authHeader(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

/** Best-effort JSON parse of a response body. Returns `undefined` rather than throwing on
 * an empty/invalid body, so a 200 with no JSON still maps to `ok` (the gateway always
 * returns JSON today, but callers must not depend on it to decide success). */
async function readJsonSafe(res: Response): Promise<unknown> {
  try {
    return (await res.json()) as unknown;
  } catch {
    return undefined;
  }
}

/** Maps an unknown gateway-projects payload onto `{ id }[]`, tolerating the full project
 * shape: keeps only array entries that are objects carrying a string `id`. Anything else
 * (non-array, null entries, missing/non-string id) is dropped — never throws. */
function toProjectRefs(body: unknown): ProjectRef[] {
  if (!Array.isArray(body)) {
    return [];
  }
  const refs: ProjectRef[] = [];
  for (const entry of body) {
    if (entry !== null && typeof entry === "object" && "id" in entry) {
      const id = (entry as { id: unknown }).id;
      if (typeof id === "string") {
        refs.push({ id });
      }
    }
  }
  return refs;
}
