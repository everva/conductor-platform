// QuestionCard tests (Q3c, ADR-0047 — AskUserQuestion-style clarifying turn). We drive
// the real component and assert:
//   * the header chip, question, options (label + description), and the auto-added
//     "Other" choice all render;
//   * Submit is disabled until every question has an answer;
//   * single-select emits the chosen label; multi-select comma-joins choices;
//   * the "Other" free text becomes the answer when chosen.
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QuestionCard } from "./QuestionCard.tsx";
import type { Question } from "../api/types.ts";

afterEach(() => {
  vi.restoreAllMocks();
});

function singleQ(over: Partial<Question> = {}): Question {
  return {
    question: "Which datastore should it use?",
    header: "Datastore",
    multi_select: false,
    options: [
      { label: "Postgres", description: "Relational, the platform default." },
      { label: "Redis", description: "In-memory key-value cache." },
    ],
    ...over,
  };
}

describe("QuestionCard", () => {
  it("renders the header chip, question, options with descriptions, and an Other choice", () => {
    render(<QuestionCard questions={[singleQ()]} onSubmit={vi.fn()} />);
    expect(screen.getByText("Datastore")).toBeInTheDocument();
    expect(screen.getByText("Which datastore should it use?")).toBeInTheDocument();
    expect(screen.getByText("Postgres")).toBeInTheDocument();
    expect(screen.getByText("Relational, the platform default.")).toBeInTheDocument();
    // Claude Code auto-adds an "Other" free-text choice.
    expect(screen.getByRole("radio", { name: "Other" })).toBeInTheDocument();
    expect(screen.getByLabelText("Other answer for Datastore")).toBeDisabled();
  });

  it("single-select: Submit is disabled until a choice is made, then emits the label", async () => {
    const user = userEvent.setup({ delay: null });
    const onSubmit = vi.fn();
    render(<QuestionCard questions={[singleQ()]} onSubmit={onSubmit} />);

    const submit = screen.getByRole("button", { name: "Submit answers" });
    expect(submit).toBeDisabled();

    await user.click(screen.getByRole("radio", { name: /Postgres/ }));
    expect(submit).not.toBeDisabled();

    await user.click(submit);
    expect(onSubmit).toHaveBeenCalledWith([
      { question: "Which datastore should it use?", answer: "Postgres" },
    ]);
  });

  it("Other: choosing Other and typing emits the free text as the answer", async () => {
    const user = userEvent.setup({ delay: null });
    const onSubmit = vi.fn();
    render(<QuestionCard questions={[singleQ()]} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("radio", { name: "Other" }));
    const other = screen.getByLabelText("Other answer for Datastore");
    expect(other).not.toBeDisabled();
    await user.type(other, "DynamoDB");

    await user.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(onSubmit).toHaveBeenCalledWith([
      { question: "Which datastore should it use?", answer: "DynamoDB" },
    ]);
  });

  it("multi-select: multiple checkboxes combine comma-joined in pick order", async () => {
    const user = userEvent.setup({ delay: null });
    const onSubmit = vi.fn();
    const q = singleQ({
      question: "Which layers does this touch?",
      header: "Layers",
      multi_select: true,
      options: [
        { label: "API", description: "the backend service" },
        { label: "Web", description: "the frontend" },
        { label: "Mobile", description: "the native app" },
      ],
    });
    render(<QuestionCard questions={[q]} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("checkbox", { name: /API/ }));
    await user.click(screen.getByRole("checkbox", { name: /Mobile/ }));
    await user.click(screen.getByRole("button", { name: "Submit answers" }));

    expect(onSubmit).toHaveBeenCalledWith([
      { question: "Which layers does this touch?", answer: "API, Mobile" },
    ]);
  });

  it("requires every question answered before Submit enables", async () => {
    const user = userEvent.setup({ delay: null });
    const onSubmit = vi.fn();
    const q2 = singleQ({ question: "Which risk tier?", header: "Tier" });
    render(<QuestionCard questions={[singleQ(), q2]} onSubmit={onSubmit} />);

    const submit = screen.getByRole("button", { name: "Submit answers" });
    // Answer only the first question.
    await user.click(screen.getAllByRole("radio", { name: /Postgres/ })[0]);
    expect(submit).toBeDisabled();
  });
});
