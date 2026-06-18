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
});

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
