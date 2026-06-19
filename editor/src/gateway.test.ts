// Deterministic, headless tests for the gateway HTTP helpers (4B-1). Global `fetch`
// is stubbed, so these run offline with no gateway. They lock the gateway contract
// (verified from cmd/conductor-api): /readyz is unauthenticated; /status is the authed
// token-validity probe (200 valid / 401 unauthorized). They also assert the token only
// ever rides in the Authorization header.
import { afterEach, describe, expect, it, vi } from "vitest";
import { deriveWsUrl, normalizeBaseUrl, pingReadyz, validateToken } from "./gateway";

afterEach(() => {
  vi.restoreAllMocks();
});

function stubFetch(
  impl: (url: string, init: RequestInit | undefined) => Response | Promise<Response>,
) {
  const fn = vi.fn((input: string | URL, init?: RequestInit) =>
    Promise.resolve(impl(String(input), init)),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("normalizeBaseUrl", () => {
  it("trims whitespace and strips trailing slashes", () => {
    expect(normalizeBaseUrl("  http://localhost:8080/  ")).toBe("http://localhost:8080");
    expect(normalizeBaseUrl("http://gw.test///")).toBe("http://gw.test");
    expect(normalizeBaseUrl("http://gw.test")).toBe("http://gw.test");
  });
});

describe("deriveWsUrl", () => {
  it("rewrites only the scheme: http→ws, https→wss; passthrough otherwise", () => {
    expect(deriveWsUrl("http://localhost:8080")).toBe("ws://localhost:8080");
    expect(deriveWsUrl("https://gw.test/")).toBe("wss://gw.test");
    expect(deriveWsUrl("ws://already")).toBe("ws://already");
  });
});

describe("pingReadyz", () => {
  it("maps 200 → ok and hits /readyz with no Authorization header", async () => {
    const fn = stubFetch(() => new Response(null, { status: 200 }));
    const res = await pingReadyz("http://gw.test/");
    expect(res).toEqual({ ok: true, status: 200 });
    const [url, init] = fn.mock.calls[0];
    expect(String(url)).toBe("http://gw.test/readyz");
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
  });

  it("maps 503 → not ok", async () => {
    stubFetch(() => new Response(null, { status: 503 }));
    expect(await pingReadyz("http://gw.test")).toEqual({ ok: false, status: 503 });
  });

  it("maps a network failure → { ok: false, status: 0 } (never throws)", async () => {
    stubFetch(() => {
      throw new Error("ECONNREFUSED");
    });
    expect(await pingReadyz("http://gw.test")).toEqual({ ok: false, status: 0 });
  });
});

describe("validateToken", () => {
  it("sends Authorization: Bearer <token> to /status and maps 200 → valid", async () => {
    const fn = stubFetch(() => new Response("{}", { status: 200 }));
    const check = await validateToken("http://gw.test", "tok-abc");
    expect(check).toBe("valid");
    const [url, init] = fn.mock.calls[0];
    expect(String(url)).toBe("http://gw.test/status");
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer tok-abc");
  });

  it("maps 401 → unauthorized", async () => {
    stubFetch(() => new Response(null, { status: 401 }));
    expect(await validateToken("http://gw.test", "bad")).toBe("unauthorized");
  });

  it("maps any other status (e.g. 500) → unreachable", async () => {
    stubFetch(() => new Response(null, { status: 500 }));
    expect(await validateToken("http://gw.test", "tok")).toBe("unreachable");
  });

  it("maps a network failure → unreachable (never throws)", async () => {
    stubFetch(() => {
      throw new Error("ECONNREFUSED");
    });
    expect(await validateToken("http://gw.test", "tok")).toBe("unreachable");
  });
});
