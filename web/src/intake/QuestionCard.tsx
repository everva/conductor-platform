// QuestionCard: the AskUserQuestion-style clarifying turn (Faz-Q / Q3c, ADR-0047) —
// when the distiller is too unsure to draft scenarios it asks SPECIFIC multiple-choice
// questions (Claude Code's "ask, don't assume") and the director answers here instead
// of free-typing. Each question shows a short header chip + the question, its 2-4
// options (label + what choosing it means), and — exactly like Claude Code — an
// auto-added "Other" free-text choice. multi_select renders checkboxes, else radios.
//
// On Submit it emits structured {question, answer} pairs (the selected option labels,
// comma-joined, plus any "Other" text); IntakeChat folds those into the conversation
// as a director turn and re-distills. Token-free — pure UI over the gateway's data.
import { useState } from "react";
import type { Question } from "../api/types.ts";

// QuestionAnswer is one resolved answer: the question text and the director's choice
// (option labels comma-joined, plus any free-text "Other").
export interface QuestionAnswer {
  question: string;
  answer: string;
}

export interface QuestionCardProps {
  // questions is the clarifying set from the distiller (1-4, gateway-validated).
  questions: Question[];
  // onSubmit fires once every question has a resolved answer.
  onSubmit: (answers: QuestionAnswer[]) => void;
  // disabled locks the card while a re-distill is in flight.
  disabled?: boolean;
}

// OTHER is the sentinel label for the auto-added free-text choice (never a real option
// label — the gateway rejects an option literally named this is vanishingly unlikely,
// and it is namespaced to avoid collision).
const OTHER = "__other__";

export function QuestionCard({ questions, onSubmit, disabled = false }: QuestionCardProps) {
  // selected[i] = chosen option labels for question i (single-select keeps ≤1; the
  // OTHER sentinel may be among them). otherText[i] = the free-text for "Other".
  const [selected, setSelected] = useState<string[][]>(() => questions.map(() => []));
  const [otherText, setOtherText] = useState<string[]>(() => questions.map(() => ""));

  function toggle(qi: number, label: string, multi: boolean) {
    setSelected((prev) => {
      const next = prev.map((s) => s.slice());
      if (multi) {
        const at = next[qi].indexOf(label);
        if (at >= 0) {
          next[qi].splice(at, 1);
        } else {
          next[qi].push(label);
        }
      } else {
        next[qi] = [label];
      }
      return next;
    });
  }

  function setOther(qi: number, value: string) {
    setOtherText((prev) => {
      const next = prev.slice();
      next[qi] = value;
      return next;
    });
  }

  // resolveAnswer is the comma-joined answer for question i, or "" if unanswered. The
  // OTHER sentinel contributes its free text (when non-blank), never the sentinel itself.
  function resolveAnswer(qi: number): string {
    const parts = selected[qi].filter((l) => l !== OTHER);
    if (selected[qi].includes(OTHER)) {
      const t = otherText[qi].trim();
      if (t !== "") {
        parts.push(t);
      }
    }
    return parts.join(", ");
  }

  const allAnswered = questions.every((_q, qi) => resolveAnswer(qi) !== "");

  function submit() {
    if (disabled || !allAnswered) {
      return;
    }
    onSubmit(questions.map((q, qi) => ({ question: q.question, answer: resolveAnswer(qi) })));
  }

  return (
    <div className="intake-questions fleet-panel" data-testid="question-card">
      <div className="fleet-panel-body intake-questions-body">
        {questions.map((q, qi) => {
          const inputType = q.multi_select ? "checkbox" : "radio";
          const otherChosen = selected[qi].includes(OTHER);
          return (
            <fieldset key={`${q.header}-${qi}`} className="intake-question">
              <legend className="intake-question-legend">
                <span className="intake-question-chip">{q.header}</span>
                <span className="intake-question-text">{q.question}</span>
              </legend>

              {q.options.map((opt) => (
                <label key={opt.label} className="intake-option">
                  <input
                    type={inputType}
                    name={`q-${qi}`}
                    checked={selected[qi].includes(opt.label)}
                    disabled={disabled}
                    onChange={() => toggle(qi, opt.label, q.multi_select)}
                  />
                  <span className="intake-option-body">
                    <span className="intake-option-label">{opt.label}</span>
                    <span className="intake-option-desc">{opt.description}</span>
                  </span>
                </label>
              ))}

              <div className="intake-option intake-option-other">
                <label className="intake-option-pick">
                  <input
                    type={inputType}
                    name={`q-${qi}`}
                    checked={otherChosen}
                    disabled={disabled}
                    onChange={() => toggle(qi, OTHER, q.multi_select)}
                  />
                  <span className="intake-option-label">Other</span>
                </label>
                <input
                  type="text"
                  className="intake-other-input"
                  aria-label={`Other answer for ${q.header}`}
                  placeholder="Type your own answer…"
                  value={otherText[qi]}
                  disabled={disabled || !otherChosen}
                  onChange={(e) => setOther(qi, e.target.value)}
                />
              </div>
            </fieldset>
          );
        })}

        <div className="intake-actions">
          <button
            type="button"
            className="fleet-btn primary"
            disabled={disabled || !allAnswered}
            onClick={submit}
          >
            Submit answers
          </button>
        </div>
      </div>
    </div>
  );
}
