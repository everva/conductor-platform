// Intervention controls tests (3B-3). The ControlClient is a fake (no network); we
// drive the action layer (useFleetControls) through the real ProjectsTable /
// TasksView / ConfirmDialog / NoticeStack and assert:
//   * Pause/Resume toggle → calls client.pause/resume with the project id, disables
//     the button optimistically during the in-flight request, and invokes refresh()
//     on success;
//   * Abort → opens the confirm dialog; confirming calls client.abort; a 409 ApiError
//     ("no task running to abort") surfaces as a non-fatal WARN notice and the UI is
//     NOT stuck (button re-enables);
//   * Approve → confirm; task-level calls approve(projectId, taskId) and project-level
//     calls approve(projectId); a 409 ("multiple/none awaiting") surfaces clearly;
//   * the error path REVERTS the optimistic paused bit; a 401 triggers onUnauthorized.
//
// A small Harness component composes the hook with the panels exactly as the dashboard
// does, with a deferred fake so we can observe the in-flight (optimistic) window.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectsTable } from "./ProjectsTable.tsx";
import { TasksView } from "./TasksView.tsx";
import { ConfirmDialog } from "./ConfirmDialog.tsx";
import { NoticeStack } from "./NoticeStack.tsx";
import { useFleetControls } from "./useFleetControls.ts";
import type { ControlClient } from "./controls.ts";
import { ApiError } from "../api/client.ts";
import type { Lease, Project, Task } from "../api/types.ts";

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
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

