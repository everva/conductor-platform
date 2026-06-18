// InterventionBanner tests (3B-3): an intervention-needed event surfaces a banner
// row with the contextual Approve/Pause/Abort actions that call the right control
// method (Approve/Abort via the confirm dialog; Pause immediate), and the row's
// project link focuses the project. No network — the ControlClient is a fake.
import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InterventionBanner } from "./InterventionBanner.tsx";
import { ConfirmDialog } from "./ConfirmDialog.tsx";
import { useFleetControls } from "./useFleetControls.ts";
import type { ControlClient } from "./controls.ts";
import type { Event } from "../api/types.ts";

function ev(over: Partial<Event>): Event {
  return {
    id: "e",
    ts: "2026-06-18T00:00:00.000Z",
    project: "p1",
    task: "t1",
    phase: "review",
    kind: "intervention-needed",
    payload: {},
    ...over,
  };
}

function fake(): ControlClient & Record<string, ReturnType<typeof vi.fn>> {
  return {
    pause: vi.fn(async (id: string) => ({ project: id, paused: true })),
    resume: vi.fn(async (id: string) => ({ project: id, paused: false })),
    abort: vi.fn(async (id: string) => ({ project: id, aborted_task: "t1" })),
    approve: vi.fn(async (id: string, taskId?: string) => ({
      project: id,
      approved_task: taskId ?? "t1",
    })),
  } as ControlClient & Record<string, ReturnType<typeof vi.fn>>;
}

// settle flushes the post-action microtask chain without nesting a user-event click
// inside act() (which warns).
async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function Harness({
  events,
  client,
  onSelectProject = () => {},
}: {
  events: Event[];
  client: ControlClient;
  onSelectProject?: (id: string) => void;
}) {
  const controls = useFleetControls({
    token: "t",
    refresh: async () => {},
    onUnauthorized: () => {},
    makeClient: () => client,
  });
  return (
    <div>
      <InterventionBanner
        events={events}
        controls={controls}
        onSelectProject={onSelectProject}
      />
      <ConfirmDialog
        pending={controls.pending}
        onConfirm={controls.confirm}
        onCancel={controls.cancelConfirm}
      />
    </div>
  );
}

describe("InterventionBanner", () => {
  it("renders nothing when no intervention is pending", () => {
    const { container } = render(
      <Harness events={[ev({ kind: "progress" })]} client={fake()} />,
    );
    expect(container.querySelector(".fleet-intervention")).toBeNull();
  });

  it("Approve from the banner confirms then calls approve with the row's task", async () => {
    const user = userEvent.setup();
    const client = fake();
    render(<Harness events={[ev({ project: "p1", task: "t1" })]} client={client} />);

    await user.click(screen.getByRole("button", { name: /^approve$/i }));
    await user.click(screen.getByRole("button", { name: /approve & merge/i }));
    await settle();
    expect(client.approve).toHaveBeenCalledWith("p1", "t1");
  });

  it("Pause from the banner is immediate", async () => {
    const user = userEvent.setup();
    const client = fake();
    render(<Harness events={[ev({})]} client={client} />);
    await user.click(screen.getByRole("button", { name: /^pause$/i }));
    await settle();
    expect(client.pause).toHaveBeenCalledWith("p1");
  });

  it("the project link focuses the project", async () => {
    const user = userEvent.setup();
    const onSelectProject = vi.fn();
    render(
      <Harness
        events={[ev({ project: "p9", task: "tt" })]}
        client={fake()}
        onSelectProject={onSelectProject}
      />,
    );
    await user.click(screen.getByRole("button", { name: /p9·tt/i }));
    expect(onSelectProject).toHaveBeenCalledWith("p9");
  });

  it("keeps only the latest intervention per project", () => {
    render(
      <Harness
        events={[
          ev({ id: "a", project: "p1", task: "old" }),
          ev({ id: "b", project: "p1", task: "new" }),
          ev({ id: "c", project: "p2", task: "x" }),
        ]}
        client={fake()}
      />,
    );
    // p1 row reflects the latest task ("new"), and there are two project rows.
    expect(screen.getByRole("button", { name: /p1·new/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /p1·old/i })).toBeNull();
    expect(screen.getByRole("button", { name: /p2·x/i })).toBeInTheDocument();
  });
});
