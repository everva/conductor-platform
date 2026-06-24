// Deterministic, headless tests for the host-side authed control client (4C-2). `fetch`
// is injected (the constructor's testing seam; defaults to the global on Node 20), so
// these run offline with no gateway. They lock the gateway control contract (verified
// from cmd/conductor-api/control.go): pause/resume/abort/approve POST to
// /projects/{id}/{action} with "Authorization: Bearer <token>"; 2xx → ok, 401 →
// unauthorized, 409 → conflict, other/throw → unreachable; approve sends {task_id} only
// when given; listProjects GETs /projects and returns the ids. The token-leak guard proves
// a sentinel token rides ONLY in the Authorization header, never in a returned value.
import { describe, expect, it, vi } from "vitest";

import { ControlClient, ControlListError, type ProjectRef } from "./controlClient";

const BASE = "http://gw.test";
const SENTINEL = "tok-SENTINEL-do-not-leak";

/** A TokenProvider returning the sentinel token. */
function tokenProvider() {
  return { getToken: () => Promise.resolve<string | undefined>(SENTINEL) };
}

/** A TokenProvider with NO stored token (models "not connected"). */
function noTokenProvider() {
  return { getToken: () => Promise.resolve<string | undefined>(undefined) };
}

/** Builds a fetch mock with a per-call response factory and captures every call so a test
 * can assert the URL + init (headers/method/body). */
function fetchMock(
  impl: (url: string, init: RequestInit | undefined) => Response | Promise<Response>,
) {
  return vi.fn((input: string | URL | Request, init?: RequestInit) =>
    Promise.resolve(impl(String(input), init)),
  ) as unknown as typeof fetch;
}

/** Reads the Authorization header off a captured RequestInit (headers is a plain object
 * in every call this client makes). */
function authOf(init: RequestInit | undefined): string | undefined {
  const headers = (init?.headers ?? {}) as Record<string, string>;
  return headers.Authorization;
}

