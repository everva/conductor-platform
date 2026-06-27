// Component tests for the Command Center board (redesign E1): the four lifecycle
// columns render their cards, the top strip shows the director's counts, a leased
// task appears under Running with its host, an awaiting-approval card exposes the
// confirm-gated Approve, and a card click focuses its project (the E1 drill-in).
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommandCenter } from "./CommandCenter.tsx";
import type { Event, Host, Lease, Project, Task } from "../api/types.ts";
import type { FleetControls } from "./useFleetControls.ts";

function task(p: Partial<Task> & Pick<Task, "id" | "project_id" | "status">): Task {
  return {
    lane: "web",
    tier: "T2",
    requires: [],
    deps: [],
    branch: "",
    scenario_id: p.id,
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...p,
  };
}

const host = (id: string): Host => ({ id, capabilities: ["linux"], heartbeat_age_seconds: 1 });

function col(label: string): HTMLElement {
  // Each column is a listitem labelled "<Label> (<n>)".
  return screen.getByRole("listitem", { name: new RegExp(`^${label} `) });
}

describe("CommandCenter board", () => {
  it("buckets tasks into the four lifecycle columns and shows the counts", () => {
    render(
      <CommandCenter
        tasksByProject={{
          p: [
            task({ id: "T-ready", project_id: "p", status: "ready" }),
            task({ id: "T-run", project_id: "p", status: "running" }),
            task({ id: "T-await", project_id: "p", status: "awaiting-approval" }),
            task({ id: "T-block", project_id: "p", status: "blocked" }),
            task({ id: "T-done", project_id: "p", status: "done" }),
          ],
        }}
        leasesByProject={{}}
        hosts={[host("host-linux")]}
        recentEvents={[]}
      />,
    );
    expect(within(col("Ready")).getByText("T-ready")).toBeInTheDocument();
    expect(within(col("Running")).getByText("T-run")).toBeInTheDocument();
    expect(within(col("Needs Review")).getByText("T-await")).toBeInTheDocument();
    expect(within(col("Needs Review")).getByText("T-block")).toBeInTheDocument();
    expect(within(col("Done")).getByText("T-done")).toBeInTheDocument();
    // top strip: 1 running, 2 need review, 1 blocked
    expect(screen.getByText("need your review").closest(".cc-stat")).toHaveTextContent("2");
  });

  it("shows a leased task under Running with its host", () => {
    const leases: Record<string, Lease[]> = {
      p: [{ project_id: "p", host_id: "host-mac", task_id: "T-1", acquired_at: "x" }],
    };
    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-1", project_id: "p", status: "ready" })] }}
        leasesByProject={leases}
        hosts={[]}
        recentEvents={[]}
      />,
    );
    const running = col("Running");
    expect(within(running).getByText("T-1")).toBeInTheDocument();
    expect(within(running).getByText(/host-mac/)).toBeInTheDocument();
  });

  it("offers Approve AND Reject on an awaiting-approval card and opens its session on click", async () => {
    const user = userEvent.setup();
    const onOpenSession = vi.fn();
    const requestApprove = vi.fn();
    const requestReject = vi.fn();
    const controls = {
      isTaskBusy: () => false,
      isProjectBusy: () => false,
      retry: vi.fn(),
      requestApprove,
      requestReject,
    } as unknown as FleetControls;

    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-h", project_id: "p", status: "awaiting-approval" })] }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[]}
        controls={controls}
        onOpenSession={onOpenSession}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect(requestApprove).toHaveBeenCalledWith("p", "T-h");

    // A held card is NOT a dead-end: Reject declines the merge (the counterpart action).
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(requestReject).toHaveBeenCalledWith("p", "T-h");

    // Clicking the card body (not the action) drills into the task's session.
    await user.click(screen.getByText("T-h"));
    expect(onOpenSession).toHaveBeenCalledWith(
      expect.objectContaining({ id: "T-h", project_id: "p" }),
    );
  });

  it("shows the rich live activity (📖/🔎) on a developing card", () => {
    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-1", project_id: "p", status: "running" })] }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[
          {
            id: "e1",
            ts: "2026-06-20T00:00:00Z",
            project: "p",
            task: "T-1",
            phase: "develop",
            kind: "progress",
            payload: { step: "developing", detail: "📖 Okunuyor: app.ts" },
          } as unknown as Event,
        ]}
      />,
    );
    // The board surfaces claude's real activity line verbatim — the director watches it work.
    expect(screen.getByText("📖 Okunuyor: app.ts")).toBeInTheDocument();
  });

  it("multi-selects held cards and bulk-approves the exact selection (E4)", async () => {
    const user = userEvent.setup();
    const onOpenSession = vi.fn();
    const requestBulkApprove = vi.fn();
    const controls = {
      isTaskBusy: () => false,
      isProjectBusy: () => false,
      retry: vi.fn(),
      requestApprove: vi.fn(),
      requestBulkApprove,
    } as unknown as FleetControls;

    render(
      <CommandCenter
        tasksByProject={{
          web: [
            task({ id: "W-1", project_id: "web", status: "awaiting-approval" }),
            task({ id: "W-2", project_id: "web", status: "awaiting-approval" }),
            task({ id: "W-run", project_id: "web", status: "running" }),
          ],
        }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[]}
        controls={controls}
        onOpenSession={onOpenSession}
      />,
    );

    // Only the two held cards are selectable; the running one has no checkbox.
    expect(screen.getAllByRole("checkbox")).toHaveLength(2);

    // Ticking a checkbox selects WITHOUT opening the session (stopPropagation).
    await user.click(screen.getByRole("checkbox", { name: /select task W-1/i }));
    expect(onOpenSession).not.toHaveBeenCalled();
    await user.click(screen.getByRole("checkbox", { name: /select task W-2/i }));

    // The bulk bar reflects the live count.
    const bulkbar = screen.getByRole("region", { name: "Bulk actions" });
    expect(within(bulkbar).getByText("2 selected")).toBeInTheDocument();

    // Approve & merge passes the EXACT selected items (stable order).
    await user.click(within(bulkbar).getByRole("button", { name: /approve & merge 2/i }));
    expect(requestBulkApprove).toHaveBeenCalledWith([
      { projectId: "web", taskId: "W-1" },
      { projectId: "web", taskId: "W-2" },
    ]);
  });

  it("multi-selects BLOCKED cards and bulk-retries the exact selection (quick-win)", async () => {
    const user = userEvent.setup();
    const onOpenSession = vi.fn();
    const requestBulkRetry = vi.fn();
    const controls = {
      isTaskBusy: () => false,
      isProjectBusy: () => false,
      retry: vi.fn(),
      requestApprove: vi.fn(),
      requestBulkRetry,
    } as unknown as FleetControls;

    render(
      <CommandCenter
        tasksByProject={{
          web: [
            task({ id: "B-1", project_id: "web", status: "blocked" }),
            task({ id: "B-2", project_id: "web", status: "blocked" }),
            task({ id: "W-run", project_id: "web", status: "running" }),
          ],
        }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[]}
        controls={controls}
        onOpenSession={onOpenSession}
      />,
    );

    // Only the two blocked cards are selectable; the running one has no checkbox.
    expect(screen.getAllByRole("checkbox")).toHaveLength(2);

    // Ticking a blocked card selects it WITHOUT opening the session.
    await user.click(screen.getByRole("checkbox", { name: /select task B-1 for bulk retry/i }));
    expect(onOpenSession).not.toHaveBeenCalled();
    await user.click(screen.getByRole("checkbox", { name: /select task B-2 for bulk retry/i }));

    // The bulk bar offers Retry (not Approve) for a blocked selection.
    const bulkbar = screen.getByRole("region", { name: "Bulk actions" });
    expect(within(bulkbar).getByText("2 selected")).toBeInTheDocument();
    expect(within(bulkbar).queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();

    // Retry passes the EXACT selected blocked items (stable order).
    await user.click(within(bulkbar).getByRole("button", { name: /^retry 2$/i }));
    expect(requestBulkRetry).toHaveBeenCalledWith([
      { projectId: "web", taskId: "B-1" },
      { projectId: "web", taskId: "B-2" },
    ]);
  });

  it("scopes the board to selectedProjectId and Show all clears the scope (Q1)", async () => {
    const user = userEvent.setup();
    const onShowAllProjects = vi.fn();
    render(
      <CommandCenter
        tasksByProject={{
          web: [task({ id: "W-1", project_id: "web", status: "running" })],
          api: [task({ id: "A-1", project_id: "api", status: "running" })],
        }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[]}
        selectedProjectId="web"
        onShowAllProjects={onShowAllProjects}
      />,
    );
    // Only the selected project's task is on the board (the other project is scoped out).
    expect(within(col("Running")).getByText("W-1")).toBeInTheDocument();
    expect(within(col("Running")).queryByText("A-1")).not.toBeInTheDocument();
    // The scope indicator names the project; "Show all" clears the scope back to the full fleet.
    const scope = screen.getByRole("status");
    expect(within(scope).getByText("web")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show all" }));
    expect(onShowAllProjects).toHaveBeenCalled();
  });

  it("Clear empties the selection and hides the bulk bar (E4)", async () => {
    const user = userEvent.setup();
    const controls = {
      isTaskBusy: () => false,
      isProjectBusy: () => false,
      retry: vi.fn(),
      requestApprove: vi.fn(),
      requestBulkApprove: vi.fn(),
    } as unknown as FleetControls;

    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-h", project_id: "p", status: "awaiting-approval" })] }}
        leasesByProject={{}}
        hosts={[]}
        recentEvents={[]}
        controls={controls}
        onOpenSession={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("checkbox", { name: /select task T-h/i }));
    expect(screen.getByRole("region", { name: "Bulk actions" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.queryByRole("region", { name: "Bulk actions" })).not.toBeInTheDocument();
  });
});

describe("CommandCenter — scoped project Pause / Resume", () => {
  function pauseControls(over: Partial<Record<string, unknown>>): FleetControls {
    return {
      isTaskBusy: () => false,
      isProjectBusy: () => false,
      projectAction: () => null,
      isPaused: (_id: string, stored: boolean) => stored,
      pause: vi.fn(),
      resume: vi.fn(),
      retry: vi.fn(),
      requestApprove: vi.fn(),
      ...over,
    } as unknown as FleetControls;
  }

  function renderScoped(controls: FleetControls, paused: boolean) {
    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-1", project_id: "p", status: "running" })] }}
        leasesByProject={{}}
        hosts={[]}
        projects={[{ id: "p", paused } as Project]}
        recentEvents={[]}
        controls={controls}
        selectedProjectId="p"
      />,
    );
  }

  it("offers Pause for a scoped, running project and calls pause", async () => {
    const user = userEvent.setup();
    const pause = vi.fn();
    renderScoped(pauseControls({ pause }), false);
    await user.click(screen.getByRole("button", { name: /^pause$/i }));
    expect(pause).toHaveBeenCalledWith("p");
  });

  it("offers Resume for a scoped, paused project and calls resume", async () => {
    const user = userEvent.setup();
    const resume = vi.fn();
    renderScoped(pauseControls({ isPaused: () => true, resume }), true);
    await user.click(screen.getByRole("button", { name: /^resume$/i }));
    expect(resume).toHaveBeenCalledWith("p");
  });

  it("shows no pause control across the whole fleet (no scoped project)", () => {
    render(
      <CommandCenter
        tasksByProject={{ p: [task({ id: "T-1", project_id: "p", status: "running" })] }}
        leasesByProject={{}}
        hosts={[]}
        projects={[{ id: "p", paused: false } as Project]}
        recentEvents={[]}
        controls={pauseControls({})}
      />,
    );
    expect(screen.queryByRole("button", { name: /^pause$/i })).not.toBeInTheDocument();
  });
});
