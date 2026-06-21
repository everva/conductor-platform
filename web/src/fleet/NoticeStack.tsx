// NoticeStack: the non-fatal toast/inline notice queue for control actions (3B-3).
// Each notice is a one-shot, dismissable message: "ok" (confirmed mutation), "warn"
// (benign 409 carrying the gateway {error}, e.g. "no task running to abort"), or
// "error" (unexpected failure). It is presentation-only — the parent owns the queue
// (useFleetControls) and dismissal. role="status" keeps it polite for screen readers.
import { X } from "lucide-react";
import type { ControlNotice } from "./controls.ts";

export interface NoticeStackProps {
  notices: ControlNotice[];
  onDismiss: (id: number) => void;
}

export function NoticeStack({ notices, onDismiss }: NoticeStackProps) {
  if (notices.length === 0) {
    return null;
  }
  return (
    <div className="fleet-notices" role="status" aria-live="polite">
      {notices.map((n) => (
        <div key={n.id} className={`fleet-notice ${n.tone}`} data-testid="fleet-notice">
          <span className="fleet-notice-msg">{n.message}</span>
          <button
            type="button"
            className="fleet-notice-dismiss"
            aria-label="Dismiss"
            onClick={() => onDismiss(n.id)}
          >
            <X size={15} strokeWidth={2.4} />
          </button>
        </div>
      ))}
    </div>
  );
}
