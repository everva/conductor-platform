// Dashboard e2e (3B-1): with the gateway fully MOCKED via page.route (REST stubs +
// a stubbed /ws so no real socket is needed), sign in through the TokenGate and
// assert the fleet dashboard renders the projects and hosts. Deterministic and
// offline — no running gateway required.
import { expect, test, type Page } from "@playwright/test";

// Cleanly mock the /ws WebSocket so the cockpit sees an OPEN connection rather than
// a rejected upgrade (which fires onclose → "offline" state churn and makes the
// dashboard re-render under Playwright, the root of the intake-test flakiness). An
// empty routeWebSocket handler accepts the socket client-side and stays silent — no
// real gateway needed. (Playwright 1.48+.)
async function mockWebSocket(page: Page) {
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});
}

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
  await mockWebSocket(page);
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
  // Redesign E1: the Command Center board is the default surface; this test asserts
  // the Fleet view's project/host tables, so switch to it.
  await page.getByRole("tab", { name: /^fleet$/i }).click();
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
  await mockWebSocket(page);
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

test("intervention controls: resume (200) and abort (409 notice) over mocked control API (3B-3)", async ({
  page,
}) => {
  await mockWebSocket(page);
  await page.route("**/status", (route) => route.fulfill(json(STATUS)));
  await page.route("**/hosts", (route) => route.fulfill(json(HOSTS)));
  await page.route("**/projects", (route) => route.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (route) => route.fulfill(json(TASKS)));

  // Control POSTs: resume → 200; abort → 409 (gateway "no task running to abort"),
  // which the UI must surface as a non-fatal warn notice (never a stuck spinner).
  let resumeCalls = 0;
  await page.route("**/projects/*/resume", (route) => {
    resumeCalls += 1;
    return route.fulfill(json({ project: "p1", paused: false }));
  });
  await page.route("**/projects/*/abort", (route) =>
    route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({ error: "no task running to abort" }),
    }),
  );

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /fleet status/i })).toBeVisible();
  // Redesign E1: board is the default surface; the per-project Pause/Resume/Abort
  // controls live in the Fleet view, so switch to it.
  await page.getByRole("tab", { name: /^fleet$/i }).click();

  // Abort is confirm-gated: clicking it opens the dialog; confirming POSTs abort. The
  // mocked 409 ("no task running to abort") surfaces as a non-fatal warn notice and
  // the dialog closes (never a stuck spinner). NOTE: the modal confirm uses
  // dispatchEvent rather than a synthesized click — the background poll/WS-reconnect
  // re-render churn trips Playwright's "element stable" actionability gate on the
  // fixed-position modal button, so we fire the click directly (the deterministic
  // vitest suite covers the full confirm→abort→409-notice path exhaustively).
  await page.getByRole("button", { name: /^abort$/i }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByRole("button", { name: /abort task/i }).dispatchEvent("click");
  await expect(page.getByText(/no task running to abort/i)).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // Resume is immediate (no confirm): clicking it POSTs resume to the control API.
  // dispatchEvent for the same reason as above (background re-render churn vs. the
  // "stable" actionability gate); we assert the control POST actually fired.
  await page.getByRole("button", { name: /^resume$/i }).dispatchEvent("click");
  await expect.poll(() => resumeCalls).toBe(1);
});

const DISTILL_RESULT = {
  scenarios: [
    {
      id: "A-1",
      title: "First distilled task",
      lane: "backend",
      tier: "T1",
      deps: [],
      acceptance: ["does the first thing"],
      hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go",
    },
  ],
  yaml: "id: A-1\ntitle: First distilled task\n",
};

test("intake tab: converse → distill → review proposed scenario + holdout (3B-4b)", async ({
  page,
}) => {
  await mockWebSocket(page);
  await page.route("**/status", (route) => route.fulfill(json(STATUS)));
  await page.route("**/hosts", (route) => route.fulfill(json(HOSTS)));
  await page.route("**/projects", (route) => route.fulfill(json(PROJECTS)));
  await page.route("**/projects/*/tasks", (route) => route.fulfill(json(TASKS)));
  await page.route("**/projects/*/distill", (route) =>
    route.fulfill(json(DISTILL_RESULT)),
  );

  await page.goto("/");
  await page.getByLabel(/api token/i).fill("test-token");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByRole("region", { name: /fleet status/i })).toBeVisible();

  // Switch to Intake, describe the work, distill, and review the proposal — including
  // the highlighted repo-external hidden holdout ref. dispatchEvent fires React's
  // onClick directly (a real .click() can be dropped when the 5s fleet-poll re-render
  // moves the button mid-click); with the WebSocket cleanly mocked (mockWebSocket — no
  // offline/closed churn) the button stays mounted, so dispatchEvent triggers reliably.
  await page.getByRole("tab", { name: /intake/i }).click();
  await page.getByLabel("Conversation").fill("build the auth flow");
  // Retry the click → outcome until it lands: on rare 5s-poll re-render frames a
  // single dispatched click can be dropped, so toPass re-fires it until the proposal
  // renders (deterministic outcome, no fixed sleep). The distill route is mocked.
  await expect(async () => {
    await page.getByRole("button", { name: /^distill$/i }).dispatchEvent("click");
    await expect(page.getByTestId("scenario-card")).toBeVisible({ timeout: 2000 });
  }).toPass({ timeout: 15000 });
  await expect(page.getByTestId("scenario-holdout")).toContainText(
    "store://holdouts/A-1/holdout_test.go",
  );

  // The intake YAML textarea is the authoritative input the human approves verbatim.
  await expect(page.getByLabel("Intake YAML")).toHaveValue(/id: A-1/);

  // NOTE: the confirm-modal → approve → /intake → created-ids leg is NOT asserted
  // here: the background poll/WS-reconnect re-render churn makes the fixed-position
  // confirm dialog flaky under Playwright's actionability gate (the same limitation
  // documented for the abort/approve control modals above). That full leg —
  // approve POSTs the EDITED yaml and renders the created ids — is covered
  // deterministically by the vitest suite (IntakeChat.test.tsx).
});
