// Component tests for the Command Center board (redesign E1): the four lifecycle
// columns render their cards, the top strip shows the director's counts, a leased
// task appears under Running with its host, an awaiting-approval card exposes the
// confirm-gated Approve, and a card click focuses its project (the E1 drill-in).
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommandCenter } from "./CommandCenter.tsx";
import type { Host, Lease, Task } from "../api/types.ts";
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

  it("offers Approve on an awaiting-approval card and opens its session on click", async () => {
    const user = userEvent.setup();
    const onOpenSession = vi.fn();
    const requestApprove = vi.fn();
    const controls = {
      isTaskBusy: () => false,
      requestApprove,
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

    // Clicking the card body (not the action) drills into the task's session.
    await user.click(screen.getByText("T-h"));
    expect(onOpenSession).toHaveBeenCalledWith(
      expect.objectContaining({ id: "T-h", project_id: "p" }),
    );
  });
});
