// OPT-IN live integration test for the host-side HostBridge distill proxy (Faz-4 4E-2,
// gate "G3"). The webview-facing distill flow ("yazışarak senaryo üret") never holds the
// token: the cockpit asks the HOST (over postMessage) to POST /projects/{id}/distill, and
// the host attaches auth and performs the real call. THIS drives the REAL HostBridge with
// the REAL global fetch against a RUNNING gateway whose distiller is the real `claude -p`,
// proving the editor draws PROPOSED scenarios end to end — the committed counterpart to the
// 4E-2 G3 manual check (closing-review Lens-3). Deterministic unit coverage (fake fetch)
// lives in bridge/hostBridge.test.ts; THIS is the live-wire reality check.
//
// HARD GUARDS (the default `npm test` gate MUST stay deterministic + offline):
//   - env guard: SKIPPED unless BOTH CP_LIVE_GATEWAY (gateway base url; http(s):// or
//     ws(s)://, normalized to http here) and CP_LIVE_TOKEN are set. Collected-but-skipped
//     in a normal run (no socket / no fetch).
//   - PRECONDITION: the gateway must have a real distiller configured (the production
//     `claude -p` path) and at least one onboarded project. The test discovers a project
//     via GET /projects; it persists NOTHING (distill drafts only).
//   - TOKEN DISCIPLINE: the token rides only in the host's Authorization header; the test
//     asserts it never appears in ANY message the host posts back to the (fake) webview.
//
// Run it explicitly (after standing up the live stack — see editor/README "Live e2e check"):
//
//	CP_LIVE_GATEWAY=http://localhost:8080 CP_LIVE_TOKEN=<token> \
//	  npx vitest run src/hostBridge.live.test.ts
import { describe, expect, it } from "vitest";

import { HostBridge } from "./bridge/hostBridge";
import type { HostMessage } from "./bridge/protocol";

const RAW = process.env.CP_LIVE_GATEWAY;
const TOKEN = process.env.CP_LIVE_TOKEN;
// Accept the same env value the WS live test uses (ws://…) by normalizing scheme to http.
const HTTP_BASE = RAW ? RAW.replace(/^ws(s?):\/\//, "http$1://") : undefined;
const WS_BASE = RAW ? RAW.replace(/^http(s?):\/\//, "ws$1://") : undefined;

/** A WebviewLike fake that records every posted HostMessage and lets the test await the
 * reply for a given request id. Mirrors the real webview seam the HostBridge talks to. */
class FakeWebview {
  readonly posted: HostMessage[] = [];
  #listener: ((m: unknown) => void) | undefined;
  readonly #waiters: { id: string; resolve: (m: HostMessage) => void }[] = [];

  postMessage(message: unknown): void {
    const msg = message as HostMessage;
    this.posted.push(msg);
    for (let i = this.#waiters.length - 1; i >= 0; i--) {
      const w = this.#waiters[i];
      if ("id" in msg && msg.id === w.id) {
        w.resolve(msg);
        this.#waiters.splice(i, 1);
      }
    }
  }
  onDidReceiveMessage(listener: (m: unknown) => void): { dispose(): void } {
    this.#listener = listener;
    return { dispose: () => undefined };
  }
  fire(message: unknown): void {
    this.#listener?.(message);
  }
  reply(id: string): Promise<HostMessage> {
    return new Promise((resolve) => this.#waiters.push({ id, resolve }));
  }
}

describe.skipIf(!HTTP_BASE || !TOKEN)("HostBridge live distill (G3, CP_LIVE_GATEWAY)", () => {
  it(
    "proxies POST /distill to the running gateway and drafts scenarios token-free",
    async () => {
      // skipIf guarantees a non-empty token here, so the leak assertion below is meaningful.
      expect(TOKEN).toBeTruthy();

      // Discover a real project to distill against (distill existence-checks the project).
      const listRes = await fetch(`${HTTP_BASE}/projects`, {
        headers: { Authorization: `Bearer ${TOKEN}` },
      });
      expect(listRes.ok).toBe(true);
      const projects = (await listRes.json()) as Array<{ id: string }>;
      expect(Array.isArray(projects) && projects.length > 0).toBe(true);
      const projectId = projects[0].id;

      const webview = new FakeWebview();
      const bridge = new HostBridge({
        webview,
        baseUrl: HTTP_BASE!,
        wsBaseUrl: WS_BASE!,
        tokenProvider: { getToken: () => Promise.resolve(TOKEN) },
        // REST-only test: the WS connector is never exercised.
        wsConnector: { open: () => ({ close: () => undefined }) },
        // fetchImpl defaults to the real global fetch.
      });
      bridge.attach();

      try {
        const id = "distill-live-1";
        const reply = webview.reply(id);
        const conversation =
          "We need a backend endpoint GET /health that returns HTTP 200 with body \"ok\". " +
          "Low risk, no auth. Add a test that asserts the 200 and the body.";
        webview.fire({
          kind: "rest-request",
          id,
          method: "POST",
          path: `/projects/${encodeURIComponent(projectId)}/distill`,
          body: JSON.stringify({ conversation }),
          contentType: "application/json",
        });

        const msg = await reply;
        // The host proxied it as a real response (not a token-free rest-error).
        expect(msg.kind).toBe("rest-response");
        if (msg.kind !== "rest-response") {
          throw new Error(`expected rest-response, got ${msg.kind}`);
        }
        expect(msg.status).toBe(200);

        // Real `claude -p` drafted at least one PROPOSED scenario + intake-ready YAML.
        const body = JSON.parse(msg.body) as { scenarios?: unknown; yaml?: unknown };
        expect(Array.isArray(body.scenarios)).toBe(true);
        expect((body.scenarios as unknown[]).length).toBeGreaterThan(0);
        expect(typeof body.yaml).toBe("string");
        expect((body.yaml as string).length).toBeGreaterThan(0);

        // TOKEN DISCIPLINE: nothing the host posts back to the webview carries the token.
        // Assert via a boolean so a failure message can NEVER echo the token itself.
        expect(JSON.stringify(webview.posted).includes(TOKEN!)).toBe(false);
      } finally {
        bridge.dispose();
      }
    },
    120_000,
  );
});
