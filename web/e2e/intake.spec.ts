// Intake "write spec directly" e2e (redesign E3): with the gateway MOCKED, open
// "+ New work" and prove the claude-FREE authoring path — "Write spec directly"
// opens the authoritative YAML editor seeded with a valid template (no distill
// call), ready to dispatch. The full confirm→POST→created-ids leg is covered
// deterministically by the vitest suite (IntakeChat.test.tsx) + a live run; the
// fixed-position confirm modal is flaky under Playwright's actionability gate (as
// documented for the other control modals), so it is not asserted here.
import { expect, test, type Page } from "@playwright/test";

async function mockWebSocket(page: Page) {
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});
}

const STATUS = { projects: 1, hosts: 1, leases: [], generated_at: "2026-06-20T00:00:00Z" };
const PROJECTS = [
  { id: "web-shop", repo: "git@x:web-shop.git", base_branch: "develop", host_id: "", readiness: "ready", recipe_pointer: "", governance_policy: "auto", paused: false },
];

function json(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

test("'+ New work' → Write spec directly opens the authoritative YAML editor (claude-free)", async ({ page }) => {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/tasks", (r) => r.fulfill(json([])));
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();

  // "+ New work" jumps to the Intake surface.
  await page.getByRole("button", { name: "+ New work" }).click();
  await expect(page.getByRole("tab", { name: /intake/i })).toHaveAttribute("aria-selected", "true");

  // The claude-free path: no conversation, no distill — author straight into the
  // authoritative YAML editor, seeded with a valid scenario template.
  await page.getByRole("button", { name: "Write spec directly" }).click();
  await expect(page.getByLabel("Intake YAML")).toHaveValue(/id: NEW-1/);
  await expect(page.getByRole("button", { name: "Approve & add to ledger" })).toBeVisible();
});
