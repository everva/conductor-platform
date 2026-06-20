// Session view e2e (redesign E2): with the gateway MOCKED, sign in, drill from a
// board card into the task's SESSION, and assert the agent-native detail renders —
// the SPEC (acceptance), the deterministic Verifier verdict (per-gate checks +
// MERGE-READY), the diff, the activity timeline, a confirm-gated Approve — and that
// the back button returns to the board. Deterministic + offline.
import { expect, test, type Page } from "@playwright/test";

async function mockWebSocket(page: Page) {
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});
}

const STATUS = { projects: 1, hosts: 1, leases: [], generated_at: "2026-06-20T00:00:00Z" };
const PROJECTS = [
  { id: "web-shop", repo: "git@x:web-shop.git", base_branch: "develop", host_id: "", readiness: "ready", recipe_pointer: "", governance_policy: "auto", paused: false },
];
const HOSTS = [{ id: "host-linux", capabilities: ["linux", "web"], heartbeat_age_seconds: 2 }];
const TASKS = [
  { id: "W-1", project_id: "web-shop", lane: "web", tier: "T3", status: "awaiting-approval", requires: [], deps: [], branch: "conductor/W-1", scenario_id: "S-1", retry_count: 0, abort_requested: false, approved: false },
];
const SCENARIOS = [
  { id: "S-1", title: "Ship the Feature helper", lane: "web", tier: "T3", deps: [], acceptance: ["package app exposes Feature()", "hidden holdout green"], hidden_holdout_ref: "store://holdouts/S-1/h.go" },
];
const EVENTS = [
  { id: "e1", ts: "2026-06-20T10:00:00Z", project: "web-shop", task: "W-1", phase: "develop", kind: "started", payload: {} },
  { id: "e2", ts: "2026-06-20T10:04:00Z", project: "web-shop", task: "W-1", phase: "verify", kind: "started", payload: {} },
  { id: "e3", ts: "2026-06-20T10:05:00Z", project: "web-shop", task: "W-1", phase: "review", kind: "decision",
    payload: { result: "pass", checks: [{ name: "go build", result: "pass", evidence: "exit 0" }, { name: "hidden holdout", result: "pass", evidence: "exit 0" }] } },
  { id: "e4", ts: "2026-06-20T10:05:01Z", project: "web-shop", task: "W-1", phase: "review", kind: "diff",
    payload: { branch: "conductor/W-1", base: "develop", truncated: false, patch: "@@ -0,0 +1 @@\n+func Feature() string { return \"shipped\" }\n", files: [{ path: "feature.go", status: "A", additions: 6, deletions: 0 }] } },
];

function json(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

test("drill from the board into a task session: spec + verdict + diff + timeline", async ({ page }) => {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json(HOSTS)));
  await page.route("**/events*", (r) => r.fulfill(json(EVENTS)));
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json(SCENARIOS)));
  await page.route("**/projects/*/tasks", (r) => r.fulfill(json(TASKS)));
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();

  // Drill into W-1's card (it sits in Needs Review, awaiting-approval).
  await page.getByText("W-1").click();

  // SESSION view: spec (acceptance), Verifier verdict (per-gate checks + MERGE-READY),
  // diff, and the activity timeline — all from the mocked gateway.
  const session = page.getByRole("region", { name: /^session$/i });
  await expect(session).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ship the Feature helper" })).toBeVisible();
  await expect(page.getByText("package app exposes Feature()")).toBeVisible();
  await expect(page.getByText("go build")).toBeVisible();
  await expect(page.getByText(/MERGE-READY/)).toBeVisible();
  await expect(page.getByText("feature.go")).toBeVisible();
  await expect(page.getByText("Review · verdict")).toBeVisible();
  await expect(page.getByRole("button", { name: /approve & merge/i })).toBeVisible();

  // Back returns to the board.
  await page.getByRole("button", { name: /command center/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();
});
