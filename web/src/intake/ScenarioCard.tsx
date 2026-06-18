// ScenarioCard: renders ONE proposed scenario (3B-4b) for the human review step of
// the intake flow. It shows the fields the human gates on — id, title, lane, tier,
// deps, acceptance criteria — and HIGHLIGHTS the hidden holdout ref, which is the
// repo-EXTERNAL hidden acceptance check (ADR-0018): it must never be a repo-relative
// path, and the human verifies it points at the external locator before approving.
// This is presentation-only; it never mutates and POSTs nothing.
import type { Scenario } from "../api/types.ts";

export interface ScenarioCardProps {
  scenario: Scenario;
}

export function ScenarioCard({ scenario }: ScenarioCardProps) {
  const deps = scenario.Deps ?? [];
  const acceptance = scenario.Acceptance ?? [];

  return (
    <article className="intake-scenario" data-testid="scenario-card">
      <header className="intake-scenario-head">
        <span className="intake-scenario-id mono">{scenario.ID}</span>
        <span className="intake-scenario-title">{scenario.Title}</span>
      </header>

      <div className="intake-scenario-meta">
        <span className="chip" data-testid="scenario-lane">
          lane: {scenario.Lane}
        </span>
        <span className="chip" data-testid="scenario-tier">
          tier: {scenario.Tier}
        </span>
        {deps.length > 0 && (
          <span className="intake-deps">
            deps:{" "}
            {deps.map((d) => (
              <span key={d} className="chip muted">
                {d}
              </span>
            ))}
          </span>
        )}
      </div>

      {acceptance.length > 0 && (
        <div className="intake-scenario-section">
          <span className="intake-label">Acceptance</span>
          <ul className="intake-acceptance">
            {acceptance.map((a, i) => (
              <li key={i}>{a}</li>
            ))}
          </ul>
        </div>
      )}

      <div className="intake-holdout" data-testid="scenario-holdout">
        <span className="intake-holdout-tag">hidden holdout</span>
        <span className="intake-holdout-ref mono">{scenario.HoldoutRef}</span>
        <span className="intake-holdout-note muted">
          repo-external hidden acceptance check — verify before approving
        </span>
      </div>
    </article>
  );
}
