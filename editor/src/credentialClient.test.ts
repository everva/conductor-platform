// Deterministic, headless tests for the host-side authed credential client (L3, ADR-0049).
// `fetch` is injected, so these run offline. They lock the gateway credential contract
// (cmd/conductor-api/agent.go): PUT/DELETE /agent/credentials/{kind} with "Authorization:
// Bearer <gateway-token>"; PUT body {token}; 2xx → ok, 401 → unauthorized, 501/503 →
// not-configured, other/throw → unreachable, no token → not-connected. The leak guard proves
// BOTH secrets (the gateway bearer + the uploaded claude token) ride only in the header/body
// and never appear in a returned value.
import { describe, expect, it, vi } from "vitest";

import { CredentialClient } from "./credentialClient";

const BASE = "http://gw.test";
const GW_SENTINEL = "gw-tok-SENTINEL-do-not-leak";
const CLAUDE_SENTINEL = "sk-ant-oat-SENTINEL-do-not-leak";
const KIND = "CLAUDE_CODE_OAUTH_TOKEN";

function tokenProvider() {
  return { getToken: () => Promise.resolve<string | undefined>(GW_SENTINEL) };
}
function noTokenProvider() {
  return { getToken: () => Promise.resolve<string | undefined>(undefined) };
}

function fetchMock(impl: (url: string, init: RequestInit | undefined) => Response | Promise<Response>) {
  return vi.fn((input: string | URL | Request, init?: RequestInit) =>
    Promise.resolve(impl(String(input), init)),
  ) as unknown as typeof fetch;
}
function authOf(init: RequestInit | undefined): string | undefined {
  const headers = (init?.headers ?? {}) as Record<string, string>;
  return headers.Authorization;
}

describe("CredentialClient.upload", () => {
  it("PUTs the kind with the bearer header + the token in the body; 204 → ok", async () => {
    let seenUrl = "";
    let seenInit: RequestInit | undefined;
    const fn = fetchMock((url, init) => {
      seenUrl = url;
      seenInit = init;
      return new Response(null, { status: 204 });
    });
    const client = new CredentialClient(BASE, tokenProvider(), fn);

    const res = await client.upload(KIND, CLAUDE_SENTINEL);

    expect(res).toEqual({ ok: true });
    expect(seenUrl).toBe(`${BASE}/agent/credentials/${KIND}`);
    expect(seenInit?.method).toBe("PUT");
    expect(authOf(seenInit)).toBe(`Bearer ${GW_SENTINEL}`);
    // The claude token rides in the body only.
    expect(seenInit?.body).toBe(JSON.stringify({ token: CLAUDE_SENTINEL }));
  });

  it("maps 401 → unauthorized, 503/501 → not-configured, other → unreachable", async () => {
    const cases: Array<[number, string]> = [
      [401, "unauthorized"],
      [503, "not-configured"],
      [501, "not-configured"],
      [500, "unreachable"],
    ];
    for (const [status, reason] of cases) {
      const client = new CredentialClient(BASE, tokenProvider(), fetchMock(() => new Response(null, { status })));
      const res = await client.upload(KIND, CLAUDE_SENTINEL);
      expect(res).toEqual({ ok: false, reason, status });
    }
  });

  it("no gateway token → not-connected (no request sent)", async () => {
    const fn = fetchMock(() => new Response(null, { status: 204 }));
    const client = new CredentialClient(BASE, noTokenProvider(), fn);
    const res = await client.upload(KIND, CLAUDE_SENTINEL);
    expect(res).toEqual({ ok: false, reason: "not-connected", status: 0 });
    expect(fn).not.toHaveBeenCalled();
  });

  it("a thrown fetch → unreachable", async () => {
    const fn = (() => Promise.reject(new Error("network"))) as unknown as typeof fetch;
    const client = new CredentialClient(BASE, tokenProvider(), fn);
    const res = await client.upload(KIND, CLAUDE_SENTINEL);
    expect(res).toEqual({ ok: false, reason: "unreachable", status: 0 });
  });
});

describe("CredentialClient.remove", () => {
  it("DELETEs the kind with the bearer header (no body); 204 → ok", async () => {
    let seenInit: RequestInit | undefined;
    const fn = fetchMock((_url, init) => {
      seenInit = init;
      return new Response(null, { status: 204 });
    });
    const client = new CredentialClient(BASE, tokenProvider(), fn);
    const res = await client.remove(KIND);
    expect(res).toEqual({ ok: true });
    expect(seenInit?.method).toBe("DELETE");
    expect(authOf(seenInit)).toBe(`Bearer ${GW_SENTINEL}`);
    expect(seenInit?.body).toBeUndefined();
  });
});

describe("token-leak guard", () => {
  it("neither the gateway bearer nor the uploaded claude token appears in any returned value", async () => {
    const results: unknown[] = [];
    // success + every failure mapping
    for (const status of [204, 401, 503, 500]) {
      const client = new CredentialClient(BASE, tokenProvider(), fetchMock(() => new Response(null, { status })));
      results.push(await client.upload(KIND, CLAUDE_SENTINEL));
      results.push(await client.remove(KIND));
    }
    const noTok = new CredentialClient(BASE, noTokenProvider(), fetchMock(() => new Response(null, { status: 204 })));
    results.push(await noTok.upload(KIND, CLAUDE_SENTINEL));

    const json = JSON.stringify(results);
    expect(json).not.toContain(GW_SENTINEL);
    expect(json).not.toContain(CLAUDE_SENTINEL);
  });
});