function task(over: Partial<Task> = {}): Task {
  return {
    id: "t1",
    project_id: "p1",
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

// deferred is a manually-resolvable promise so a test can assert the optimistic
// in-flight window before the POST settles.
function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

interface FakeClient extends ControlClient {
  pause: ReturnType<typeof vi.fn>;
  resume: ReturnType<typeof vi.fn>;
  abort: ReturnType<typeof vi.fn>;
  approve: ReturnType<typeof vi.fn>;
}

function makeFake(over?: Partial<Record<keyof ControlClient, unknown>>): FakeClient {
  return {
    pause: vi.fn(async (id: string) => ({ project: id, paused: true })),
    resume: vi.fn(async (id: string) => ({ project: id, paused: false })),
    abort: vi.fn(async (id: string) => ({ project: id, aborted_task: "t1" })),
    approve: vi.fn(async (id: string, taskId?: string) => ({
      project: id,
      approved_task: taskId ?? "t1",
    })),
    ...over,
  } as FakeClient;
}

interface HarnessProps {
  client: ControlClient;
  projects: Project[];
  leasesByProject?: Record<string, Lease[]>;
  tasks?: Task[];
  selectedProjectId?: string | null;
  refresh: () => Promise<void>;
  onUnauthorized?: () => void;
}

// Harness wires the hook to the panels the same way FleetDashboard does.
function Harness({
  client,
  projects,
  leasesByProject = {},
  tasks,
  selectedProjectId = null,
  refresh,
  onUnauthorized = () => {},
}: HarnessProps) {
  const controls = useFleetControls({
    token: "tkn",
    refresh,
    onUnauthorized,
    makeClient: () => client,
  });
  return (
    <div>
      <NoticeStack notices={controls.notices} onDismiss={controls.dismissNotice} />
      <ProjectsTable
        projects={projects}
        leasesByProject={leasesByProject}
        selectedProjectId={selectedProjectId}
        onSelect={() => {}}
        controls={controls}
      />
      <TasksView
        selectedProjectId={selectedProjectId}
        tasks={tasks}
        controls={controls}
      />
      <ConfirmDialog
        pending={controls.pending}
        onConfirm={controls.confirm}
        onCancel={controls.cancelConfirm}
      />
    </div>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

// settle flushes the post-success microtask chain (refresh().then(...) → notice)
// without nesting a user-event click inside act() (which warns).
async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("Pause / Resume", () => {
  it("calls pause with the project id, disables optimistically, and refreshes on success", async () => {
    const user = userEvent.setup({ delay: null });
    const d = deferred<{ project: string; paused: boolean }>();
    const client = makeFake({ pause: vi.fn(() => d.promise) });
    const refresh = vi.fn(async () => {});

    render(<Harness client={client} projects={[project({ paused: false })]} refresh={refresh} />);

    const btn = screen.getByRole("button", { name: /^pause$/i });
    await user.click(btn);

    // In flight: client called, button shows the optimistic label and is disabled.
    expect(client.pause).toHaveBeenCalledWith("p1");
    const pausing = screen.getByRole("button", { name: /pausing…/i });
    expect(pausing).toBeDisabled();
    expect(refresh).not.toHaveBeenCalled();

    // Settle the POST → refresh runs (store reflection) + an ok notice appears.
    await act(async () => {
      d.resolve({ project: "p1", paused: true });
      await Promise.resolve();
    });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("fleet-notice")).toHaveTextContent(/paused p1/i);
  });

  it("shows Resume for a paused project and calls resume", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake();
    render(
      <Harness client={client} projects={[project({ paused: true })]} refresh={vi.fn(async () => {})} />,
    );
    await user.click(screen.getByRole("button", { name: /^resume$/i }));
    expect(client.resume).toHaveBeenCalledWith("p1");
  });

  it("reverts the optimistic paused bit and surfaces a notice on error", async () => {
    const user = userEvent.setup({ delay: null });
    const d = deferred<{ project: string; paused: boolean }>();
    const client = makeFake({ pause: vi.fn(() => d.promise) });
    const refresh = vi.fn(async () => {});
    render(<Harness client={client} projects={[project({ paused: false })]} refresh={refresh} />);

    await user.click(screen.getByRole("button", { name: /^pause$/i }));
    // Optimistically the state badge flips to "paused".
    expect(screen.getByText("paused")).toBeInTheDocument();

    await act(async () => {
      d.reject(new ApiError(500, "boom"));
      await Promise.resolve();
      await Promise.resolve();
    });

    // Reverted: back to "active", button re-enabled, error notice shown, no refresh.
    expect(screen.getByText("active")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^pause$/i })).toBeEnabled();
    expect(screen.getByTestId("fleet-notice")).toHaveTextContent(/500/);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("signs out on a 401", async () => {
    const user = userEvent.setup({ delay: null });
    const onUnauthorized = vi.fn();
    const client = makeFake({
      pause: vi.fn(async () => {
        throw new ApiError(401, "bad token");
      }),
    });
    render(
      <Harness
        client={client}
        projects={[project()]}
        refresh={vi.fn(async () => {})}
        onUnauthorized={onUnauthorized}
      />,
    );
    await user.click(screen.getByRole("button", { name: /^pause$/i }));
    await settle();
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
});

describe("Abort", () => {
  const lease: Record<string, Lease[]> = {
    p1: [{ project_id: "p1", host_id: "h", task_id: "t1", acquired_at: "" }],
  };

  it("confirms then aborts, then refreshes", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake();
    const refresh = vi.fn(async () => {});
    render(
      <Harness client={client} projects={[project()]} leasesByProject={lease} refresh={refresh} />,
    );

    await user.click(screen.getByRole("button", { name: /^abort$/i }));
    // Confirm dialog appears; abort not yet called.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(client.abort).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: /abort task/i }));
    await settle();
    expect(client.abort).toHaveBeenCalledWith("p1");
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("surfaces a 409 as a non-fatal warn and does not get stuck", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake({
      abort: vi.fn(async () => {
        throw new ApiError(409, "no task running to abort");
      }),
    });
    const refresh = vi.fn(async () => {});
    render(
      <Harness client={client} projects={[project()]} leasesByProject={lease} refresh={refresh} />,
    );

    await user.click(screen.getByRole("button", { name: /^abort$/i }));
    await user.click(screen.getByRole("button", { name: /abort task/i }));
    await settle();

    const notice = screen.getByTestId("fleet-notice");
    expect(notice).toHaveTextContent(/no task running to abort/i);
    expect(notice.className).toContain("warn");
    // Not stuck: the Abort button is back and enabled, refresh not called (no success).
    expect(screen.getByRole("button", { name: /^abort$/i })).toBeEnabled();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("disables Abort when no task is running", () => {
    render(
      <Harness client={makeFake()} projects={[project()]} leasesByProject={{}} refresh={vi.fn(async () => {})} />,
    );
    expect(screen.getByRole("button", { name: /^abort$/i })).toBeDisabled();
  });

  it("cancel dismisses the dialog without calling abort", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake();
    render(
      <Harness client={client} projects={[project()]} leasesByProject={lease} refresh={vi.fn(async () => {})} />,
    );
    await user.click(screen.getByRole("button", { name: /^abort$/i }));
    await user.click(screen.getByRole("button", { name: /^cancel$/i }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(client.abort).not.toHaveBeenCalled();
  });
});

describe("Approve", () => {
  const heldTask = task({ id: "t1", status: "awaiting-approval", approved: false });

  it("confirms then approves task-level with the task id", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake();
    const refresh = vi.fn(async () => {});
    render(
      <Harness
        client={client}
        projects={[project()]}
        tasks={[heldTask, task({ id: "t2", status: "awaiting-approval" })]}
        selectedProjectId="p1"
        refresh={refresh}
      />,
    );

    // Two held tasks → no project-level auto-resolve button; act on the row instead.
    expect(screen.queryByRole("button", { name: /approve awaiting/i })).toBeNull();

    const rows = screen.getAllByRole("row").slice(1);
    const t1Row = rows.find((r) => within(r).queryByText("t1"))!;
    await user.click(within(t1Row).getByRole("button", { name: /^approve$/i }));
    await user.click(screen.getByRole("button", { name: /approve & merge/i }));
    await settle();
    expect(client.approve).toHaveBeenCalledWith("p1", "t1");
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("project-level approve auto-resolves a unique awaiting task (no task id)", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake();
    render(
      <Harness
        client={client}
        projects={[project()]}
        tasks={[heldTask]}
        selectedProjectId="p1"
        refresh={vi.fn(async () => {})}
      />,
    );
    await user.click(screen.getByRole("button", { name: /approve awaiting/i }));
    await user.click(screen.getByRole("button", { name: /approve & merge/i }));
    await settle();
    expect(client.approve).toHaveBeenCalledWith("p1", undefined);
  });

  it("surfaces a 409 (ambiguous/none awaiting) clearly", async () => {
    const user = userEvent.setup({ delay: null });
    const client = makeFake({
      approve: vi.fn(async () => {
        throw new ApiError(409, "multiple tasks awaiting approval; specify task_id");
      }),
    });
    render(
      <Harness
        client={client}
        projects={[project()]}
        tasks={[heldTask]}
        selectedProjectId="p1"
        refresh={vi.fn(async () => {})}
      />,
    );
    await user.click(screen.getByRole("button", { name: /approve awaiting/i }));
    await user.click(screen.getByRole("button", { name: /approve & merge/i }));
    await settle();
    const notice = screen.getByTestId("fleet-notice");
    expect(notice).toHaveTextContent(/multiple tasks awaiting approval/i);
    expect(notice.className).toContain("warn");
  });

  it("offers no Approve affordance when nothing is awaiting", () => {
    render(
      <Harness
        client={makeFake()}
        projects={[project()]}
        tasks={[task({ id: "t1", status: "running" })]}
        selectedProjectId="p1"
        refresh={vi.fn(async () => {})}
      />,
    );
    expect(screen.queryByRole("button", { name: /^approve$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /approve awaiting/i })).toBeNull();
  });
});
