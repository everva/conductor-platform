// Component test for the agent-native Session view (E2): with the task's events
// (history) + scenario injected, it renders the SPEC (acceptance), the deterministic
// Verifier verdict (per-gate checks + MERGE-READY), the diff, the activity timeline,
// and a confirm-gated Approve; the back button returns to the board.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionView } from "./SessionView.tsx";
import type { Event, Scenario, Task } from "../api/types.ts";
import type { EventTransport } from "../api/useEventStream.ts";
import type { FleetControls } from "../fleet/useFleetControls.ts";

// A no-op live transport: no WS in jsdom; the timeline/verdict/diff come from the
// injected history backfill.
const noopTransport: EventTransport = { subscribe: () => ({ close() {} }) };

const TASK: Task = {
  id: "T-1", project_id: "web-shop", lane: "web", tier: "T3", status: "awaiting-approval",
  requires: [], deps: [], branch: "conductor/T-1", scenario_id: "S-1",
  retry_count: 0, abort_requested: false, approved: false,
};

const SCENARIO: Scenario = {
  id: "S-1", title: "Ship the Feature helper", lane: "web", tier: "T3", deps: [],
  acceptance: ["package app exposes Feature()", "hidden holdout green"],
  hidden_holdout_ref: "store://holdouts/S-1/h.go",
};

function ev(p: Partial<Event> & Pick<Event, "id" | "kind" | "phase">): Event {
  return { ts: "2026-06-20T10:00:00Z", project: "web-shop", task: "T-1", payload: {}, ...p };
}

const EVENTS: Event[] = [
  ev({ id: "e1", ts: "2026-06-20T10:00:00Z", kind: "started", phase: "develop" }),
  ev({ id: "e2", ts: "2026-06-20T10:04:00Z", kind: "started", phase: "verify" }),
  ev({
    id: "e3", ts: "2026-06-20T10:05:00Z", kind: "decision", phase: "review",
    payload: {
      result: "pass",
      checks: [
        { name: "go build", result: "pass", evidence: "exit 0" },
        { name: "hidden holdout", result: "pass", evidence: "exit 0" },
      ],
    },
  }),
  ev({
    id: "e4", ts: "2026-06-20T10:05:01Z", kind: "diff", phase: "review",
    payload: {
      branch: "conductor/T-1", base: "develop", truncated: false,
      patch: "@@ -0,0 +1 @@\n+func Feature() string { return \"shipped\" }\n",
      files: [{ path: "feature.go", status: "A", additions: 6, deletions: 0 }],
    },
  }),
];

describe("SessionView", () => {
  it("renders spec + Verifier verdict + diff + timeline + Approve, and back works", async () => {
    const onBack = vi.fn();
    const requestApprove = vi.fn();
    const controls = {
      isTaskBusy: () => false,
      requestApprove,
      requestAbort: vi.fn(),
    } as unknown as FleetControls;

    render(
      <SessionView
        task={TASK}
        token="t"
        onBack={onBack}
        onUnauthorized={() => {}}
        controls={controls}
        makeScenarioClient={() => ({ listScenarios: async () => [SCENARIO] })}
        makeHistory={() => ({ listEvents: async () => EVENTS })}
        eventTransport={noopTransport}
      />,
    );

    // SPEC (from the injected scenario).
    expect(await screen.findByText("Ship the Feature helper")).toBeInTheDocument();
    expect(screen.getByText("package app exposes Feature()")).toBeInTheDocument();

    // VERIFIER VERDICT (from the decision event's checks).
    expect(await screen.findByText("go build")).toBeInTheDocument();
    expect(screen.getByText("hidden holdout")).toBeInTheDocument();
    expect(screen.getByText(/MERGE-READY/)).toBeInTheDocument();

    // DIFF.
    expect(screen.getByText("feature.go")).toBeInTheDocument();

    // ACTIVITY timeline.
    expect(screen.getByText("Develop · started")).toBeInTheDocument();
    expect(screen.getByText("Review · verdict")).toBeInTheDocument();

    // Approve (held task) is confirm-gated via controls.
    await userEvent.click(screen.getByRole("button", { name: /approve/i }));
    expect(requestApprove).toHaveBeenCalledWith("web-shop", "T-1");

    // Back returns to the board.
    await userEvent.click(screen.getByRole("button", { name: /command center/i }));
    expect(onBack).toHaveBeenCalled();
  });

  it("replays the verdict + diff as of a selected timeline entry, then returns to live (E4)", async () => {
    const user = userEvent.setup();
    render(
      <SessionView
        task={TASK}
        token="t"
        onBack={vi.fn()}
        onUnauthorized={() => {}}
        makeScenarioClient={() => ({ listScenarios: async () => [SCENARIO] })}
        makeHistory={() => ({ listEvents: async () => EVENTS })}
        eventTransport={noopTransport}
      />,
    );

    // Live: the latest verdict + diff are shown, and the timeline carries summaries.
    expect(await screen.findByText(/MERGE-READY/)).toBeInTheDocument();
    expect(screen.getByText("feature.go")).toBeInTheDocument();
    expect(screen.getByText(/^1 file \+6\/.0$/)).toBeInTheDocument(); // diff summary (E4)

    // Scrub back to the first event (develop started, 10:00) — before the gate
    // decided and before any diff existed. The panels replay that empty state.
    await user.click(screen.getByText("Develop · started"));
    expect(screen.getByText(/Replaying as of 10:00:00/)).toBeInTheDocument();
    expect(screen.queryByText(/MERGE-READY/)).not.toBeInTheDocument();
    expect(screen.getByText(/Awaiting the gate/)).toBeInTheDocument();
    expect(screen.queryByText("feature.go")).not.toBeInTheDocument();

    // Return to live restores the latest verdict + diff.
    await user.click(screen.getByRole("button", { name: /return to live/i }));
    expect(screen.getByText(/MERGE-READY/)).toBeInTheDocument();
    expect(screen.getByText("feature.go")).toBeInTheDocument();
  });
});
