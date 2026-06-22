// IntakeChat tests (3B-4b). The IntakeClient is a fake (no network); we drive the
// real component through the converse→distill→review→edit→approve flow and assert:
//   * typing a conversation + Distill renders the proposed scenarios (id/lane/tier/
//     holdout) and POPULATES the editable YAML textarea;
//   * editing the YAML then Approve (confirm-gated) calls client.intake(projectId,
//     <EDITED yaml>) — NOT the original — and shows the created task ids;
//   * a 422 (DistillNoScenariosError) shows the "add more detail" GUIDANCE, not a
//     crash, and clears the spinner (never-fabricate surfaced honestly);
//   * an invalid-YAML 400 from intake shows the inline parse error;
//   * a 401 from distill triggers onUnauthorized.
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IntakeChat } from "./IntakeChat.tsx";
import type { IntakeClient } from "./IntakeChat.tsx";
import { ApiError, DistillNoScenariosError } from "../api/client.ts";
import type { DistillResult, IntakeResult, Project, Scenario } from "../api/types.ts";

afterEach(() => {
  vi.restoreAllMocks();
});

function project(over: Partial<Project> = {}): Project {
  return {
    id: "proj-x",
    repo: "owner/x",
    base_branch: "develop",
    host_id: "",
    readiness: "ready",
    recipe_pointer: "",
    governance_policy: "auto",
    paused: false,
    ...over,
  };
}

function scenario(over: Partial<Scenario> = {}): Scenario {
  return {
    id: "A-1",
    title: "First distilled task",
    lane: "backend",
    tier: "T1",
    deps: [],
    acceptance: ["does the first thing"],
    hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go",
    ...over,
  };
}

const PROPOSAL: DistillResult = {
  scenarios: [scenario()],
  yaml: "id: A-1\ntitle: First distilled task\n",
};

// fakeClient builds an IntakeClient with stubbed distill/intake outcomes.
function fakeClient(over: Partial<IntakeClient> = {}): IntakeClient {
  return {
    distill: vi.fn((): Promise<DistillResult> => Promise.resolve(PROPOSAL)),
    intake: vi.fn(
      (): Promise<IntakeResult> =>
        Promise.resolve({ created: ["A-1"], skipped: [] }),
    ),
    ...over,
  };
}

