// Real-gateway cockpit e2e (M3) — NO page.route mocks. The board is driven against a REAL,
// freshly-built conductor-api seeded over its real REST API (run.sh: onboard everva/e2e-demo →
// intake seed.yaml → lease task E2E-RUN). This proves M1 end-to-end through the actual gateway:
// leasing flips the persisted status to running, /tasks reports it, and the board shows the task
// in the Running column with its real lease host — the exact path the live davinci host uses.
//
// Run via `npm run e2e:realgw` (opt-in; never CI). The token + ports come from run.sh's env.
import { expect, test, type Page } from "@playwright/test";

const TOKEN = process.env.REALGW_TOKEN ?? "";

const column = (page: Page, label: string) =>
  page.getByRole("listitem", { name: new RegExp(`^${label} `) });

async function signIn(page: Page) {
  await page.goto("/");
  await page.getByLabel(/api token/i).fill(TOKEN);
  await page.getByRole("button", { name: /sign in/i }).click();
  // The Command Center board is the default surface, served from REAL gateway data.
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();
}

test("leased task shows Running on the board, from the real gateway (M1, no mocks)", async ({ page }) => {
  expect(TOKEN, "REALGW_TOKEN must be set by run.sh").not.toBe("");
  await signIn(page);

  // The seeded+leased task lands in the Running column — its STORED status is "running"
  // (the gateway flipped it on lease), so the board (lease-derived) and any /tasks reader
  // agree. Before M1 the stored status stayed "todo" and /tasks readers disagreed.
  await expect(column(page, "Running").getByText("E2E-RUN")).toBeVisible();
  // The card shows the real lease host that run.sh leased it as.
  await expect(column(page, "Running").getByText(/e2e-host/)).toBeVisible();
  // The top-strip running count reflects the one real running task.
  await expect(page.locator(".cc-stat", { hasText: "running" })).toContainText("1");

  await page.screenshot({ path: "test-results/realgw-board-running.png", fullPage: true });
});

test("the real gateway's /tasks reports the leased task as running (M1 status flip)", async ({ request, baseURL }) => {
  // Hit the gateway through the SAME same-origin preview proxy the app uses — the editor's
  // sessions tree reads exactly this endpoint, so this is the tree-vs-board consistency in data form.
  const res = await request.get(`${baseURL}/projects/e2e-demo/tasks`, {
    headers: { Authorization: `Bearer ${TOKEN}` },
  });
  expect(res.ok()).toBeTruthy();
  const tasks = (await res.json()) as Array<{ id: string; status: string }>;
  const run = tasks.find((t) => t.id === "E2E-RUN");
  expect(run, "seeded task E2E-RUN present").toBeTruthy();
  expect(run!.status).toBe("running");
});
