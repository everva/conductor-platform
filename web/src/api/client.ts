// Typed REST client over the conductor-api gateway (ADR-0025/0026). A thin
// wrapper around fetch: it attaches "Authorization: Bearer <token>" to every
// request, maps non-2xx responses to a typed ApiError carrying the status and the
// gateway's {error} body, and exposes one method per gateway endpoint with the
// exact request/response shapes from cmd/conductor-api.
//
// The token is held in memory only and is NEVER logged or serialized into error
// messages. The WebSocket surface lives in useEventStream.ts (browsers cannot set
// an Authorization header, so /ws takes ?token=).
import type {
  DistillResult,
  Event,
  EventQuery,
  Host,
  IntakeResult,
  IntakeSessionDetail,
  IntakeSessionMessage,
  IntakeSessionSummary,
  Project,
  Scenario,
  StatusSummary,
  Task,
} from "./types.ts";

// ApiError is the single typed failure for every non-2xx gateway response. It
// carries the HTTP status and the gateway's secret-free {error} message (or a
// fallback). It deliberately does NOT carry the request or any auth material.
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// DistillNoScenariosError is the TYPED "nothing distillable" signal: the gateway
// returned 422 because the distiller honestly produced no approvable scenario
// (intake.ErrNoScenarios) or a block that failed validation
// (intake.ErrMalformedScenarios). This is the never-fabricate contract surfaced to
// the UI — it is guidance ("add more detail"), NOT a transport error. It carries the
// gateway's secret-free reason and never the conversation or the token.
export class DistillNoScenariosError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "DistillNoScenariosError";
  }
}

// ClientConfig configures the REST base URL and bearer token. baseUrl defaults to
// VITE_API_BASE (same-origin "" when unset); token is required for authed calls.
export interface ClientConfig {
  baseUrl?: string;
  token: string;
}

// HttpResponse is the raw HTTP result the transport returns: the body is already
// read as text (the ApiClient owns JSON parsing / error mapping).
export interface HttpResponse {
  status: number;
  ok: boolean;
  body: string;
}

// HttpRequest is a single request the transport must perform. path is
// gateway-relative (e.g. "/projects"); body (if any) is ALREADY serialized (a JSON
// string or raw text), and contentType is the header to send when a body exists.
export interface HttpRequest {
  method: string;
  path: string;
  body?: string;
  contentType?: string;
}

// HttpTransport is the REST seam. Auth is the transport's responsibility (web:
// FetchTransport attaches Authorization from its own token; fork: the extension
// host attaches auth when forwarding), so ApiClient stays auth-agnostic and the
// fork webview never holds the token (ADR-0027).
export interface HttpTransport {
  send(req: HttpRequest): Promise<HttpResponse>;
  // sendStream is the OPTIONAL Server-Sent-Events seam (Q3c.4): it performs an SSE
  // request, invoking onEvent(event, data) for each parsed frame, and resolves with
  // the status + (for a non-stream/error response) the full body for normal error
  // mapping. A transport that cannot stream (the fork postMessage bridge — the webview
  // never holds the token, so it cannot fetch directly) OMITS this; callers then fall
  // back to send(). Web's FetchTransport implements it via the response body reader.
  sendStream?(
    req: HttpRequest,
    onEvent: (event: string, data: string) => void,
  ): Promise<HttpResponse>;
}

// defaultBaseUrl reads the build-time API base, falling back to same-origin "".
export function defaultBaseUrl(): string {
  return import.meta.env.VITE_API_BASE ?? "";
}

// FetchTransport is the WEB HttpTransport: it owns the bearer token and attaches
// "Authorization: Bearer <token>" (plus Content-Type when a body is present) to a
// global fetch against `${baseUrl}${path}`. baseUrl must already be
// trailing-slash-stripped. The token is held in memory only and is NEVER logged.
export class FetchTransport implements HttpTransport {
  private readonly baseUrl: string;
  private readonly token: string;

  constructor(baseUrl: string, token: string) {
    this.baseUrl = baseUrl;
    this.token = token;
  }