describe("IntakeChat", () => {
  it("distill renders the proposed scenario (id/lane/tier/holdout) and fills the YAML", async () => {
    const user = userEvent.setup({ delay: null });
    const client = fakeClient();
    render(
      <IntakeChat
        projects={[project()]}
        client={client}
        onUnauthorized={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("Message"), "build the auth flow");
    await user.click(screen.getByRole("button", { name: "Send" }));

    const card = await screen.findByTestId("scenario-card");
    expect(within(card).getByText("A-1")).toBeInTheDocument();
    expect(within(card).getByTestId("scenario-lane")).toHaveTextContent("backend");
    expect(within(card).getByTestId("scenario-tier")).toHaveTextContent("T1");
    expect(within(card).getByTestId("scenario-holdout")).toHaveTextContent(
      "store://holdouts/A-1/holdout_test.go",
    );

    const yaml = screen.getByLabelText("Intake YAML") as HTMLTextAreaElement;
    expect(yaml.value).toBe(PROPOSAL.yaml);
    expect(client.distill).toHaveBeenCalledWith("proj-x", "build the auth flow");
  });

  it("multi-turn: a second message re-distills with the WHOLE conversation accumulated (Q3b)", async () => {
    const user = userEvent.setup({ delay: null });
    // Turn 1 → 422 (the assistant asks for more detail); turn 2 → a proposal. Proves the thread
    // accumulates context across turns and the never-fabricate guidance is a clarifying reply.
    const distill = vi
      .fn<IntakeClient["distill"]>()
      .mockRejectedValueOnce(new DistillNoScenariosError("need more detail"))
      .mockResolvedValueOnce(PROPOSAL);
    const client = fakeClient({ distill });
    render(<IntakeChat projects={[project()]} client={client} onUnauthorized={vi.fn()} />);

    await user.type(screen.getByLabelText("Message"), "build auth");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText(/Add more detail/i)).toBeInTheDocument();
    expect(screen.queryByTestId("scenario-card")).not.toBeInTheDocument();

    await user.type(
      screen.getByLabelText("Message"),
      "acceptance: login works; lane backend; tier T1",
    );
    await user.click(screen.getByRole("button", { name: "Send" }));

    // The proposal lands, AND the second distill got BOTH turns joined (accumulated context).
    expect(await screen.findByTestId("scenario-card")).toBeInTheDocument();
    expect(distill).toHaveBeenNthCalledWith(
      2,
      "proj-x",
      "build auth\n\nacceptance: login works; lane backend; tier T1",
    );
  });

  it("editing the YAML then Approve posts the EDITED yaml and shows created ids", async () => {
    const user = userEvent.setup({ delay: null });
    const client = fakeClient();
    render(
      <IntakeChat
        projects={[project()]}
        client={client}
        onUnauthorized={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("Message"), "x");
    await user.click(screen.getByRole("button", { name: "Send" }));

    const yaml = await screen.findByLabelText("Intake YAML");
    await user.clear(yaml);
    await user.type(yaml, "id: A-9");

    await user.click(screen.getByRole("button", { name: "Approve & add to ledger" }));
    // Confirm gate.
    await user.click(screen.getByRole("button", { name: "Approve & add" }));

    expect(client.intake).toHaveBeenCalledWith("proj-x", "id: A-9");
    const result = await screen.findByTestId("intake-result");
    expect(within(result).getByTestId("created-id")).toHaveTextContent("A-1");
  });

  it("a 422 distill shows the add-more-detail guidance and clears the spinner", async () => {
    const user = userEvent.setup({ delay: null });
    const client = fakeClient({
      distill: vi.fn(() =>
        Promise.reject(
          new DistillNoScenariosError("no scenarios could be distilled"),
        ),
      ),
    });
    render(
      <IntakeChat
        projects={[project()]}
        client={client}
        onUnauthorized={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("Message"), "vague");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText(/Add more detail/i)).toBeInTheDocument();
    // No proposal rendered, and the spinner cleared: the button is back to "Send" (not "Sending…")
    // and the inline "Distilling…" indicator is gone. (It's disabled only because Send clears the
    // input — the conversational flow; typing a follow-up re-enables it.)
    expect(screen.queryByTestId("scenario-card")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
    expect(screen.queryByText("Distilling…")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Message"), "more");
    expect(screen.getByRole("button", { name: "Send" })).not.toBeDisabled();
  });

  it("an invalid-YAML 400 from intake shows the inline parse error", async () => {
    const user = userEvent.setup({ delay: null });
    const client = fakeClient({
      intake: vi.fn(() =>
        Promise.reject(new ApiError(400, "yaml: line 2: mapping values not allowed")),
      ),
    });
    render(
      <IntakeChat
        projects={[project()]}
        client={client}
        onUnauthorized={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("Message"), "x");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByLabelText("Intake YAML");
    await user.click(screen.getByRole("button", { name: "Approve & add to ledger" }));
    await user.click(screen.getByRole("button", { name: "Approve & add" }));

    expect(
      await screen.findByText(/Invalid YAML: yaml: line 2/i),
    ).toBeInTheDocument();
  });

  it("a 401 from distill triggers onUnauthorized", async () => {
    const user = userEvent.setup({ delay: null });
    const onUnauthorized = vi.fn();
    const client = fakeClient({
      distill: vi.fn(() => Promise.reject(new ApiError(401, "unauthorized"))),
    });
    render(
      <IntakeChat
        projects={[project()]}
        client={client}
        onUnauthorized={onUnauthorized}
      />,
    );

    await user.type(screen.getByLabelText("Message"), "x");
    await user.click(screen.getByRole("button", { name: "Send" }));

    await vi.waitFor(() => expect(onUnauthorized).toHaveBeenCalledTimes(1));
  });

  it("Write spec directly opens the YAML editor (no distill) and dispatches the authored spec", async () => {
    const user = userEvent.setup({ delay: null });
    const client = fakeClient();
    render(
      <IntakeChat projects={[project()]} client={client} onUnauthorized={vi.fn()} />,
    );

    // The claude-free path: author straight into the authoritative editor.
    await user.click(screen.getByRole("button", { name: "Write spec directly" }));
    const yaml = screen.getByLabelText("Intake YAML") as HTMLTextAreaElement;
    expect(yaml.value).toContain("id: NEW-1");
    expect(client.distill).not.toHaveBeenCalled();

    // Approve dispatches the EXACT authored YAML to /intake (confirm-gated).
    await user.click(screen.getByRole("button", { name: "Approve & add to ledger" }));
    await user.click(screen.getByRole("button", { name: "Approve & add" }));
    expect(client.intake).toHaveBeenCalledWith(
      "proj-x",
      expect.stringContaining("id: NEW-1"),
    );
  });
});
