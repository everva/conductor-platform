// Pins the P2b host-side full-diff client (diffContentClient.ts): the authed GET, the token
// discipline (token ONLY in the Authorization header — leak-guard), and the tolerant
// undefined-on-any-miss behavior the open flow relies on for its graceful fallback.
import { describe, it, expect, vi } from "vitest";
import { DiffContentClient } from "./diffContentClient";

const TOKEN = "tok-MUST-NOT-LEAK";
const tokenProvider = { getToken: () => Promise.resolve<string | undefined>(TOKEN) };

/** A minimal Response stand-in for the stubbed fetch. */
function jsonResponse(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: () => Promise.resolve(body) } as unknown as Response;
}

describe("DiffContentClient.fetchFullDiff", () => {
  it("fetches the full diff with the bearer token in the Authorization header ONLY", async () => {
    const fetchSpy = vi
      .fn<(url: string, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValue(
        jsonResponse({
          project_id: "p",
          task_id: "t",
          base: "develop",
          branch: "conductor/t",
          patch: "FULL PATCH",
          truncated: true,
        }),
      );
    const c = new DiffContentClient("http://gw.test", tokenProvider, fetchSpy as unknown as typeof fetch);

    const got = await c.fetchFullDiff("p", "t");

    expect(got).toEqual({ patch: "FULL PATCH", base: "develop", branch: "conductor/t", truncated: true });
    const [url, init] = fetchSpy.mock.calls[0]!;
    expect(url).toBe("http://gw.test/projects/p/tasks/t/diff");
    expect(init?.method).toBe("GET");
    expect(init?.headers).toEqual({ Authorization: `Bearer ${TOKEN}` });
    // LEAK GUARD: the token appears NOWHERE but the Authorization header.
    expect(JSON.stringify(got)).not.toContain(TOKEN);
    expect(String(url)).not.toContain(TOKEN);
  });

  it("URL-encodes the project + task path segments", async () => {
    const fetchSpy = vi
      .fn<(url: string, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValue(jsonResponse({ patch: "x" }));
    const c = new DiffContentClient("http://gw.test", tokenProvider, fetchSpy as unknown as typeof fetch);
    await c.fetchFullDiff("p/1", "t 2");
    expect(fetchSpy.mock.calls[0]![0]).toBe("http://gw.test/projects/p%2F1/tasks/t%202/diff");
  });

  it("returns undefined with no stored token (and never fetches)", async () => {
    const fetchSpy = vi.fn();
    const c = new DiffContentClient(
      "http://gw.test",
      { getToken: () => Promise.resolve<string | undefined>(undefined) },
      fetchSpy as unknown as typeof fetch,
    );
    expect(await c.fetchFullDiff("p", "t")).toBeUndefined();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("returns undefined on a non-2xx (404 = none persisted → caller falls back to the bounded patch)", async () => {
    const c = new DiffContentClient(
      "http://gw.test",
      tokenProvider,
      (() => Promise.resolve(jsonResponse({}, false, 404))) as unknown as typeof fetch,
    );
    expect(await c.fetchFullDiff("p", "t")).toBeUndefined();
  });

  it("returns undefined on a network throw", async () => {
    const c = new DiffContentClient(
      "http://gw.test",
      tokenProvider,
      (() => Promise.reject(new Error("net"))) as unknown as typeof fetch,
    );
    expect(await c.fetchFullDiff("p", "t")).toBeUndefined();
  });

  it("returns undefined on a JSON parse error", async () => {
    const c = new DiffContentClient(
      "http://gw.test",
      tokenProvider,
      (() =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.reject(new Error("bad json")),
        } as unknown as Response)) as unknown as typeof fetch,
    );
    expect(await c.fetchFullDiff("p", "t")).toBeUndefined();
  });

  it("returns undefined when the patch is empty (nothing full to render → fall back)", async () => {
    const c = new DiffContentClient(
      "http://gw.test",
      tokenProvider,
      (() => Promise.resolve(jsonResponse({ patch: "" }))) as unknown as typeof fetch,
    );
    expect(await c.fetchFullDiff("p", "t")).toBeUndefined();
  });
});
