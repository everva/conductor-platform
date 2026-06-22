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

  // "New work" jumps to the Intake surface (the leading + is now a decorative,
  // aria-hidden icon, so the accessible name is "New work").
  await page.getByRole("button", { name: /new work/i }).click();
  await expect(page.getByRole("tab", { name: /intake/i })).toHaveAttribute("aria-selected", "true");

  // The claude-free path: no conversation, no distill — author straight into the
  // authoritative YAML editor, seeded with a valid scenario template.
  await page.getByRole("button", { name: "Write spec directly" }).click();
  await expect(page.getByLabel("Intake YAML")).toHaveValue(/id: NEW-1/);
  await expect(page.getByRole("button", { name: "Approve & add to ledger" })).toBeVisible();
});

test("'+ New work' → distill asks a clarifying question; answering re-distills to a proposal (Q3c)", async ({ page }) => {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/tasks", (r) => r.fulfill(json([])));
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));

  // Stateful distill mock (ADR-0047): the 1st call asks a clarifying question
  // (AskUserQuestion), the 2nd — once the director has answered — returns scenarios.
  let distillCalls = 0;
  await page.route("**/projects/*/distill", (r) => {
    distillCalls += 1;
    if (distillCalls === 1) {
      void r.fulfill(
        json({
          scenarios: [],
          yaml: "",
          questions: [
            {
              question: "Which datastore should it use?",
              header: "Datastore",
              multi_select: false,
              options: [
                { label: "Postgres", description: "Relational, the platform default." },
                { label: "Redis", description: "In-memory key-value cache." },
              ],
            },
          ],
        }),
      );
      return;
    }
    void r.fulfill(
      json({
        scenarios: [
          {
            id: "A-1",
            title: "Data service",
            lane: "backend",
            tier: "T2",
            deps: [],
            acceptance: ["stores records"],
            hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go",
          },
        ],
        yaml: "id: A-1\ntitle: Data service\n",
      }),
    );
  });

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();

  await page.getByRole("button", { name: /new work/i }).click();
  await expect(page.getByRole("tab", { name: /intake/i })).toHaveAttribute("aria-selected", "true");

  // Describe the work → the distiller asks a clarifying question instead of guessing.
  await page.getByLabel("Message").fill("build a data service");
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByTestId("question-card")).toBeVisible();
  await expect(page.getByText("Which datastore should it use?")).toBeVisible();

  // Answer it → the answer folds into the conversation and re-distills → proposal.
  await page.getByRole("radio", { name: /Postgres/ }).click();
  await page.getByRole("button", { name: "Submit answers" }).click();

  await expect(page.getByLabel("Intake YAML")).toHaveValue(/id: A-1/);
  await expect(page.getByTestId("question-card")).toHaveCount(0);
});
