// Command palette (⌘K) e2e (redesign E4): with the gateway MOCKED (REST stubs +
// a silent /ws), sign in and prove the director keyboard flow end-to-end — the
// tab-bar trigger AND the ⌘K shortcut open the palette, typing filters to a
// session, Enter/click drills into it, and a navigation row switches surface.
// Deterministic + offline.
import { expect, test, type Page } from "@playwright/test";

async function mockWebSocket(page: Page) {
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});
}

const STATUS = {
  projects: 1,
  hosts: 1,
  leases: [],
  generated_at: "2026-06-20T00:00:00Z",
};
const PROJECTS = [
  { id: "web-shop", repo: "git@x:web-shop.git", base_branch: "develop", host_id: "", readiness: "ready", recipe_pointer: "", governance_policy: "auto", paused: false },
];
const HOSTS = [
  { id: "host-linux", capabilities: ["linux", "web", "backend"], heartbeat_age_seconds: 2 },
];
function task(id: string, status: string) {
  return {
    id, project_id: "web-shop", lane: "backend", tier: "T2",
    status, requires: [], deps: [], branch: "", scenario_id: id,
    retry_count: 0, abort_requested: false, approved: false,
  };
}
const TASKS: Record<string, ReturnType<typeof task>[]> = {
  "web-shop": [task("WEB-7", "running"), task("WEB-8", "awaiting-approval")],
};

function json(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

async function signIn(page: Page) {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json(HOSTS)));
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (r) => {
    const pid = r.request().url().match(/\/projects\/([^/]+)\/tasks/)?.[1] ?? "";
    return r.fulfill(json(TASKS[pid] ?? []));
  });
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();
}

test("the tab-bar trigger opens the palette; typing a task id drills into its session", async ({ page }) => {
  await signIn(page);

  await page.getByRole("button", { name: /search & jump/i }).click();
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();

  // Filter to a single session and open it (click runs the row).
  await palette.getByRole("textbox", { name: "Command palette query" }).fill("web-8");
  await palette.getByText("WEB-8").click();

  // The session detail view (E2) is now showing for that task.
  await expect(page.getByRole("region", { name: /^session$/i })).toBeVisible();
});

test("the ⌘K shortcut opens the palette; a nav row switches surface via keyboard", async ({ page }) => {
  await signIn(page);

  // ControlOrMeta+K works on both macOS and Linux runners.
  await page.keyboard.press("ControlOrMeta+KeyK");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();

  // Type to filter to the Events nav row, then Enter runs the highlighted item.
  await palette.getByRole("textbox", { name: "Command palette query" }).fill("events");
  await page.keyboard.press("Enter");

  // The cockpit navigated to the Events surface; the palette closed.
  await expect(page.getByRole("tab", { name: /^events$/i })).toHaveAttribute("aria-selected", "true");
  await expect(palette).toBeHidden();

  // Esc-close path: reopen, then Escape dismisses.
  await page.keyboard.press("ControlOrMeta+KeyK");
  await expect(palette).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(palette).toBeHidden();

  await page.screenshot({ path: "test-results/command-palette.png", fullPage: true });
});