  async send(req: HttpRequest): Promise<HttpResponse> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
    };
    const init: RequestInit = { method: req.method, headers };
    if (req.body !== undefined) {
      headers["Content-Type"] = req.contentType ?? "text/plain; charset=utf-8";
      init.body = req.body;
    }

    const res = await fetch(`${this.baseUrl}${req.path}`, init);
    return { status: res.status, ok: res.ok, body: await res.text() };
  }

  // sendStream performs an SSE request and dispatches each parsed frame to onEvent.
  // A non-2xx or non-event-stream response (a pre-stream error) is returned with its
  // full body for the caller's normal error mapping — no frames are dispatched.
  async sendStream(
    req: HttpRequest,
    onEvent: (event: string, data: string) => void,
  ): Promise<HttpResponse> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
    };
    const init: RequestInit = { method: req.method, headers };
    if (req.body !== undefined) {
      headers["Content-Type"] = req.contentType ?? "text/plain; charset=utf-8";
      init.body = req.body;
    }

    const res = await fetch(`${this.baseUrl}${req.path}`, init);
    const ct = res.headers.get("Content-Type") ?? "";
    if (!res.ok || !ct.includes("text/event-stream") || res.body === null) {
      return { status: res.status, ok: res.ok, body: await res.text() };
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buf += decoder.decode(value, { stream: true });
      let sep = buf.indexOf("\n\n");
      while (sep >= 0) {
        const frame = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        let ev = "";
        let data = "";
        for (const line of frame.split("\n")) {
          if (line.startsWith("event: ")) {
            ev = line.slice(7);
          } else if (line.startsWith("data: ")) {
            data = line.slice(6);
          }
        }
        if (ev !== "") {
          onEvent(ev, data);
        }
        sep = buf.indexOf("\n\n");
      }
    }
    return { status: res.status, ok: true, body: "" };
  }
}

// utf8ToBase64 encodes a string as base64 over its UTF-8 bytes (the gateway's holdout PUT expects
// base64 file contents). Chunked so a large file can't overflow the String.fromCharCode call.
function utf8ToBase64(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let bin = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    bin += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(bin);
}

export class ApiClient {
  private readonly transport: HttpTransport;

  // Two accepted forms (additive — ClientConfig behavior is unchanged):
  //   new ApiClient({ token })            → builds a web FetchTransport (holds token)
  //   new ApiClient({ transport })        → injected transport (token-agnostic path)
  constructor(config: ClientConfig | { transport: HttpTransport }) {
    if ("transport" in config) {
      // Injected transport (e.g. the fork postMessage bridge): the ApiClient stays
      // auth-agnostic and never sees a token. The transport owns auth.
      this.transport = config.transport;
    } else {
      // Web default: strip a trailing slash so path joins are unambiguous, then
      // hand the base + token to the FetchTransport which owns the auth header.
      this.transport = new FetchTransport(
        (config.baseUrl ?? defaultBaseUrl()).replace(/\/$/, ""),
        config.token,
      );
    }
  }

  // --- read endpoints (server.go) ---

  listProjects(): Promise<Project[]> {
    return this.request<Project[]>("GET", "/projects");
  }

  listTasks(projectId: string): Promise<Task[]> {
    return this.request<Task[]>(
      "GET",
      `/projects/${encodeURIComponent(projectId)}/tasks`,
    );
  }

  listScenarios(projectId: string): Promise<Scenario[]> {
    return this.request<Scenario[]>(
      "GET",
      `/projects/${encodeURIComponent(projectId)}/scenarios`,
    );
  }

  listHosts(): Promise<Host[]> {
    return this.request<Host[]>("GET", "/hosts");
  }

  status(): Promise<StatusSummary> {
    return this.request<StatusSummary>("GET", "/status");
  }

  listEvents(params: EventQuery = {}): Promise<Event[]> {
    return this.request<Event[]>("GET", `/events${eventQueryString(params)}`);
  }

  // --- control endpoints (control.go) ---

  onboard(repo: string, baseBranch?: string): Promise<Project> {
    const body: { repo: string; base_branch?: string } = { repo };
    if (baseBranch !== undefined) {
      body.base_branch = baseBranch;
    }
    return this.request<Project>("POST", "/projects", body);
  }

