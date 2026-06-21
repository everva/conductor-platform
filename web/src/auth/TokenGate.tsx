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
import { Button } from "../ui/index.ts";
import { useAuth } from "./context.ts";
import "./auth.css";

// The Conductor brand mark (mirrors the app-bar): a small constellation glyph.
const BrandMark = () => (
  <span className="auth-mark" aria-hidden="true">
    <svg width="18" height="18" viewBox="0 0 16 16" fill="none">
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
);

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
    <main className="auth-wrap">
      <form onSubmit={onSubmit} className="auth-card" aria-label="Sign in">
        <div className="auth-brand">
          <BrandMark />
          <div>
            <h1 className="auth-title">Conductor</h1>
            <p className="auth-subtitle">Sign in to the cockpit</p>
          </div>
        </div>
        <div className="auth-field">
          <label htmlFor="token" className="auth-label">
            API token
          </label>
          <input
            id="token"
            type="password"
            autoComplete="off"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="bearer token"
            className="auth-input"
            disabled={busy}
          />
        </div>
        <Button
          type="submit"
          variant="primary"
          loading={busy}
          className="auth-submit"
        >
          {busy ? "Verifying…" : "Sign in"}
        </Button>
        {error !== null && (
          <p role="alert" className="auth-error">
            {error}
          </p>
        )}
      </form>
    </main>
  );
}
