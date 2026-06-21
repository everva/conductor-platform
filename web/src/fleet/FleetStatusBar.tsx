// FleetStatusBar: the dashboard's top bar. Shows the fleet aggregate counts (from
// GET /status) plus the live WS connection state and the time of the last event.
import type { StatusSummary } from "../api/types.ts";
import type { StreamState } from "../api/useEventStream.ts";
import { shortTime } from "./format.ts";
import { Button, StatusDot } from "../ui/index.ts";
import type { DotTone } from "../ui/index.ts";

export interface FleetStatusBarProps {
  status: StatusSummary | null;
  streamState: StreamState;
  lastEventAt: number | null;
  onRefresh: () => void;
}

const CONN_LABEL: Record<StreamState, string> = {
  open: "live",
  connecting: "connecting…",
  closed: "offline",
};

const CONN_TONE: Record<StreamState, DotTone> = {
  open: "success",
  connecting: "warn",
  closed: "danger",
};

export function FleetStatusBar({
  status,
  streamState,
  lastEventAt,
  onRefresh,
}: FleetStatusBarProps) {
  const activeLeases = status?.leases.length ?? 0;
  return (
    <div className="fleet-statusbar" role="region" aria-label="Fleet status">
      <div className="fleet-stat">
        <span className="fleet-stat-label">Projects</span>
        <span className="fleet-stat-value">{status?.projects ?? "—"}</span>
      </div>
      <div className="fleet-stat">
        <span className="fleet-stat-label">Hosts</span>
        <span className="fleet-stat-value">{status?.hosts ?? "—"}</span>
      </div>
      <div className="fleet-stat">
        <span className="fleet-stat-label">Active leases</span>
        <span className="fleet-stat-value">{status ? activeLeases : "—"}</span>
      </div>
      <div className="fleet-stat">
        <span className="fleet-stat-label">Last event</span>
        <span className="fleet-stat-value small">{shortTime(lastEventAt)}</span>
      </div>

      <div className="fleet-spacer" />

      <span className="fleet-conn" aria-label={`Stream ${streamState}`}>
        <StatusDot tone={CONN_TONE[streamState]} pulse={streamState === "open"} />
        {CONN_LABEL[streamState]}
      </span>
      <Button variant="secondary" size="sm" onClick={onRefresh}>
        Refresh
      </Button>
    </div>
  );
}
