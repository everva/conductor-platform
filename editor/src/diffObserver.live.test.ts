// OPT-IN live integration test for the host-side DiffObserver (Faz-4 4E-2): it drives
// the REAL DiffObserver over the REAL `ws` wsConnector against a RUNNING gateway, proving
// the editor receives a live `KindDiff` frame end to end (daemon → PG bus → gateway /ws →
// DiffObserver) and distills it token-free. The deterministic unit coverage (fake connector,
// fake frames) lives in diffObserver.test.ts; THIS is the live-wire reality check the
// crafted frames can't give.
//
// HARD GUARDS (the default `npm test` gate MUST stay deterministic + offline):
//   - env guard: SKIPPED unless BOTH CP_LIVE_GATEWAY (ws base url, e.g. ws://localhost:8080)
//     and CP_LIVE_TOKEN are set. Collected-but-skipped in a normal run (no socket opened).
//   - it only READS the event stream; the token rides ONLY in the WS `?token=` query (the
//     DiffObserver's own discipline) and is asserted to never appear in the distilled diff.
//
// Run it explicitly (after standing up the live stack — see editor/README "Live e2e check"):
//
//	CP_LIVE_GATEWAY=ws://localhost:8080 CP_LIVE_TOKEN=<token> \
//	  npx vitest run src/diffObserver.live.test.ts
//
// then, within the wait window, trigger a green-gate diff on the gateway's bus (a daemon
// tick over the shared -dsn Postgres). The test resolves on the first `diff` frame.
import { describe, expect, it } from "vitest";

import { DiffObserver, type TaskDiff } from "./diffObserver";
import { wsConnector } from "./bridge/wsConnector";

const GATEWAY = process.env.CP_LIVE_GATEWAY; // ws base url, e.g. ws://localhost:8080
const TOKEN = process.env.CP_LIVE_TOKEN;

// The sentinel the leak-guard checks: the real token must NEVER appear in the distilled,
// user-facing TaskDiff (it rides only in the WS URL query the DiffObserver builds).
describe.skipIf(!GATEWAY || !TOKEN)("DiffObserver live integration (CP_LIVE_GATEWAY)", () => {
  it(
    "receives a live KindDiff from the running gateway and distills it token-free",
    async () => {
      // skipIf guarantees a non-empty token here, so the leak assertion below is meaningful.
      expect(TOKEN).toBeTruthy();
      let observer: DiffObserver | undefined;
      const firstDiff = new Promise<TaskDiff>((resolve) => {
        observer = new DiffObserver({
          wsBaseUrl: GATEWAY!,
          tokenProvider: { getToken: () => Promise.resolve(TOKEN) },
          wsConnector,
          onDiff: (d) => resolve(d),
        });
        void observer.start();
      });

      try {
        const diff = await firstDiff;

        // The diff is REAL and locatable.
        expect(diff.task).toBeTruthy();
        expect(diff.project).toBeTruthy();
        // It carries a real, non-empty unified patch...
        expect(diff.patch.length).toBeGreaterThan(0);
        // ...and lists the changed file the live performer added.
        expect(diff.files.length).toBeGreaterThan(0);
        expect(diff.files.some((f) => f.path.endsWith("greeting.go"))).toBe(true);
        // TOKEN DISCIPLINE: the bearer token never leaks into the user-facing diff.
        expect(JSON.stringify(diff)).not.toContain(TOKEN);
      } finally {
        observer?.stop();
      }
    },
    95_000,
  );
});
