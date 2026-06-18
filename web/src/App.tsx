// App is the cockpit root: AuthProvider → TokenGate → Shell. The Shell renders the
// fleet dashboard (3B-1) once authed — projects/hosts/tasks live over REST + WS,
// with heartbeat freshness and lease/paused/intervention surfacing. A 401 from the
// dashboard's data layer signs the session out (back to the token gate).
import { AuthProvider } from "./auth/AuthProvider.tsx";
import { TokenGate } from "./auth/TokenGate.tsx";
import { useAuth } from "./auth/context.ts";
import { FleetDashboard } from "./fleet/FleetDashboard.tsx";

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
      <header style={styles.header}>
        <strong>Conductor Cockpit</strong>
        <button type="button" onClick={signOut} style={styles.signout}>
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

const styles: Record<string, React.CSSProperties> = {
  header: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    padding: "0.75rem 1.25rem",
    borderBottom: "1px solid #2a2f37",
  },
  signout: {
    padding: "0.375rem 0.75rem",
    borderRadius: "0.375rem",
    border: "1px solid #2a2f37",
    background: "transparent",
    color: "#e6e6e6",
  },
};
