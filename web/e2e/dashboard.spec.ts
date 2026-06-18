// Dashboard e2e (3B-1): with the gateway fully MOCKED via page.route (REST stubs +
// a stubbed /ws so no real socket is needed), sign in through the TokenGate and
// assert the fleet dashboard renders the projects and hosts. Deterministic and
// offline — no running gateway required.
import { expect, test } from "@playwright/test";

const STATUS = {
  projects: 1,
  hosts: 1,
  leases: [
    { project_id: "p1", host_id: "h1", task_id: "t1", acquired_at: "2026-06-18T00:00:00Z" },
  ],
  generated_at: "2026-06-18T00:00:00Z",
};

const PROJECTS = [
  {
    id: "p1",
    repo: "git@example.com:p1.git",
    base_branch: "main",
    host_id: "h1",
    readiness: "ready",
    recipe_pointer: "r1",
    governance_policy: "auto",
    paused: true,
  },
];

const HOSTS = [
  {
    id: "h1",
    capabilities: ["go", "node"],
    last_heartbeat: "2026-06-18T00:00:00Z",
    heartbeat_age_seconds: 7,
  },
];

const TASKS = [
  {
    id: "t1",
    project_id: "p1",
    lane: "build",
    tier: "core",
    status: "running",
    requires: [],
    deps: [],
    branch: "feat/t1",
    scenario_id: "s1",
    retry_count: 0,
    abort_requested: false,
    approved: true,
  },
];

function json(body: unknown) {
  return {
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  };
}

test("renders the fleet dashboard with mocked gateway", async ({ page }) => {
  // Stub /ws so the browser never opens a real socket (route the upgrade GET).
  await page.route("**/ws*", (route) => route.fulfill({ status: 200, body: "" }));
  await page.route("**/status", (route) => route.fulfill(json(STATUS)));
  await page.route("**/hosts", (route) => route.fulfill(json(HOSTS)));
  // Register the bare-/projects route first, then the more specific tasks route:
  // Playwright matches the LAST-registered matching route first, so the tasks
  // route must win over the **/projects glob for /projects/<id>/tasks.
  await page.route("**/projects", (route) => route.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (route) => route.fulfill(json(TASKS)));

  await page.goto("/");

  // Sign in through the token gate (status() probe is mocked → succeeds).
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();

  // Fleet panels render with the mocked data.
  await expect(page.getByRole("region", { name: /fleet status/i })).toBeVisible();
  await expect(page.getByRole("cell", { name: "p1" }).first()).toBeVisible();
  await expect(page.getByText("paused")).toBeVisible();
  await expect(page.getByRole("cell", { name: "h1" }).first()).toBeVisible();
  await expect(page.getByText("go", { exact: true })).toBeVisible();

  // Selecting the project shows its task with the approved badge.
  await page.getByRole("cell", { name: "p1" }).first().click();
  await expect(page.getByRole("cell", { name: "t1", exact: true })).toBeVisible();
  await expect(page.getByText("approved")).toBeVisible();
});

const EVENTS = [
  {
    id: "ev-1",
    ts: "2026-06-18T00:00:01.000Z",
    project: "p1",
    task: "t1",
    phase: "develop",
    kind: "progress",
    payload: { msg: "building" },
  },
  {
    id: "ev-2",
    ts: "2026-06-18T00:00:02.000Z",
    project: "p1",
    task: "t1",
    phase: "review",
    kind: "intervention-needed",
    payload: { reason: "human gate" },
  },
];

test("renders the event stream tab with backfilled history (3B-2)", async ({ page }) => {
  await page.route("**/ws*", (route) => route.fulfill({ status: 200, body: "" }));
  await page.route("**/status", (route) => route.fulfill(json(STATUS)));
  await page.route("**/hosts", (route) => route.fulfill(json(HOSTS)));
  // /events must be registered AFTER the bare /projects glob would not match it;
  // it is its own path so order vs projects/tasks is irrelevant here.
  await page.route("**/events*", (route) => route.fulfill(json(EVENTS)));
  await page.route("**/projects", (route) => route.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (route) => route.fulfill(json(TASKS)));

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /fleet status/i })).toBeVisible();

  // Switch to the Events tab; the backfilled history renders newest-first with the
  // intervention row highlighted (marker visible).
  await page.getByRole("tab", { name: /events/i }).click();
  await expect(page.getByRole("region", { name: /event stream/i })).toBeVisible();
  await expect(page.getByLabel(/intervention needed/i)).toBeVisible();
  await expect(page.getByText('{"msg":"building"}')).toBeVisible();
  // The pause toggle is present (its display-freeze behavior is covered exhaustively
  // by the deterministic vitest suite; here we only assert it renders in-browser).
  await expect(page.getByRole("button", { name: /^pause$/i })).toBeVisible();
});
