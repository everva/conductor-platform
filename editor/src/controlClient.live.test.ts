// OPT-IN live integration test for the host-side ControlClient approve→merge flow (Faz-4
// 4E-2, gate "G4"). The editor's inline `conductor.approve` command drives
// ControlClient.approve() against the gateway; the gateway flips the held task to approved
// and the daemon (sharing the same -dsn Postgres) merges it on its next tick
// (Outcome "approved-merged"). THIS drives the REAL ControlClient with the REAL global
// fetch against a RUNNING stack and observes the task leave "awaiting-approval" — the
// committed counterpart to the 4E-2 G4 manual check (closing-review Lens-3). Deterministic
// unit coverage (fake fetch, every status mapping) lives in controlClient.test.ts.
//
// HARD GUARDS (the default `npm test` gate MUST stay deterministic + offline):
//   - env guard: SKIPPED unless CP_LIVE_GATEWAY (http(s):// or ws(s)://, normalized to
//     http), CP_LIVE_TOKEN, and CP_LIVE_PROJECT are ALL set. Collected-but-skipped in a
//     normal run (no fetch).
//   - PRECONDITION (operator sets up, like the diffObserver live test's "trigger a diff"):
//     CP_LIVE_PROJECT has EXACTLY ONE task parked in "awaiting-approval" (a held T3/T4),
//     and the conductor daemon is running over the shared DSN so it merges after approval.
//   - TOKEN DISCIPLINE: the token rides only in ControlClient's Authorization header; the
//     test asserts it never appears in the returned ControlResult.
//
// Run it explicitly (after standing up the live stack — see editor/README "Live e2e check"):
//
//	CP_LIVE_GATEWAY=http://localhost:8080 CP_LIVE_TOKEN=<token> CP_LIVE_PROJECT=<id> \
//	  npx vitest run src/controlClient.live.test.ts
import { describe, expect, it } from "vitest";

import { ControlClient } from "./controlClient";

const RAW = process.env.CP_LIVE_GATEWAY;
const TOKEN = process.env.CP_LIVE_TOKEN;
const PROJECT = process.env.CP_LIVE_PROJECT;
const HTTP_BASE = RAW ? RAW.replace(/^ws(s?):\/\//, "http$1://") : undefined;

const AWAITING = "awaiting-approval";

interface LiveTask {
  id: string;
  status: string;
  approved: boolean;
}

/** Authed GET of a project's tasks (the gateway read the editor's quick-pick doesn't use,
 * but the test needs to observe the held→merged transition). Token only in the header. */
async function fetchTasks(projectId: string): Promise<LiveTask[]> {
  const res = await fetch(`${HTTP_BASE}/projects/${encodeURIComponent(projectId)}/tasks`, {
    headers: { Authorization: `Bearer ${TOKEN}` },
  });
  if (!res.ok) {
    throw new Error(`GET tasks failed: ${res.status}`);
  }
  return (await res.json()) as LiveTask[];
}

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

describe.skipIf(!HTTP_BASE || !TOKEN || !PROJECT)(
  "ControlClient live approve→merge (G4, CP_LIVE_GATEWAY)",
  () => {
    it(
      "approves a held task and the daemon merges it (leaves awaiting-approval), token-free",
      async () => {
        // skipIf guarantees a non-empty token here, so the leak assertion below is meaningful.
        expect(TOKEN).toBeTruthy();

        // PRECONDITION: find the single held task. If none exists the live stack wasn't set
        // up as documented — fail loudly rather than pass vacuously.
        const before = await fetchTasks(PROJECT!);
        const held = before.filter((t) => t.status === AWAITING);
        expect(
          held.length,
          `expected exactly one ${AWAITING} task in ${PROJECT!}; set up the held T3/T4 first`,
        ).toBe(1);
        const heldId = held[0].id;

        const client = new ControlClient(HTTP_BASE!, {
          getToken: () => Promise.resolve(TOKEN),
        });

        // The editor's action: approve the held task.
        const result = await client.approve(PROJECT!, heldId);
        expect(result.ok, `approve result: ${JSON.stringify(result)}`).toBe(true);
        // TOKEN DISCIPLINE: the token never rides in the returned ControlResult.
        expect(JSON.stringify(result)).not.toContain(TOKEN);

        // The daemon merges on its next tick: poll until the task leaves awaiting-approval
        // (advanced/merged) or disappears from the active list.
        const deadline = Date.now() + 90_000;
        let merged = false;
        while (Date.now() < deadline) {
          const now = await fetchTasks(PROJECT!);
          const still = now.find((t) => t.id === heldId);
          if (still === undefined || still.status !== AWAITING) {
            merged = true;
            break;
          }
          await sleep(2_000);
        }
        expect(merged, `task ${heldId} never left ${AWAITING} after approve`).toBe(true);
      },
      120_000,
    );
  },
);
