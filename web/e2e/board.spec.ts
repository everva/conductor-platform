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

function task(id: string, project_id: string, status: string, reason = "") {
  return {
    id, project_id, lane: project_id === "ios-app" ? "ios" : "web", tier: "T2",
    status, requires: [], deps: [], branch: "", scenario_id: id,
    retry_count: 0, abort_requested: false, approved: false, reason,
  };
}

const BLOCK_REASON = "agent run failed: engine: malformed verdict: no result-keyed JSON object in output";

const TASKS_BY_PROJECT: Record<string, ReturnType<typeof task>[]> = {
  "web-shop": [task("W-ready", "web-shop", "ready"), task("W-run", "web-shop", "running"), task("W-done", "web-shop", "done")],
  "ios-app": [task("I-await", "ios-app", "awaiting-approval"), task("I-block", "ios-app", "blocked", BLOCK_REASON)],
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
  // The session drill-in fetches these; empty is fine for the board-level tests.
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
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

  // A blocked card explains WHY it stalled (the agent's report summary), right on the board —
  // the director no longer has to dig through agent logs to learn the cause.
  await expect(column(page, "Needs Review").getByText(/malformed verdict/)).toBeVisible();

  // The leased task shows the host actually running it.
  await expect(column(page, "Running").getByText(/host-linux/)).toBeVisible();

  // Top strip counts the director scans first.
  await expect(page.locator(".cc-stat", { hasText: "running" })).toContainText("1");
  await expect(page.locator(".cc-stat", { hasText: "need your review" })).toContainText("2");
  await expect(page.locator(".cc-stat", { hasText: "blocked" })).toContainText("1");

  // Save a screenshot artifact of the live board.
  await page.screenshot({ path: "test-results/command-center-board.png", fullPage: true });
});

test("awaiting-approval card exposes Approve; a card click drills into its session", async ({ page }) => {
  await signInToBoard(page);

  // The held card offers Approve (confirm-gated, same path as TasksView; the full
  // confirm→POST→created-ids leg is covered deterministically by the vitest suite).
  const review = column(page, "Needs Review");
  await expect(review.getByRole("button", { name: "Approve" })).toBeVisible();

  // Clicking a card body drills into that task's session detail view (E2).
  await review.getByText("I-await").click();
  await expect(page.getByRole("region", { name: /^session$/i })).toBeVisible();
});

test("the shell's needs-review signal persists across surfaces and routes back to the board (E4)", async ({ page }) => {
  await signInToBoard(page);

  // The shell badge counts the Needs-Review lane (I-await + I-block = 2) and stays
  // visible from ANY surface — switch to Events and it persists.
  await page.getByRole("tab", { name: /^events$/i }).click();
  const badge = page.locator(".needs-review");
  await expect(badge).toContainText("2 tasks need your review");

  // One click routes back to the review queue (the board's Needs-Review lane).
  await badge.getByRole("button", { name: /review/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();
  await expect(column(page, "Needs Review").getByText("I-await")).toBeVisible();
});

test("multi-select held cards opens an enumerated bulk-approve confirm (E4)", async ({ page }) => {
  // A local seed with TWO gate-green (awaiting-approval) tasks so we can genuinely
  // multi-select (the shared seed has one); the per-item approve fan-out is covered
  // deterministically by the vitest suites.
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json({ projects: 1, hosts: 1, leases: [], generated_at: "2026-06-20T00:00:00Z" })));
  await page.route("**/hosts", (r) => r.fulfill(json(HOSTS)));
  await page.route("**/projects", (r) => r.fulfill(json([PROJECTS[0]])));
  const held: Record<string, ReturnType<typeof task>[]> = {
    "web-shop": [task("W-a", "web-shop", "awaiting-approval"), task("W-b", "web-shop", "awaiting-approval")],
  };
  await page.route("**/projects/*/tasks", (r) => {
    const pid = r.request().url().match(/\/projects\/([^/]+)\/tasks/)?.[1] ?? "";
    return r.fulfill(json(held[pid] ?? []));
  });
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();

  // Select both held cards via their checkboxes (ticking does not open the session).
  const review = column(page, "Needs Review");
  await review.getByRole("checkbox", { name: /select task W-a/i }).check();
  await review.getByRole("checkbox", { name: /select task W-b/i }).check();

  // The bulk bar reflects the live count.
  const bulkbar = page.getByRole("region", { name: "Bulk actions" });
  await expect(bulkbar).toContainText("2 selected");

  // Approve & merge opens ONE confirm enumerating BOTH tasks (nothing hidden).
  await bulkbar.getByRole("button", { name: /approve & merge 2/i }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Approve 2 tasks?");
  await expect(dialog).toContainText("web-shop/W-a");
  await expect(dialog).toContainText("web-shop/W-b");
  await page.screenshot({ path: "test-results/bulk-approve.png", fullPage: true });
});