  // distill POSTs a free-text conversation as JSON {"conversation":...} and returns
  // the PROPOSED scenarios + an intake-ready YAML string (distillResultDTO). It
  // persists NOTHING — this is the assisted-drafting step (ADR-0005/ADR-0012). The
  // never-fabricate contract surfaces as a 422 when the model produced nothing
  // usable (ErrNoScenarios) or a malformed block (ErrMalformedScenarios); this maps
  // to a typed DistillNoScenariosError so the UI shows guidance, not a crash. The
  // conversation is sent as the request body only — never logged or stored here.
  distill(projectId: string, conversation: string): Promise<DistillResult> {
    return this.request<DistillResult>(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/distill`,
      { conversation },
    ).catch((err: unknown) => {
      if (err instanceof ApiError && err.status === 422) {
        // 422 is NOT a transport/auth failure: the distiller honestly produced
        // nothing approvable. Re-throw as a distinct typed signal carrying the
        // gateway's secret-free reason so the UI can render review guidance.
        throw new DistillNoScenariosError(err.message);
      }
      throw err;
    });
  }

  // enhance expands a rough request into a detailed Turkish spec by having an AGENT read the
  // project's REAL code (code-aware). It POSTs the rough text to create a job, then polls the
  // job until the agent writes back the spec (done) or fails. While the job runs, the agent
  // streams claude's REAL activity (read/search/think) into the job's `progress`; onProgress
  // fires with the latest line whenever it changes (detail OR progress_at), so the UI can show a
  // live status and reset its idle timer even when the same activity repeats. No fabricated
  // lines — each one is a real claude event. The rough text + produced spec + progress cross the
  // authed channel only (secret-free).
  async enhance(
    projectId: string,
    roughSpec: string,
    onProgress?: (detail: string) => void,
  ): Promise<string> {
    const created = await this.request<{ id: string; status: string }>(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/enhance`,
      { rough_spec: roughSpec },
    );
    const jobId = created.id;
    const deadline = Date.now() + 19 * 60_000; // generous: agent code exploration + completeness pass
    let lastSig = ""; // detail|progress_at, so repeated activity still resets the UI idle timer
    for (;;) {
      const job = await this.request<{
        status: string;
        result?: string;
        error?: string;
        progress?: string;
        progress_at?: string;
      }>(
        "GET",
        `/projects/${encodeURIComponent(projectId)}/enhance/${encodeURIComponent(jobId)}`,
      );
      if (onProgress && job.progress) {
        const sig = `${job.progress}|${job.progress_at ?? ""}`;
        if (sig !== lastSig) {
          lastSig = sig;
          onProgress(job.progress);
        }
      }
      if (job.status === "done") return job.result ?? "";
      if (job.status === "failed") throw new Error(job.error || "enhance failed");
      if (Date.now() > deadline) throw new Error("enhance timed out — no agent picked it up");
      await new Promise((r) => setTimeout(r, 2000));
    }
  }

  // distillStream is the STREAMING variant of distill (Q3c.4): it POSTs the same
  // conversation to /distill/stream and surfaces live progress (the model-output line
  // COUNT, never content) via onProgress while the model works, resolving with the
  // SAME DistillResult (scenarios OR clarifying questions). When the transport cannot
  // stream (the fork postMessage bridge), it transparently FALLS BACK to the
  // non-streaming distill — onProgress simply never fires. The 422 never-fabricate
  // contract is preserved (DistillNoScenariosError); conversation/token are never logged.
  async distillStream(
    projectId: string,
    conversation: string,
    onProgress?: (lines: number) => void,
  ): Promise<DistillResult> {
    const transport = this.transport;
    if (transport.sendStream === undefined) {
      return this.distill(projectId, conversation);
    }
    let result: DistillResult | undefined;
    let streamErr: Error | undefined;
    const res = await transport.sendStream(
      {
        method: "POST",
        path: `/projects/${encodeURIComponent(projectId)}/distill/stream`,
        body: JSON.stringify({ conversation }),
        contentType: "application/json",
      },
      (event, data) => {
        if (event === "progress") {
          try {
            const p = JSON.parse(data) as { lines?: number };
            if (typeof p.lines === "number") {
              onProgress?.(p.lines);
            }
          } catch {
            // advisory only — ignore a malformed progress frame.
          }
        } else if (event === "result") {
          try {
            result = JSON.parse(data) as DistillResult;
          } catch {
            streamErr = new ApiError(502, "distiller returned a malformed result");
          }
        } else if (event === "error") {
          streamErr = sseDistillError(data);
        }
      },
    );
    // A pre-stream HTTP failure (401/404/400/501): map like a normal request.
    if (!res.ok) {
      const msg = errorMessageFromBody(res.body, res.status);
      if (res.status === 422) {
        throw new DistillNoScenariosError(msg);
      }
      throw new ApiError(res.status, msg);
    }
    if (streamErr !== undefined) {
      throw streamErr;
    }
    if (result !== undefined) {
      return result;
    }
    throw new ApiError(502, "distill stream ended without a result");
  }

