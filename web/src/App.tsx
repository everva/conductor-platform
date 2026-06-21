// App is the cockpit root: AuthProvider → TokenGate → Shell. The Shell renders the
// fleet dashboard (3B-1) once authed — projects/hosts/tasks live over REST + WS,
// with heartbeat freshness and lease/paused/intervention surfacing. A 401 from the
// dashboard's data layer signs the session out (back to the token gate).
import { AuthProvider } from "./auth/AuthProvider.tsx";
import { TokenGate } from "./auth/TokenGate.tsx";
import { useAuth } from "./auth/context.ts";
// The web App is the first consumer THROUGH the shared cockpit barrel (ADR-0028);
// the fork webview (4B) is the second. Auth/TokenGate stay imported directly from
// ./auth — they are web-only bootstrap, intentionally NOT in the barrel.
import { FleetDashboard } from "./cockpit.ts";

export function App() {
  return (
    <AuthProvider>
      <TokenGate>
        <Shell />
      </TokenGate>
    </AuthProvider>
  );
}

function Shell() {
  const { token, signOut } = useAuth();

  return (
    <div>
      <header className="app-bar">
        <div className="app-brand">
          <span className="app-mark" aria-hidden="true">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
              <g stroke="#fff" strokeWidth="1" opacity="0.6">
                <line x1="8" y1="8" x2="13" y2="4" />
                <line x1="8" y1="8" x2="3" y2="5.5" />
                <line x1="8" y1="8" x2="9.5" y2="13" />
              </g>
              <g fill="#fff">
                <circle cx="8" cy="8" r="2.3" />
                <circle cx="13" cy="4" r="1.5" />
                <circle cx="3" cy="5.5" r="1.5" />
                <circle cx="9.5" cy="13" r="1.5" />
              </g>
            </svg>
          </span>
          <span className="app-name">Conductor</span>
        </div>
        <button type="button" onClick={signOut} className="app-signout">
          Sign out
        </button>
      </header>
      <main>
        {token !== null && (
          <FleetDashboard token={token} onUnauthorized={signOut} />
        )}
      </main>
    </div>
  );
}
