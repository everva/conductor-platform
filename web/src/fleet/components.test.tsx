// Component tests for the fleet panels: HostsPanel heartbeat rendering (fresh /
// stale / never), ProjectsTable paused badge + lease holder, TasksView id-sorting
// and abort/approved badges, and empty states that render without crashing.
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { HostsPanel } from "./HostsPanel.tsx";
import { ProjectsTable } from "./ProjectsTable.tsx";
import { TasksView } from "./TasksView.tsx";
import { EventTicker } from "./EventTicker.tsx";
import type { Event, Host, Lease, Project, Task } from "../api/types.ts";

function host(over: Partial<Host>): Host {
  return { id: "h", capabilities: [], heartbeat_age_seconds: 0, ...over };
}

function project(over: Partial<Project>): Project {
  return {
    id: "p",
    repo: "r",
    base_branch: "main",
    host_id: "",
    readiness: "ready",
    recipe_pointer: "",
    governance_policy: "auto",
    paused: false,
    ...over,
  };
}

function task(over: Partial<Task>): Task {
  return {
    id: "t",
    project_id: "p",
    lane: "l",
    tier: "core",
    status: "queued",
    requires: [],
    deps: [],
    branch: "b",
    scenario_id: "",
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...over,
  };
}

describe("HostsPanel", () => {
  it("renders fresh, stale, and never heartbeats", () => {
    render(
      <HostsPanel
        hosts={[
          host({ id: "host-a", heartbeat_age_seconds: 12, last_heartbeat: "2026-06-18T00:00:00Z" }),
          host({ id: "host-b", heartbeat_age_seconds: 120, last_heartbeat: "2026-06-18T00:00:00Z" }),
          host({ id: "host-c", heartbeat_age_seconds: 0 }),
        ]}
      />,
    );
    expect(screen.getByText("12s ago")).toBeInTheDocument();
    expect(screen.getByText("2m ago")).toBeInTheDocument();
    expect(screen.getByText("stale")).toBeInTheDocument();
    expect(screen.getByText("never")).toBeInTheDocument();
  });

  it("renders an empty state for no hosts", () => {
    render(<HostsPanel hosts={[]} />);
    expect(screen.getByText(/no hosts registered/i)).toBeInTheDocument();
  });
});

describe("ProjectsTable", () => {
  it("renders the paused badge and the lease holder", () => {
    const leases: Record<string, Lease[]> = {
      p1: [{ project_id: "p1", host_id: "hostA", task_id: "taskA", acquired_at: "" }],
    };
    render(
      <ProjectsTable
        projects={[project({ id: "p1", paused: true })]}
        leasesByProject={leases}
        selectedProjectId={null}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText("paused")).toBeInTheDocument();
    expect(screen.getByText(/hostA · taskA/)).toBeInTheDocument();
  });

  it("calls onSelect when a row is clicked", async () => {
    const onSelect = vi.fn();
    render(
      <ProjectsTable
        projects={[project({ id: "pX" })]}
        leasesByProject={{}}
        selectedProjectId={null}
        onSelect={onSelect}
      />,
    );
    screen.getByText("pX").closest("tr")!.click();
    expect(onSelect).toHaveBeenCalledWith("pX");
  });

  it("renders an empty state for no projects", () => {
    render(
      <ProjectsTable
        projects={[]}
        leasesByProject={{}}
        selectedProjectId={null}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText(/no projects onboarded/i)).toBeInTheDocument();
  });
});

describe("TasksView", () => {
  it("sorts tasks by id and renders abort/approved badges", () => {
    render(
      <TasksView
        selectedProjectId="p"
        tasks={[
          task({ id: "t-c", approved: true }),
          task({ id: "t-a", abort_requested: true }),
          task({ id: "t-b" }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("row").slice(1); // drop header
    const firstCells = rows.map((r) => within(r).getAllByRole("cell")[0].textContent);
    expect(firstCells).toEqual(["t-a", "t-b", "t-c"]);
    expect(screen.getByText("approved")).toBeInTheDocument();
    expect(screen.getByText("abort")).toBeInTheDocument();
  });

  it("shows the block reason on a stalled task, hidden otherwise", () => {
    render(
      <TasksView
        selectedProjectId="p"
        tasks={[
          task({ id: "t-blocked", status: "blocked", reason: "agent run failed: malformed verdict" }),
          task({ id: "t-run", status: "running", reason: "should not show" }),
        ]}
      />,
    );
    // The reason surfaces on the blocked task so the director sees WHY it stalled…
    expect(screen.getByText("agent run failed: malformed verdict")).toBeInTheDocument();
    // …but a non-blocked task never shows a (stale) reason.
    expect(screen.queryByText("should not show")).not.toBeInTheDocument();
  });

  it("prompts to select a project when none is selected", () => {
    render(<TasksView selectedProjectId={null} tasks={undefined} />);
    expect(screen.getByText(/select a project/i)).toBeInTheDocument();
  });

  it("renders an empty state for a project with no tasks", () => {
    render(<TasksView selectedProjectId="p" tasks={[]} />);
    expect(screen.getByText(/no tasks for this project/i)).toBeInTheDocument();
  });
});

describe("EventTicker", () => {
  function ev(over: Partial<Event>): Event {
    return {
      id: "e",
      ts: "2026-06-18T00:00:00Z",
      project: "p",
      task: "t",
      phase: "develop",
      kind: "progress",
      payload: {},
      ...over,
    };
  }

  it("highlights intervention-needed events", () => {
    render(
      <EventTicker
        events={[ev({ id: "e1", kind: "intervention-needed", phase: "review" })]}
      />,
    );
    expect(screen.getByText("review/intervention-needed")).toBeInTheDocument();
  });

  it("renders an empty state for no events", () => {
    render(<EventTicker events={[]} />);
    expect(screen.getByText(/no events yet/i)).toBeInTheDocument();
  });
});