  // intake POSTs RAW scenario YAML (text/plain), not JSON — mirrors handleIntake
  // which reads the body verbatim and parses it as YAML.
  intake(projectId: string, yaml: string): Promise<IntakeResult> {
    return this.request<IntakeResult>(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/intake`,
      yaml,
      "text/plain; charset=utf-8",
    );
  }

  // putHoldout stores an (approved, reviewed) hidden-holdout body so the agent's gate can run it
  // (Faz-S S5). File contents are UTF-8 → base64 so arbitrary bytes survive the JSON round-trip.
  // Returns the stored locator (what a scenario's hidden_holdout_ref should point at).
  putHoldout(id: string, files: Record<string, string>): Promise<{ locator: string }> {
    const encoded: Record<string, string> = {};
    for (const [path, content] of Object.entries(files)) {
      encoded[path] = utf8ToBase64(content);
    }
    return this.request<{ locator: string }>(
      "PUT",
      `/holdouts/${encodeURIComponent(id)}`,
      { files: encoded },
    );
  }

  pause(projectId: string): Promise<{ project: string; paused: boolean }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/pause`);
  }

  resume(projectId: string): Promise<{ project: string; paused: boolean }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/resume`);
  }

  abort(
    projectId: string,
  ): Promise<{ project: string; aborted_task: string }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/abort`);
  }

  // retry resets a BLOCKED task back to "ready" so the agent re-picks it (the gateway
  // clears the stale abort + block reason). Returns the project, task, and new status.
  retry(
    projectId: string,
    taskId: string,
  ): Promise<{ project: string; task: string; status: string }> {
    return this.request(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/retry`,
    );
  }

  approve(
    projectId: string,
    taskId?: string,
  ): Promise<{ project: string; approved_task: string }> {
    const body = taskId !== undefined ? { task_id: taskId } : undefined;
    return this.request(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/approve`,
      body,
    );
  }

  // reject DECLINES a held awaiting-approval task (counterpart to approve): the gateway moves it to
  // the terminal "rejected" status WITHOUT merging. taskId targets the specific held task; reason is
  // an optional short note recorded on the task so the board shows why it left the review queue.
  reject(
    projectId: string,
    taskId?: string,
    reason?: string,
  ): Promise<{ project: string; rejected_task: string }> {
    const body: { task_id?: string; reason?: string } = {};
    if (taskId !== undefined) body.task_id = taskId;
    if (reason !== undefined && reason !== "") body.reason = reason;
    return this.request(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/reject`,
      Object.keys(body).length > 0 ? body : undefined,
    );
  }

  // --- intake conversation history (intakesession.go) ---

  // listIntakeSessions returns a project's saved intake conversations newest-first as light
  // summaries (no message bodies). Empty when none are saved.
  listIntakeSessions(projectId: string): Promise<IntakeSessionSummary[]> {
    return this.request<{ sessions: IntakeSessionSummary[] }>(
      "GET",
      `/projects/${encodeURIComponent(projectId)}/intake/sessions`,
    ).then((r) => r.sessions ?? []);
  }

  // getIntakeSession returns one saved conversation in full (the message thread + any draft YAML).
  getIntakeSession(
    projectId: string,
    sessionId: string,
  ): Promise<IntakeSessionDetail> {
    return this.request<IntakeSessionDetail>(
      "GET",
      `/projects/${encodeURIComponent(projectId)}/intake/sessions/${encodeURIComponent(sessionId)}`,
    );
  }

  // putIntakeSession UPSERTS a conversation snapshot (the web mints the id; saving after each turn
  // builds the history). created_at (optional) stamps the conversation's start on the first save;
  // the gateway preserves it across later saves. The conversation crosses the authed channel only.
  putIntakeSession(
    projectId: string,
    sessionId: string,
    snapshot: {
      title: string;
      messages: IntakeSessionMessage[];
      result?: string;
      created_at?: string;
    },
  ): Promise<{ status: string; id: string }> {
    return this.request(
      "PUT",
      `/projects/${encodeURIComponent(projectId)}/intake/sessions/${encodeURIComponent(sessionId)}`,
      snapshot,
    );
  }

  // --- core request plumbing ---

  // request issues a single call through the transport. A plain object body is
  // JSON-encoded; a string body is sent verbatim with the given contentType (used
  // for raw YAML intake). The transport owns auth + the network; this layer owns
  // serialization, error mapping (non-2xx → ApiError), and JSON parsing.
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    contentType?: string,
  ): Promise<T> {
    let payload: string | undefined;
    let ct: string | undefined;
    if (typeof body === "string") {
      payload = body;
      ct = contentType ?? "text/plain; charset=utf-8";
    } else if (body !== undefined) {
      payload = JSON.stringify(body);
      ct = "application/json";
    }

    // Build the request omitting optional keys when absent (exactOptionalPropertyTypes:
    // never assign an explicit undefined to an optional prop). ct is always set when
    // payload is, but the two vars don't narrow together, so guard ct explicitly.
    const req: HttpRequest = { method, path };
    if (payload !== undefined) {
      req.body = payload;
      if (ct !== undefined) {
        req.contentType = ct;
      }
    }

    const res = await this.transport.send(req);

    if (!res.ok) {
      throw new ApiError(res.status, errorMessageFromBody(res.body, res.status));
    }

    // 204 / empty body → undefined; all gateway 2xx responses carry JSON.
    if (res.body.length === 0) {
      return undefined as T;
    }
    return JSON.parse(res.body) as T;
  }
}

// errorMessageFromBody extracts the gateway's {error: string} message from a
// failed response body, falling back to a generic status line. Never throws (a
// non-JSON body is tolerated) and never includes the token.
function errorMessageFromBody(body: string, status: number): string {
  try {
    const data: unknown = JSON.parse(body);
    if (
      data !== null &&
      typeof data === "object" &&
      "error" in data &&
      typeof (data as { error: unknown }).error === "string"
    ) {
      return (data as { error: string }).error;
    }
  } catch {
    // non-JSON / empty body — fall through to the generic message.
  }
  return `request failed with status ${status}`;
}

// sseDistillError maps a distill-stream `error` frame ({status,error}) to a typed
// error: a 422 is the never-fabricate DistillNoScenariosError (guidance), anything
// else an ApiError. A malformed frame degrades to an opaque 502 ApiError.
function sseDistillError(data: string): Error {
  try {
    const e = JSON.parse(data) as { status?: number; error?: string };
    const msg = typeof e.error === "string" ? e.error : "distiller failed";
    if (e.status === 422) {
      return new DistillNoScenariosError(msg);
    }
    return new ApiError(typeof e.status === "number" ? e.status : 502, msg);
  } catch {
    return new ApiError(502, "distiller failed");
  }
}

// eventQueryString builds the GET /events query string from an EventQuery,
// omitting empty values and only emitting intervention=1 when true (matching the
// gateway's isTruthy parsing).
export function eventQueryString(params: EventQuery): string {
  const q = new URLSearchParams();
  if (params.project) q.set("project", params.project);
  if (params.task) q.set("task", params.task);
  if (params.phase) q.set("phase", params.phase);
  if (params.kind) q.set("kind", params.kind);
  if (params.intervention) q.set("intervention", "1");
  if (params.since) q.set("since", params.since);
  if (params.limit !== undefined) q.set("limit", String(params.limit));
  const s = q.toString();
  return s.length > 0 ? `?${s}` : "";
}
