// Deterministic, headless tests for the host-side fleet READ client (N2). Mirrors
// controlClient.test.ts: a stubbed `fetch` drives it with no network, and a sentinel-token
// leak guard proves the bearer rides ONLY in the Authorization header.
import { describe, expect, it } from "vitest";
import { FleetReadClient } from "./fleetReadClient";

const SENTINEL = "sk-sentinel-FLEET-READ-TOKEN-must-not-leak";

function makeClient(opts: {
  token?: string | undefined;
  responder: (url: string, init: RequestInit) => Response | Promise<Response>;
}): { client: FleetReadClient; calls: { url: string; init: RequestInit }[] } {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchImpl = (async (input: string | URL, init?: RequestInit) => {
    const url = String(input);
    const i = init ?? {};
    calls.push({ url, init: i });
    return opts.responder(url, i);
  }) as unknown as typeof fetch;
  const client = new FleetReadClient(
    "https://gw.test",
    { getToken: () => Promise.resolve(opts.token) },
    fetchImpl,
  );
  return { client, calls };
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("FleetReadClient.listProjects", () => {
  it("GETs /projects with the bearer header and maps the tolerant project projection", async () => {
    const { client, calls } = makeClient({
      token: SENTINEL,
      responder: () =>
        json(200, [
          { id: "web-shop", repo: "git@x:web-shop.git", readiness: "ready", paused: false, extra: 1 },
          { id: "api", readiness: "blocked", paused: true },
          { repo: "no-id" }, // dropped (no string id)
          null, // dropped
        ]),
    });
    const projects = await client.listProjects();
    expect(projects).toEqual([
      { id: "web-shop", readiness: "ready", paused: false },
      { id: "api", readiness: "blocked", paused: true },
    ]);
    expect(calls[0]?.url).toBe("https://gw.test/projects");
    expect((calls[0]?.init.headers as Record<string, string>)["Authorization"]).toBe(
      `Bearer ${SENTINEL}`,
    );
  });

  it("returns [] with NO request when there is no token", async () => {
    const { client, calls } = makeClient({ token: undefined, responder: () => json(200, []) });
    expect(await client.listProjects()).toEqual([]);
    expect(calls).toHaveLength(0);
  });

  it("returns [] on a non-2xx (e.g. 401) without throwing", async () => {
    const { client } = makeClient({
      token: SENTINEL,
      responder: () => json(401, { error: "unauthorized" }),
    });
    expect(await client.listProjects()).toEqual([]);
  });

  it("returns [] on a network throw without propagating it", async () => {
    const { client } = makeClient({
      token: SENTINEL,
      responder: () => {
        throw new Error("ECONNREFUSED");
      },
    });
    expect(await client.listProjects()).toEqual([]);
  });

  it("returns [] on a non-array / malformed body", async () => {
    const { client } = makeClient({ token: SENTINEL, responder: () => json(200, { nope: true }) });
    expect(await client.listProjects()).toEqual([]);
  });
});

describe("FleetReadClient.listTasks", () => {
  it("GETs /projects/{id}/tasks (encoded) and maps the tolerant task projection", async () => {
    const { client, calls } = makeClient({
      token: SENTINEL,
      responder: () =>
        json(200, [
          {
            id: "W-1",
            project_id: "web shop",
            lane: "web",
            tier: "T3",
            status: "awaiting-approval",
            extra: true,
          },
          { id: "W-2", lane: "web", tier: "T1", status: "done" },
          { project_id: "x" }, // dropped (no id)
        ]),
    });
    const tasks = await client.listTasks("web shop");
    expect(tasks).toEqual([
      { id: "W-1", projectId: "web shop", lane: "web", tier: "T3", status: "awaiting-approval" },
      { id: "W-2", projectId: "", lane: "web", tier: "T1", status: "done" },
    ]);
    expect(calls[0]?.url).toBe("https://gw.test/projects/web%20shop/tasks");
  });
});

describe("FleetReadClient token discipline (leak guard)", () => {
  it("the bearer rides ONLY in the Authorization header — never in a returned value or the URL", async () => {
    const { client, calls } = makeClient({
      token: SENTINEL,
      responder: () => json(200, [{ id: "p", readiness: "ready", paused: false }]),
    });
    const projects = await client.listProjects();
    // Returned data carries no token.
    expect(JSON.stringify(projects)).not.toContain(SENTINEL);
    // The token's only egress is the Authorization header (present there)…
    const headers = JSON.stringify(calls.map((c) => c.init.headers));
    expect(headers).toContain(SENTINEL);
    // …and NOT in the request URL.
    expect(calls[0]?.url ?? "").not.toContain(SENTINEL);
  });
});
