// ConfirmDialog: a small, dependency-free modal confirm for consequential control
// actions (3B-3). Used to gate Abort (destructive: cancels in-flight work) and
// Approve (triggers a real merge). It is a controlled component — the parent owns
// the pending state and supplies the copy + the confirm/cancel callbacks. Escape and
// the backdrop cancel; the confirm button is auto-focused so Enter confirms.
import { useEffect, useRef } from "react";
import type { PendingConfirm } from "./useFleetControls.ts";

export interface ConfirmDialogProps {
  pending: PendingConfirm | null;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({ pending, onConfirm, onCancel }: ConfirmDialogProps) {
  const confirmRef = useRef<HTMLButtonElement>(null);

  // Focus the affirmative action and wire Escape-to-cancel while the dialog is open.
  useEffect(() => {
    if (pending === null) {
      return;
    }
    confirmRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onCancel();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [pending, onCancel]);

  if (pending === null) {
    return null;
  }

  return (
    <div
      className="fleet-modal-backdrop"
      onClick={onCancel}
      data-testid="confirm-backdrop"
    >
      <div
        className="fleet-modal"
        role="dialog"
        aria-modal="true"
        aria-label={pending.title}
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="fleet-modal-title">{pending.title}</h3>
        <p className="fleet-modal-body">{pending.body}</p>
        <div className="fleet-modal-actions">
          <button type="button" className="fleet-btn" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            ref={confirmRef}
            className={`fleet-btn ${pending.tone === "danger" ? "danger" : "primary"}`}
            onClick={onConfirm}
          >
            {pending.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
