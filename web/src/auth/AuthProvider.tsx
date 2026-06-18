// AuthProvider holds the bearer-token session state and exposes it via
// AuthContext. The token is hydrated from sessionStorage on mount and persisted
// there on sign-in (ADR-0026: sessionStorage, not localStorage). It does NOT
// verify the token — verification (a status() probe) happens in <TokenGate> so
// the provider stays storage-only and testable.
import { useCallback, useState, type ReactNode } from "react";
import { AuthContext, type AuthContextValue } from "./context.ts";
import { clearToken, loadToken, saveToken } from "./session.ts";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() => loadToken());

  const signIn = useCallback((t: string) => {
    saveToken(t);
    setToken(t);
  }, []);

  const signOut = useCallback(() => {
    clearToken();
    setToken(null);
  }, []);

  const value: AuthContextValue = { token, signIn, signOut };
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
