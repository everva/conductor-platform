// TokenGate is the cockpit's front door (ADR-0026). When no token is present it
// renders a single-input form; on submit it VERIFIES the token by calling the
// gateway's status() endpoint. A 401 (or any failure) shows an error and does
// NOT store the token. On success it signs in (persisting to sessionStorage via
// AuthProvider) and renders its children (the app shell).
//
// The verify function is injectable so component tests can drive it without a
// real network; in production it constructs an ApiClient and calls status().
import { useState, type ReactNode } from "react";
import { ApiClient, ApiError } from "../api/client.ts";
import { useAuth } from "./context.ts";

// VerifyFn checks a candidate token against the gateway. It resolves on success
// and rejects (ideally with ApiError) on failure. Default: status() probe.
export type VerifyFn = (token: string) => Promise<void>;

const defaultVerify: VerifyFn = async (token) => {
  await new ApiClient({ token }).status();
};

export interface TokenGateProps {
  children: ReactNode;
  verify?: VerifyFn;
}

export function TokenGate({ children, verify = defaultVerify }: TokenGateProps) {
  const { token, signIn } = useAuth();
  const [input, setInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (token !== null) {
    return <>{children}</>;
  }

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const candidate = input.trim();
    if (candidate.length === 0) {
      setError("Enter a token.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await verify(candidate);
      signIn(candidate);
    } catch (err) {
      // Never echo the token; map a 401 to a friendly message.
      if (err instanceof ApiError && err.status === 401) {
        setError("Invalid token.");
      } else {
        setError("Could not reach the gateway.");
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <main style={styles.wrap}>
      <form onSubmit={onSubmit} style={styles.form} aria-label="Sign in">
        <h1 style={styles.title}>Conductor Cockpit</h1>
        <label htmlFor="token" style={styles.label}>
          API token
        </label>
        <input
          id="token"
          type="password"
          autoComplete="off"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="bearer token"
          style={styles.input}
          disabled={busy}
        />
        <button type="submit" style={styles.button} disabled={busy}>
          {busy ? "Verifying…" : "Sign in"}
        </button>
        {error !== null && (
          <p role="alert" style={styles.error}>
            {error}
          </p>
        )}
      </form>
    </main>
  );
}

const styles: Record<string, React.CSSProperties> = {
  wrap: {
    display: "flex",
    minHeight: "100vh",
    alignItems: "center",
    justifyContent: "center",
  },
  form: {
    display: "flex",
    flexDirection: "column",
    gap: "0.75rem",
    width: "20rem",
    padding: "1.5rem",
    border: "1px solid #2a2f37",
    borderRadius: "0.5rem",
    background: "#1b1e24",
  },
  title: { margin: 0, fontSize: "1.25rem" },
  label: { fontSize: "0.85rem", color: "#9aa4b2" },
  input: {
    padding: "0.5rem",
    borderRadius: "0.375rem",
    border: "1px solid #2a2f37",
    background: "#14161a",
    color: "#e6e6e6",
  },
  button: {
    padding: "0.5rem",
    borderRadius: "0.375rem",
    border: "none",
    background: "#3b82f6",
    color: "white",
  },
  error: { margin: 0, color: "#f87171", fontSize: "0.85rem" },
};
