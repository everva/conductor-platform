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

// sseBody fulfills an SSE (text/event-stream) response from the given frames — the
// shape POST /projects/{id}/distill/stream returns (Q3c.4).
function sseBody(...frames: Array<{ event: string; data: unknown }>) {
  return {
    status: 200,
    contentType: "text/event-stream",
    body: frames.map((f) => `event: ${f.event}\ndata: ${JSON.stringify(f.data)}\n\n`).join(""),
  };
}

test("'+ New work' → Write spec directly opens the authoritative YAML editor (claude-free)", async ({ page }) => {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/tasks", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/intake/sessions**", (r) => r.fulfill(json({ sessions: [] })));
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
  await page.route("**/projects/*/intake/sessions**", (r) => r.fulfill(json({ sessions: [] })));
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));

  // Stateful streaming-distill mock (ADR-0047 + Q3c.4 SSE): the 1st call streams a
  // progress event then asks a clarifying question; the 2nd — once answered — streams
  // progress then returns scenarios. The app consumes /distill/stream (SSE).
  let distillCalls = 0;
  await page.route("**/projects/*/distill/stream", (r) => {
    distillCalls += 1;
    if (distillCalls === 1) {
      void r.fulfill(
        sseBody(
          { event: "progress", data: { lines: 2 } },
          {
            event: "result",
            data: {
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
            },
          },
        ),
      );
      return;
    }
    void r.fulfill(
      sseBody(
        { event: "progress", data: { lines: 4 } },
        {
          event: "result",
          data: {
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
          },
        },
      ),
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

test("intake history: a saved conversation is listed per project and reopens its thread", async ({ page }) => {
  await mockWebSocket(page);
  await page.route("**/status", (r) => r.fulfill(json(STATUS)));
  await page.route("**/hosts", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/scenarios", (r) => r.fulfill(json([])));
  await page.route("**/events*", (r) => r.fulfill(json([])));
  await page.route("**/projects/*/tasks", (r) => r.fulfill(json([])));

  // A prior conversation already saved for this project (the gateway's history endpoints).
  const PRIOR_FULL = {
    id: "is-prior",
    project_id: "web-shop",
    title: "kullanıcı profili ekle",
    messages: [
      { role: "you", text: "kullanıcı profili sayfası ekle" },
      { role: "assistant", text: "Drafted 1 scenario — review the plan below." },
    ],
    result: "id: A-1\ntitle: profile\n",
    created_at: "2026-06-20T09:00:00Z",
    updated_at: "2026-06-20T09:01:00Z",
  };
  // The GET-with-id (full) and PUT (autosave) share the `/sessions/*` glob — branch on method.
  await page.route("**/projects/*/intake/sessions/*", (r) => {
    if (r.request().method() === "PUT") {
      void r.fulfill(json({ status: "ok", id: "is-prior" }));
      return;
    }
    void r.fulfill(json(PRIOR_FULL));
  });
  // The list (no trailing id) returns the one prior conversation as a summary.
  await page.route("**/projects/*/intake/sessions", (r) =>
    r.fulfill(
      json({
        sessions: [
          {
            id: "is-prior",
            project_id: "web-shop",
            title: "kullanıcı profili ekle",
            has_result: true,
            created_at: "2026-06-20T09:00:00Z",
            updated_at: "2026-06-20T09:01:00Z",
          },
        ],
      }),
    ),
  );
  await page.route("**/projects", (r) => r.fulfill(json(PROJECTS)));

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /command center/i })).toBeVisible();

  await page.getByRole("button", { name: /new work/i }).click();
  await expect(page.getByRole("tab", { name: /intake/i })).toHaveAttribute("aria-selected", "true");

  // The History panel lists the prior conversation for this project (Claude-Code-style).
  const history = page.getByRole("region", { name: /conversation history/i });
  await expect(history).toBeVisible();
  await expect(history.getByText("kullanıcı profili ekle")).toBeVisible();

  // Clicking it reopens the full thread (both turns restored) and resumes its authored YAML.
  await history.getByText("kullanıcı profili ekle").click();
  const thread = page.getByRole("log", { name: /intake conversation/i });
  await expect(thread.getByText("kullanıcı profili sayfası ekle")).toBeVisible();
  await expect(thread.getByText(/Drafted 1 scenario/)).toBeVisible();
  await expect(page.getByLabel("Intake YAML")).toHaveValue(/id: A-1/);
});
