// EventTicker: a lightweight tail of the live event stream so the dashboard feels
// alive. It shows the most recent N events (newest first) with their phase/kind
// and highlights `intervention-needed` distinctly. The full event-stream UI is
// 3B-2; this stays intentionally minimal.
import { KIND_INTERVENTION_NEEDED } from "../types/events.gen.ts";
import type { Event } from "../api/types.ts";
import { shortTime } from "./format.ts";

export interface EventTickerProps {
  events: Event[];
  // limit caps how many recent events are shown. Defaults to 12.
  limit?: number;
}

export function EventTicker({ events, limit = 12 }: EventTickerProps) {
  // Newest first, capped.
  const recent = events.slice(-limit).reverse();

  return (
    <section className="fleet-panel" aria-label="Recent events">
      <div className="fleet-panel-head">
        <h2>Recent events</h2>
        <span className="muted">{events.length}</span>
      </div>
      <div className="fleet-panel-body fleet-ticker">
        {recent.length === 0 ? (
          <p className="fleet-empty">No events yet.</p>
        ) : (
          recent.map((e) => {
            const intervention = e.kind === KIND_INTERVENTION_NEEDED;
            return (
              <div
                key={e.id}
                className={`fleet-ticker-row${intervention ? " intervention" : ""}`}
              >
                <span
                  className={`fleet-ticker-kind${intervention ? " intervention" : ""}`}
                >
                  {e.phase}/{e.kind}
                </span>
                <span className="mono muted">
                  {e.project}
                  {e.task ? `·${e.task}` : ""}
                </span>
                <span className="fleet-ticker-time">{shortTime(e.ts)}</span>
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}
