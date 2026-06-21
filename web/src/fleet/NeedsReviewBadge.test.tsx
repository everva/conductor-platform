// NeedsReviewBadge tests (redesign E4). The shell's persistent review signal: it
// stays silent when nothing awaits the director (a signal, not chrome), announces
// a live count when work is waiting, pluralises correctly, and routes to the
// review queue on click.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NeedsReviewBadge } from "./NeedsReviewBadge.tsx";

describe("NeedsReviewBadge", () => {
  it("renders nothing when the review queue is empty", () => {
    const { container } = render(<NeedsReviewBadge count={0} onReview={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("announces a singular count as an accessible status", () => {
    render(<NeedsReviewBadge count={1} onReview={vi.fn()} />);
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("1 task needs your review");
  });

  it("pluralises the count", () => {
    render(<NeedsReviewBadge count={3} onReview={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("3 tasks need your review");
  });

  it("routes to the review queue on click", async () => {
    const user = userEvent.setup({ delay: null });
    const onReview = vi.fn();
    render(<NeedsReviewBadge count={2} onReview={onReview} />);
    await user.click(screen.getByRole("button", { name: /review/i }));
    expect(onReview).toHaveBeenCalledTimes(1);
  });
});
