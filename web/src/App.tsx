// App is the cockpit root: AuthProvider → TokenGate → Shell. The Shell is
// intentionally minimal for 3B-0 — it proves the client + auth + gateway wiring
// end to end by calling status() and rendering the project/host counts, plus a
// sign-out. The full fleet dashboard is 3B-1.
import { useCallback, useEffect, useState } from "react";
import { ApiClient, ApiError } from "./api/client.ts";
import type { StatusSummary } from "./api/types.ts";
import { AuthProvider } from "./auth/AuthProvider.tsx";
import { TokenGate } from "./auth/TokenGate.tsx";
import { useAuth } from "./auth/context.ts";

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
      <main style={styles.main}>
        {token !== null && <ConnectedPanel token={token} />}
      </main>
    </div>
  );
}

type LoadState =
  | { kind: "loading" }
  | { kind: "ok"; status: StatusSummary }
  | { kind: "error"; message: string };

function ConnectedPanel({ token }: { token: string }) {
  const [state, setState] = useState<LoadState>({ kind: "loading" });

  const load = useCallback(async () => {
    setState({ kind: "loading" });
    try {
      const status = await new ApiClient({ token }).status();
      setState({ kind: "ok", status });
    } catch (err) {
      const message =
        err instanceof ApiError
          ? `gateway error (${err.status})`
          : "could not reach the gateway";
      setState({ kind: "error", message });
    }
  }, [token]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <section style={styles.panel}>
      <h2 style={styles.h2}>Connected</h2>
      {state.kind === "loading" && <p>Loading status…</p>}
      {state.kind === "error" && (
        <p role="alert" style={styles.error}>
          {state.message}{" "}
          <button type="button" onClick={() => void load()}>
            Retry
          </button>
        </p>
      )}
      {state.kind === "ok" && (
        <dl style={styles.stats}>
          <div>
            <dt style={styles.dt}>Projects</dt>
            <dd style={styles.dd}>{state.status.projects}</dd>
          </div>
          <div>
            <dt style={styles.dt}>Hosts</dt>
            <dd style={styles.dd}>{state.status.hosts}</dd>
          </div>
        </dl>
      )}
    </section>
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
  main: { padding: "1.25rem" },
  panel: {
    maxWidth: "24rem",
    padding: "1.25rem",
    border: "1px solid #2a2f37",
    borderRadius: "0.5rem",
    background: "#1b1e24",
  },
  h2: { marginTop: 0, fontSize: "1rem" },
  stats: { display: "flex", gap: "2rem", margin: 0 },
  dt: { fontSize: "0.8rem", color: "#9aa4b2" },
  dd: { margin: 0, fontSize: "1.75rem", fontWeight: 600 },
  error: { color: "#f87171" },
};
