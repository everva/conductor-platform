// HostsPanel: one row per host (id, capability chips, heartbeat freshness). The
// heartbeat_age_seconds from the gateway is rendered as a human age with a status
// color: fresh (recent), stale (> threshold), or never (no heartbeat recorded).
import type { Host } from "../api/types.ts";
import { heartbeatFreshness } from "./format.ts";

export interface HostsPanelProps {
  hosts: Host[];
}

export function HostsPanel({ hosts }: HostsPanelProps) {
  return (
    <section className="fleet-panel" aria-label="Hosts">
      <div className="fleet-panel-head">
        <h2>Hosts</h2>
        <span className="muted">{hosts.length}</span>
      </div>
      <div className="fleet-panel-body">
        {hosts.length === 0 ? (
          <p className="fleet-empty">No hosts registered.</p>
        ) : (
          <table className="fleet-table">
            <thead>
              <tr>
                <th>Host</th>
                <th>Capabilities</th>
                <th>Heartbeat</th>
              </tr>
            </thead>
            <tbody>
              {hosts.map((h) => {
                const hb = heartbeatFreshness(h.heartbeat_age_seconds, h.last_heartbeat);
                return (
                  <tr key={h.id}>
                    <td className="mono">{h.id}</td>
                    <td>
                      {h.capabilities.length === 0 ? (
                        <span className="muted">—</span>
                      ) : (
                        h.capabilities.map((c) => (
                          <span key={c} className="chip">
                            {c}
                          </span>
                        ))
                      )}
                    </td>
                    <td>
                      <span className={`hb ${hb.status}`}>{hb.label}</span>
                      {hb.status === "stale" && (
                        <span className="chip" style={{ marginLeft: "0.4rem" }}>
                          stale
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