describe("ControlClient.listProjects", () => {
  it("GETs /projects with the bearer header and returns only the ids (tolerating the full shape)", async () => {
    const fn = fetchMock(
      () =>
        new Response(
          JSON.stringify([
            { id: "alpha", name: "Alpha", paused: false },
            { id: "beta", running: true },
          ]),
          { status: 200 },
        ),
    );
    const client = new ControlClient(BASE, tokenProvider(), fn);

    const projects = await client.listProjects();

    expect(projects).toEqual<ProjectRef[]>([{ id: "alpha" }, { id: "beta" }]);
    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    const [url, init] = mock.mock.calls[0] as [string, RequestInit | undefined];
    expect(url).toBe("http://gw.test/projects");
    expect(init?.method).toBe("GET");
    expect(authOf(init)).toBe(`Bearer ${SENTINEL}`);
  });

  it("drops malformed entries (non-object / missing or non-string id)", async () => {
    const fn = fetchMock(
      () =>
        new Response(
          JSON.stringify([{ id: "ok" }, null, 42, { name: "no-id" }, { id: 7 }]),
          { status: 200 },
        ),
    );
    const client = new ControlClient(BASE, tokenProvider(), fn);
    expect(await client.listProjects()).toEqual([{ id: "ok" }]);
  });

  it("returns [] when the payload is not an array", async () => {
    const fn = fetchMock(() => new Response(JSON.stringify({ nope: true }), { status: 200 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);
    expect(await client.listProjects()).toEqual([]);
  });

  it("throws ControlListError 'not-connected' (status 0) and sends NO request when there is no token", async () => {
    const fn = fetchMock(() => new Response(null, { status: 200 }));
    const client = new ControlClient(BASE, noTokenProvider(), fn);

    await expect(client.listProjects()).rejects.toMatchObject({
      reason: "not-connected",
      status: 0,
    });
    expect(fn as unknown as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();
  });

  it("throws ControlListError 'unauthorized' on 401", async () => {
    const fn = fetchMock(() => new Response(null, { status: 401 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);
    await expect(client.listProjects()).rejects.toMatchObject({ reason: "unauthorized", status: 401 });
  });

  it("throws ControlListError 'unreachable' on a non-2xx (e.g. 500)", async () => {
    const fn = fetchMock(() => new Response(null, { status: 500 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);
    await expect(client.listProjects()).rejects.toMatchObject({ reason: "unreachable", status: 500 });
  });

  it("throws ControlListError 'unreachable' (status 0) on a network error", async () => {
    const fn = fetchMock(() => {
      throw new Error("ECONNREFUSED");
    });
    const client = new ControlClient(BASE, tokenProvider(), fn);
    await expect(client.listProjects()).rejects.toBeInstanceOf(ControlListError);
    await expect(client.listProjects()).rejects.toMatchObject({ reason: "unreachable", status: 0 });
  });
});

describe("ControlClient pause/resume/abort", () => {
  it.each(["pause", "resume", "abort"] as const)(
    "%s POSTs /projects/{id}/%s with the bearer header and maps 200 → ok",
    async (action) => {
      const fn = fetchMock(
        () => new Response(JSON.stringify({ project: "p1", ok: true }), { status: 200 }),
      );
      const client = new ControlClient(BASE, tokenProvider(), fn);

      const result = await client[action]("p1");

      expect(result.ok).toBe(true);
      const mock = fn as unknown as ReturnType<typeof vi.fn>;
      const [url, init] = mock.mock.calls[0] as [string, RequestInit | undefined];
      expect(url).toBe(`http://gw.test/projects/p1/${action}`);
      expect(init?.method).toBe("POST");
      expect(authOf(init)).toBe(`Bearer ${SENTINEL}`);
    },
  );

  it("maps 401 → unauthorized, 409 → conflict, 500 → unreachable, throw → unreachable", async () => {
    const make = (impl: Parameters<typeof fetchMock>[0]) =>
      new ControlClient(BASE, tokenProvider(), fetchMock(impl));

    const unauthorized = await make(() => new Response(null, { status: 401 })).abort("p");
    expect(unauthorized).toEqual({ ok: false, reason: "unauthorized", status: 401 });

    const conflict = await make(() => new Response(null, { status: 409 })).abort("p");
    expect(conflict).toEqual({ ok: false, reason: "conflict", status: 409 });

    const unreachable = await make(() => new Response(null, { status: 500 })).pause("p");
    expect(unreachable).toEqual({ ok: false, reason: "unreachable", status: 500 });

    const thrown = await make(() => {
      throw new Error("boom");
    }).resume("p");
    expect(thrown).toEqual({ ok: false, reason: "unreachable", status: 0 });
  });

  it("returns not-connected (status 0) and sends NO request when there is no token", async () => {
    const fn = fetchMock(() => new Response(null, { status: 200 }));
    const client = new ControlClient(BASE, noTokenProvider(), fn);

    const result = await client.pause("p1");

    expect(result).toEqual({ ok: false, reason: "not-connected", status: 0 });
    expect(fn as unknown as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();
  });

  it("encodeURIComponent-escapes the project id in the URL", async () => {
    const fn = fetchMock(() => new Response(null, { status: 200 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);

    await client.pause("a/b c");

    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    const [url] = mock.mock.calls[0] as [string];
    expect(url).toBe("http://gw.test/projects/a%2Fb%20c/pause");
  });
});

describe("ControlClient.approve", () => {
  it("sends NO body when no taskId is given (auto-resolve path)", async () => {
    const fn = fetchMock(() => new Response(JSON.stringify({ approved_task: "t1" }), { status: 200 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);

    const result = await client.approve("p1");

    expect(result.ok).toBe(true);
    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    const [url, init] = mock.mock.calls[0] as [string, RequestInit | undefined];
    expect(url).toBe("http://gw.test/projects/p1/approve");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBeUndefined();
    // No JSON content-type when there is no body.
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers["Content-Type"]).toBeUndefined();
  });

  it("sends {task_id} JSON (with content-type) when a taskId is given", async () => {
    const fn = fetchMock(() => new Response(JSON.stringify({ approved_task: "t9" }), { status: 200 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);

    await client.approve("p1", "t9");

    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    const [, init] = mock.mock.calls[0] as [string, RequestInit | undefined];
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers["Content-Type"]).toBe("application/json");
    expect(authOf(init)).toBe(`Bearer ${SENTINEL}`);
    expect(JSON.parse(String(init?.body))).toEqual({ task_id: "t9" });
  });

  it("maps 409 → conflict (zero or multiple awaiting)", async () => {
    const fn = fetchMock(() => new Response(null, { status: 409 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);
    expect(await client.approve("p1")).toEqual({ ok: false, reason: "conflict", status: 409 });
  });
});

describe("ControlClient.intake (Faz-R native dispatch)", () => {
  const YAML = 'id: "W-1"\ntitle: "x"\nlane: "backend"\ntier: "T2"\nacceptance:\n  - "a"\nhidden_holdout_ref: "store://h/W-1/h.go"\n';

  it("POSTs the RAW yaml body (not JSON) with a yaml content-type + bearer header", async () => {
    const fn = fetchMock(() => new Response(JSON.stringify({ created: ["W-1"], skipped: [] }), { status: 200 }));
    const client = new ControlClient(BASE, tokenProvider(), fn);

    const result = await client.intake("p1", YAML);

    expect(result).toEqual({ ok: true, body: { created: ["W-1"], skipped: [] } });
    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    const [url, init] = mock.mock.calls[0] as [string, RequestInit | undefined];
    expect(url).toBe("http://gw.test/projects/p1/intake");
    expect(init?.method).toBe("POST");
    // The body is the raw YAML string verbatim — NOT JSON-wrapped.
    expect(init?.body).toBe(YAML);
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers["Content-Type"]).toBe("application/yaml");
    expect(authOf(init)).toBe(`Bearer ${SENTINEL}`);
  });

  it("maps a 400 → invalid and surfaces the gateway's secret-free {error} message as detail", async () => {
    const fn = fetchMock(
      () => new Response(JSON.stringify({ error: 'scenario "W-1" invalid: unknown tier "T9"' }), { status: 400 }),
    );
    const client = new ControlClient(BASE, tokenProvider(), fn);

    const result = await client.intake("p1", YAML);

    expect(result).toEqual({
      ok: false,
      reason: "invalid",
      status: 400,
      detail: 'scenario "W-1" invalid: unknown tier "T9"',
    });
  });

  it("maps 401 → unauthorized, no-token → not-connected, and a throw → unreachable", async () => {
    const unauth = new ControlClient(BASE, tokenProvider(), fetchMock(() => new Response(null, { status: 401 })));
    expect(await unauth.intake("p1", YAML)).toEqual({ ok: false, reason: "unauthorized", status: 401 });

    const disconnected = new ControlClient(BASE, noTokenProvider(), fetchMock(() => new Response(null, { status: 200 })));
    expect(await disconnected.intake("p1", YAML)).toEqual({ ok: false, reason: "not-connected", status: 0 });

    const throws = new ControlClient(
      BASE,
      tokenProvider(),
      vi.fn(() => Promise.reject(new Error("network"))) as unknown as typeof fetch,
    );
    expect(await throws.intake("p1", YAML)).toEqual({ ok: false, reason: "unreachable", status: 0 });
  });
});

describe("token-leak guard", () => {
  it("never returns the bearer token in any ControlResult — it rides ONLY in the Authorization header", async () => {
    // Drive every method across success + each failure mapping, capturing the requests.
    // The gateway body deliberately does NOT contain the token (the token is auth-only),
    // so any appearance of the sentinel in a returned value would be a real leak.
    const responses = [200, 401, 409, 500];
    let i = 0;
    const fn = fetchMock(() => {
      const status = responses[i % responses.length] ?? 200;
      i += 1;
      return new Response(JSON.stringify({ project: "p", ok: status === 200 }), { status });
    });
    const client = new ControlClient(BASE, tokenProvider(), fn);

    const results: unknown[] = [];
    results.push(await client.pause("p"));
    results.push(await client.resume("p"));
    results.push(await client.abort("p"));
    results.push(await client.approve("p"));
    results.push(await client.approve("p", "t1"));

    // listProjects happy result (ids are gateway data, not the token).
    const okList = new ControlClient(
      BASE,
      tokenProvider(),
      fetchMock(() => new Response(JSON.stringify([{ id: "alpha" }]), { status: 200 })),
    );
    results.push(await okList.listProjects());
    // listProjects error reasons → ControlListError (message must be token-free too).
    for (const status of [401, 500]) {
      try {
        await new ControlClient(
          BASE,
          tokenProvider(),
          fetchMock(() => new Response(null, { status })),
        ).listProjects();
      } catch (err) {
        results.push(err);
      }
    }

    // The sentinel (the bearer token) must appear in NONE of the returned/thrown values.
    for (const r of results) {
      if (r instanceof ControlListError) {
        expect(r.message).not.toContain(SENTINEL);
        continue;
      }
      expect(JSON.stringify(r)).not.toContain(SENTINEL);
    }

    // And the token DID ride in the Authorization header of every actual request.
    const mock = fn as unknown as ReturnType<typeof vi.fn>;
    for (const call of mock.mock.calls) {
      const init = call[1] as RequestInit | undefined;
      expect(authOf(init)).toBe(`Bearer ${SENTINEL}`);
    }
  });
});
