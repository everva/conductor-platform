// Command Center board e2e (redesign E1): with the gateway MOCKED (REST stubs +
// a silent /ws), sign in and assert the agent-native board IS the default surface
// and buckets tasks across projects/hosts into the four lifecycle columns — the
// director's "Needs Review" lane (awaiting-approval + blocked) with a confirm-gated
// Approve, a leased task under Running with its host, the top-strip counts, and a
// card click focusing its project (the E1 drill-in). Deterministic + offline.
import { expect, test, type Page } from "@playwright/test";

async function mockWebSocket(page: Page) {
  // Accept the socket client-side and stay silent (no real gateway, no churn).
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});
}

const STATUS = {
  projects: 2,
  hosts: 2,
  // W-run holds an active lease on host-linux → the live "running on host" signal.
  leases: [
    { project_id: "web-shop", host_id: "host-linux", task_id: "W-run", acquired_at: "2026-06-20T00:00:00Z" },
  ],
  generated_at: "2026-06-20T00:00:00Z",
};

const PROJECTS = [
  { id: "web-shop", repo: "git@x:web-shop.git", base_branch: "develop", host_id: "", readiness: "ready", recipe_pointer: "", governance_policy: "auto", paused: false },
  { id: "ios-app", repo: "git@x:ios-app.git", base_branch: "develop", host_id: "", readiness: "ready", recipe_pointer: "", governance_policy: "auto", paused: false },
];

const HOSTS = [
  { id: "host-linux", capabilities: ["linux", "web", "backend"], heartbeat_age_seconds: 2 },
  { id: "host-mac", capabilities: ["macos", "ios-build", "maestro"], heartbeat_age_seconds: 3 },
];

function task(id: string, project_id: string, status: string) {
  return {
    id, project_id, lane: project_id === "ios-app" ? "ios" : "web", tier: "T2",
    status, requires: [], deps: [], branch: "", scenario_id: id,
    retry_count: 0, abort_requested: false, approved: false,
  };
}

const TASKS_BY_PROJECT: Record<string, ReturnType<typeof task>[]> = {
  "web-shop": [task("W-ready", "web-shop", "ready"), task("W-run", "web-shop", "running"), task("W-done", "web-shop", "done")],
  "ios-app": [task("I-await", "ios-app", "awaiting-approval"), task("I-block", "ios-app", "blocked")],
};

function json(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

async function signInToBoard(page: Page) {
  await mockWebSocket(page);
  await page.route("**/status", (route) => route.fulfill(json(STATUS)));
  await page.route("**/hosts", (route) => route.fulfill(json(HOSTS)));
  await page.route("**/projects", (route) => route.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (route) => {
    const pid = route.request().url().match(/\/projects\/([^/]+)\/tasks/)?.[1] ?? "";
    return route.fulfill(json(TASKS_BY_PROJECT[pid] ?? []));
  });
  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  // The board is the DEFAULT surface: its region appears with no tab click.
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();
}

const column = (page: Page, label: string) =>
  page.getByRole("listitem", { name: new RegExp(`^${label} `) });

test("board is the default surface and buckets tasks into the four lifecycle columns", async ({ page }) => {
  await signInToBoard(page);

  // Each task lands in its lifecycle column.
  await expect(column(page, "Ready").getByText("W-ready")).toBeVisible();
  await expect(column(page, "Running").getByText("W-run")).toBeVisible();
  await expect(column(page, "Needs Review").getByText("I-await")).toBeVisible();
  await expect(column(page, "Needs Review").getByText("I-block")).toBeVisible();
  await expect(column(page, "Done").getByText("W-done")).toBeVisible();

  // The leased task shows the host actually running it.
  await expect(column(page, "Running").getByText(/host-linux/)).toBeVisible();

  // Top strip counts the director scans first.
  await expect(page.locator(".cc-stat", { hasText: "running" })).toContainText("1");
  await expect(page.locator(".cc-stat", { hasText: "need your review" })).toContainText("2");
  await expect(page.locator(".cc-stat", { hasText: "blocked" })).toContainText("1");

  // Save a screenshot artifact of the live board.
  await page.screenshot({ path: "test-results/command-center-board.png", fullPage: true });
});

test("awaiting-approval card exposes Approve; a card click focuses its project (E1 drill-in)", async ({ page }) => {
  await signInToBoard(page);

  // The held card offers Approve (confirm-gated, same path as TasksView; the full
  // confirm→POST→created-ids leg is covered deterministically by the vitest suite).
  const review = column(page, "Needs Review");
  await expect(review.getByRole("button", { name: "Approve" })).toBeVisible();

  // Clicking a card body focuses its project — the E1 drill-in switches to the Fleet
  // view for that project (E2 replaces this with the full session view).
  await review.getByText("I-await").click();
  await expect(page.getByRole("tab", { name: /^fleet$/i })).toHaveAttribute("aria-selected", "true");
});
