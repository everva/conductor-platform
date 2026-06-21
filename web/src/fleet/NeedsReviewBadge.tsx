// NeedsReviewBadge: the cockpit shell's persistent review signal (agent-native
// redesign, E4 director powers). Wherever a director is — the board, another tab,
// or deep inside a session — this surfaces how many tasks are waiting on THEM
// (awaiting-approval + blocked) and takes them to the review queue in one click.
// It renders nothing when the queue is empty, so it is a signal, never chrome. The
// count comes from needsReviewCount — the SAME bucketing the board's Needs-Review
// column uses — so the shell badge and the column never disagree. role="status"
// + aria-live makes it announce when review work appears (an accessible nudge).
import { ArrowRight } from "lucide-react";
import { StatusDot } from "../ui/index.ts";
import "./needsReview.css";

export interface NeedsReviewBadgeProps {
  count: number;
  // onReview routes to the review queue (the board's Needs-Review lane).
  onReview: () => void;
}

export function NeedsReviewBadge({ count, onReview }: NeedsReviewBadgeProps) {
  if (count <= 0) {
    return null;
  }
  const label =
    count === 1 ? "1 task needs your review" : `${count} tasks need your review`;
  return (
    <div className="needs-review" role="status" aria-live="polite">
      <StatusDot tone="warn" pulse className="needs-review-dot" />
      <span className="needs-review-label">{label}</span>
      <button type="button" className="needs-review-btn" onClick={onReview}>
        Review <ArrowRight size={13} strokeWidth={2.4} />
      </button>
    </div>
  );
}
