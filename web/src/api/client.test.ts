// Client tests: assert the bearer header is attached to every request and that a
// non-2xx response maps to the typed ApiError carrying status + gateway {error}.
// fetch is mocked so the test is fully offline and deterministic. No real token
// is used.
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, DistillNoScenariosError } from "./client.ts";

const TEST_TOKEN = "test-token-123";

afterEach(() => {
  vi.restoreAllMocks();
});

function mockFetch(impl: (url: string, init: RequestInit) => Response) {
  const fn = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(impl(String(input), init ?? {})),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("ApiClient", () => {
  it("attaches Authorization: Bearer on requests", async () => {
    const fetchFn = mockFetch(() => jsonResponse(200, []));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    await client.listProjects();

    expect(fetchFn).toHaveBeenCalledTimes(1);
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects");
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers.Authorization).toBe(`Bearer ${TEST_TOKEN}`);
  });

  it("maps a non-2xx response to a typed ApiError with status + body message", async () => {
    mockFetch(() => jsonResponse(401, { error: "unauthorized" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    await expect(client.status()).rejects.toMatchObject({
      name: "ApiError",
      status: 401,
      message: "unauthorized",
    });

    // Also confirm the thrown value is the ApiError class instance.
    const err = await client.status().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
  });

  it("POSTs raw YAML for intake with a text/plain content type", async () => {
    const fetchFn = mockFetch(() =>
      jsonResponse(200, { created: ["s1"], skipped: [] }),
    );
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const res = await client.intake("proj", "id: s1\n");

    expect(res).toEqual({ created: ["s1"], skipped: [] });
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/intake");
    expect((init as RequestInit).body).toBe("id: s1\n");
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers["Content-Type"]).toContain("text/plain");
  });

  it("distill POSTs the conversation as JSON with bearer and parses {scenarios, yaml}", async () => {
    const body = {
      scenarios: [
        {
          id: "A-1",
          title: "First",
          lane: "backend",
          tier: "T1",
          deps: [],
          acceptance: ["does a thing"],
          hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go",
        },
      ],
      yaml: "id: A-1\n",
    };
    const fetchFn = mockFetch(() => jsonResponse(200, body));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const res = await client.distill("proj", "build the thing");

    expect(res.yaml).toBe("id: A-1\n");
    expect(res.scenarios[0].id).toBe("A-1");
    expect(res.scenarios[0].hidden_holdout_ref).toBe("store://holdouts/A-1/holdout_test.go");
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/distill");
    expect((init as RequestInit).body).toBe(
      JSON.stringify({ conversation: "build the thing" }),
    );
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers.Authorization).toBe(`Bearer ${TEST_TOKEN}`);
    expect(headers["Content-Type"]).toBe("application/json");
  });

  it("distill maps a 422 to the typed DistillNoScenariosError (never-fabricate)", async () => {
    mockFetch(() =>
      jsonResponse(422, { error: "no scenarios could be distilled from the conversation" }),
    );
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const err = await client.distill("proj", "hi").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DistillNoScenariosError);
    expect((err as DistillNoScenariosError).message).toContain("no scenarios");
  });

  it("distill leaves a 401 as an ApiError for the caller to sign out", async () => {
    mockFetch(() => jsonResponse(401, { error: "unauthorized" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const err = await client.distill("proj", "hi").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
  });

  it("distillStream streams progress line counts and resolves the result (Q3c.4)", async () => {
    const result = {
      scenarios: [
        {
          id: "A-1",
          title: "First",
          lane: "backend",
          tier: "T1",
          deps: [],
          acceptance: ["does a thing"],
          hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go",
        },
      ],
      yaml: "id: A-1\n",
    };
    const sse =
      'event: progress\ndata: {"lines":1}\n\n' +
      'event: progress\ndata: {"lines":2}\n\n' +
      `event: result\ndata: ${JSON.stringify(result)}\n\n`;
    const fetchFn = mockFetch(() => sseResponse(200, sse));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const progress: number[] = [];
    const res = await client.distillStream("proj", "build it", (n) => progress.push(n));

    expect(progress).toEqual([1, 2]);
    expect(res.scenarios[0].id).toBe("A-1");
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/distill/stream");
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers.Authorization).toBe(`Bearer ${TEST_TOKEN}`);
  });

  it("distillStream resolves a clarifying-questions result", async () => {
    const result = {
      scenarios: [],
      yaml: "",
      questions: [
        {
          question: "Which datastore?",
          header: "Datastore",
          multi_select: false,
          options: [
            { label: "Postgres", description: "relational" },
            { label: "Redis", description: "cache" },
          ],
        },
      ],
    };
    mockFetch(() => sseResponse(200, `event: result\ndata: ${JSON.stringify(result)}\n\n`));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const res = await client.distillStream("proj", "hi");
    expect(res.questions?.length).toBe(1);
    expect(res.questions?.[0].header).toBe("Datastore");
  });

  it("distillStream maps a 422 error frame to DistillNoScenariosError (never-fabricate)", async () => {
    mockFetch(() =>
      sseResponse(200, 'event: error\ndata: {"status":422,"error":"no scenarios"}\n\n'),
    );
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const err = await client.distillStream("proj", "hi").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DistillNoScenariosError);
  });

  it("distillStream maps a pre-stream 422 (non-SSE body) to DistillNoScenariosError", async () => {
    mockFetch(() => jsonResponse(422, { error: "no scenarios could be distilled" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const err = await client.distillStream("proj", "hi").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DistillNoScenariosError);
  });

  it("distillStream falls back to distill when the transport cannot stream (fork bridge)", async () => {
    const send = vi.fn(() =>
      Promise.resolve({ status: 200, ok: true, body: JSON.stringify({ scenarios: [], yaml: "" }) }),
    );
    const client = new ApiClient({ transport: { send } });

    const progress: number[] = [];
    const res = await client.distillStream("proj", "hi", (n) => progress.push(n));

    expect(send).toHaveBeenCalledTimes(1);
    expect(progress).toEqual([]); // no streaming transport → no progress events
    expect(res.scenarios).toEqual([]);
  });

  it("reject POSTs task_id + reason and returns the rejected task", async () => {
    const fetchFn = mockFetch(() => jsonResponse(200, { project: "proj", rejected_task: "T-9" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const res = await client.reject("proj", "T-9", "not needed");

    expect(res).toEqual({ project: "proj", rejected_task: "T-9" });
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/reject");
    expect((init as RequestInit).method).toBe("POST");
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ task_id: "T-9", reason: "not needed" });
  });

  it("reject omits an empty reason from the body", async () => {
    const fetchFn = mockFetch(() => jsonResponse(200, { project: "proj", rejected_task: "T-9" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    await client.reject("proj", "T-9");

    const [, init] = fetchFn.mock.calls[0];
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({ task_id: "T-9" });
  });

  it("listIntakeSessions GETs and unwraps the {sessions} envelope", async () => {
    const fetchFn = mockFetch(() =>
      jsonResponse(200, {
        sessions: [
          {
            id: "is-1",
            project_id: "proj",
            title: "remove X",
            has_result: true,
            created_at: "2026-06-27T10:00:00Z",
            updated_at: "2026-06-27T10:05:00Z",
          },
        ],
      }),
    );
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const sessions = await client.listIntakeSessions("proj");

    expect(sessions).toHaveLength(1);
    expect(sessions[0].id).toBe("is-1");
    expect(sessions[0].has_result).toBe(true);
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/intake/sessions");
    expect((init as RequestInit).method).toBe("GET");
  });

  it("getIntakeSession GETs the full conversation thread", async () => {
    const fetchFn = mockFetch(() =>
      jsonResponse(200, {
        id: "is-1",
        project_id: "proj",
        title: "remove X",
        messages: [
          { role: "you", text: "kaldır" },
          { role: "assistant", text: "ok", tone: "warn" },
        ],
        result: "id: A-1\n",
        created_at: "2026-06-27T10:00:00Z",
        updated_at: "2026-06-27T10:05:00Z",
      }),
    );
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const full = await client.getIntakeSession("proj", "is-1");

    expect(full.messages).toHaveLength(2);
    expect(full.messages[1].tone).toBe("warn");
    expect(full.result).toBe("id: A-1\n");
    const [url] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/intake/sessions/is-1");
  });

  it("putIntakeSession PUTs the conversation snapshot as JSON", async () => {
    const fetchFn = mockFetch(() => jsonResponse(200, { status: "ok", id: "is-1" }));
    const client = new ApiClient({ baseUrl: "https://gw.test", token: TEST_TOKEN });

    const snapshot = {
      title: "remove X",
      messages: [{ role: "you" as const, text: "kaldır" }],
      result: "id: A-1\n",
      created_at: "2026-06-27T10:00:00Z",
    };
    const res = await client.putIntakeSession("proj", "is-1", snapshot);

    expect(res).toEqual({ status: "ok", id: "is-1" });
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects/proj/intake/sessions/is-1");
    expect((init as RequestInit).method).toBe("PUT");
    expect(JSON.parse((init as RequestInit).body as string)).toEqual(snapshot);
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers["Content-Type"]).toBe("application/json");
  });
});

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// sseResponse builds a streamed text/event-stream Response (explicit ReadableStream
// so res.body.getReader() works in the test environment).
function sseResponse(status: number, body: string): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(body));
      controller.close();
    },
  });
  return new Response(stream, {
    status,
    headers: { "Content-Type": "text/event-stream" },
  });
}
