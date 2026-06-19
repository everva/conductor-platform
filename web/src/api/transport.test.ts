// Transport-seam tests (4A-1): prove the REST seam is real and behavior-preserving.
//   1) FetchTransport attaches the bearer header, joins baseUrl+path, sets
//      Content-Type only when a body is present, and returns {status, ok, body}.
//   2) ApiClient built via the { transport } form delegates to an injected
//      HttpTransport WITHOUT holding a token (token-agnostic path), and still maps
//      non-ok → ApiError and 422-on-distill → DistillNoScenariosError.
// Fully offline; no real token, no real network.
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiClient,
  ApiError,
  DistillNoScenariosError,
  FetchTransport,
  type HttpRequest,
  type HttpResponse,
  type HttpTransport,
} from "./client.ts";

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

// FakeTransport records every request and replays a scripted response so the
// ApiClient can be exercised with NO token and no network.
class FakeTransport implements HttpTransport {
  calls: HttpRequest[] = [];
  private readonly responder: (req: HttpRequest) => HttpResponse;
  constructor(responder: (req: HttpRequest) => HttpResponse) {
    this.responder = responder;
  }
  send(req: HttpRequest): Promise<HttpResponse> {
    this.calls.push(req);
    return Promise.resolve(this.responder(req));
  }
}

describe("FetchTransport", () => {
  it("attaches Authorization, joins baseUrl+path, and returns {status, ok, body}", async () => {
    const fetchFn = mockFetch(() => new Response("[]", { status: 200 }));
    const t = new FetchTransport("https://gw.test", TEST_TOKEN);

    const res = await t.send({ method: "GET", path: "/projects" });

    expect(res).toEqual({ status: 200, ok: true, body: "[]" });
    expect(fetchFn).toHaveBeenCalledTimes(1);
    const [url, init] = fetchFn.mock.calls[0];
    expect(String(url)).toBe("https://gw.test/projects");
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers.Authorization).toBe(`Bearer ${TEST_TOKEN}`);
    // No body → no Content-Type header.
    expect(headers["Content-Type"]).toBeUndefined();
  });

  it("sets Content-Type and forwards the body when a body is present", async () => {
    const fetchFn = mockFetch(() => new Response("{}", { status: 200 }));
    const t = new FetchTransport("https://gw.test", TEST_TOKEN);

    await t.send({
      method: "POST",
      path: "/projects/proj/intake",
      body: "id: s1\n",
      contentType: "text/plain; charset=utf-8",
    });

    const [, init] = fetchFn.mock.calls[0];
    expect((init as RequestInit).body).toBe("id: s1\n");
    const headers = (init as RequestInit).headers as Record<string, string>;
    expect(headers["Content-Type"]).toBe("text/plain; charset=utf-8");
  });

  it("surfaces a non-ok status as ok:false with the raw body text", async () => {
    mockFetch(() => new Response('{"error":"unauthorized"}', { status: 401 }));
    const t = new FetchTransport("https://gw.test", TEST_TOKEN);

    const res = await t.send({ method: "GET", path: "/status" });

    expect(res.status).toBe(401);
    expect(res.ok).toBe(false);
    expect(res.body).toBe('{"error":"unauthorized"}');
  });
});

describe("ApiClient via injected transport", () => {
  it("delegates to the transport WITHOUT a token (token-agnostic path)", async () => {
    const fake = new FakeTransport(() => ({
      status: 200,
      ok: true,
      body: JSON.stringify([{ id: "p1" }]),
    }));
    // Note: no token anywhere — the injected transport owns auth.
    const client = new ApiClient({ transport: fake });

    const projects = await client.listProjects();

    expect(projects).toEqual([{ id: "p1" }]);
    expect(fake.calls).toHaveLength(1);
    expect(fake.calls[0]).toMatchObject({ method: "GET", path: "/projects" });
    // The request carries no auth material — that's the transport's job.
    expect(JSON.stringify(fake.calls[0])).not.toContain("Bearer");
  });

  it("serializes a JSON body with application/json through the transport", async () => {
    const fake = new FakeTransport(() => ({ status: 200, ok: true, body: "{}" }));
    const client = new ApiClient({ transport: fake });

    await client.onboard("git@host:repo.git", "main");

    expect(fake.calls[0]).toMatchObject({
      method: "POST",
      path: "/projects",
      contentType: "application/json",
    });
    expect(fake.calls[0].body).toBe(
      JSON.stringify({ repo: "git@host:repo.git", base_branch: "main" }),
    );
  });

  it("maps a non-ok transport response to a typed ApiError", async () => {
    const fake = new FakeTransport(() => ({
      status: 401,
      ok: false,
      body: JSON.stringify({ error: "unauthorized" }),
    }));
    const client = new ApiClient({ transport: fake });

    const err = await client.status().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect((err as ApiError).message).toBe("unauthorized");
  });

  it("falls back to a generic message on a non-JSON error body", async () => {
    const fake = new FakeTransport(() => ({
      status: 500,
      ok: false,
      body: "<html>boom</html>",
    }));
    const client = new ApiClient({ transport: fake });

    const err = await client.status().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).message).toBe("request failed with status 500");
  });

  it("maps a 422 on distill to the typed DistillNoScenariosError", async () => {
    const fake = new FakeTransport(() => ({
      status: 422,
      ok: false,
      body: JSON.stringify({ error: "no scenarios could be distilled" }),
    }));
    const client = new ApiClient({ transport: fake });

    const err = await client.distill("proj", "hi").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(DistillNoScenariosError);
    expect((err as DistillNoScenariosError).message).toContain("no scenarios");
  });

  it("returns undefined for an empty (204-style) body", async () => {
    const fake = new FakeTransport(() => ({ status: 200, ok: true, body: "" }));
    const client = new ApiClient({ transport: fake });

    const res = await client.pause("proj");
    expect(res).toBeUndefined();
  });
});
