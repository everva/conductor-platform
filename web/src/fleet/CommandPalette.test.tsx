// CommandPalette tests (redesign E4). We drive the real overlay (pure palette.ts
// model under it) and assert the director keyboard flow: the input autofocuses,
// the list renders nav + session rows, typing filters, ↑/↓ moves the highlight,
// Enter runs the highlighted item, a click runs that item, and Esc closes.
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommandPalette } from "./CommandPalette.tsx";
import type { Task } from "../api/types.ts";

function task(over: Partial<Task> = {}): Task {
  return {
    id: "T-1",
    project_id: "web-shop",
    lane: "backend",
    tier: "T1",
    status: "running",
    requires: [],
    deps: [],
    branch: "",
    scenario_id: "",
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...over,
  };
}

const TASKS: Record<string, Task[]> = {
  "web-shop": [task({ id: "WEB-1", status: "awaiting-approval" })],
};

describe("CommandPalette", () => {
  it("autofocuses the query input and lists nav + session rows", () => {
    render(
      <CommandPalette tasksByProject={TASKS} onAction={vi.fn()} onClose={vi.fn()} />,
    );
    expect(screen.getByLabelText("Command palette query")).toHaveFocus();
    const list = screen.getByRole("listbox", { name: "Commands" });
    expect(within(list).getByText("Go to Board")).toBeInTheDocument();
    expect(within(list).getByText("+ New work")).toBeInTheDocument();
    // The session row is the task id with project · status context.
    expect(within(list).getByText("WEB-1")).toBeInTheDocument();
    expect(within(list).getByText("web-shop · awaiting-approval")).toBeInTheDocument();
  });

  it("typing filters the list to the matching rows", async () => {
    const user = userEvent.setup({ delay: null });
    render(
      <CommandPalette tasksByProject={TASKS} onAction={vi.fn()} onClose={vi.fn()} />,
    );
    await user.type(screen.getByLabelText("Command palette query"), "web-1");
    expect(screen.getByText("WEB-1")).toBeInTheDocument();
    expect(screen.queryByText("Go to Board")).not.toBeInTheDocument();
  });

  it("ArrowDown then Enter runs the highlighted item", async () => {
    const user = userEvent.setup({ delay: null });
    const onAction = vi.fn();
    render(
      <CommandPalette tasksByProject={TASKS} onAction={onAction} onClose={vi.fn()} />,
    );
    // Default highlight is the first row (Go to Board); one ArrowDown → Go to Fleet.
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onAction).toHaveBeenCalledWith({ type: "navigate", surface: "fleet" });
  });

  it("clicking a session row opens that session with the exact task", async () => {
    const user = userEvent.setup({ delay: null });
    const onAction = vi.fn();
    render(
      <CommandPalette tasksByProject={TASKS} onAction={onAction} onClose={vi.fn()} />,
    );
    await user.click(screen.getByText("WEB-1"));
    expect(onAction).toHaveBeenCalledWith({
      type: "open-session",
      task: TASKS["web-shop"][0],
    });
  });

  it("Enter on an empty query runs +New work after filtering to it", async () => {
    const user = userEvent.setup({ delay: null });
    const onAction = vi.fn();
    render(
      <CommandPalette tasksByProject={TASKS} onAction={onAction} onClose={vi.fn()} />,
    );
    await user.type(screen.getByLabelText("Command palette query"), "new work");
    await user.keyboard("{Enter}");
    expect(onAction).toHaveBeenCalledWith({ type: "new-work" });
  });

  it("Escape closes the palette", async () => {
    const user = userEvent.setup({ delay: null });
    const onClose = vi.fn();
    render(
      <CommandPalette tasksByProject={TASKS} onAction={vi.fn()} onClose={onClose} />,
    );
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("a non-matching query shows the empty state", async () => {
    const user = userEvent.setup({ delay: null });
    render(
      <CommandPalette tasksByProject={TASKS} onAction={vi.fn()} onClose={vi.fn()} />,
    );
    await user.type(screen.getByLabelText("Command palette query"), "zzz-none");
    expect(screen.getByText("No matches.")).toBeInTheDocument();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
