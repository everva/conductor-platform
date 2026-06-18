// Auth context object + hook (kept separate from the provider component so the
// eslint react-refresh "only-export-components" rule stays happy — a file that
// exports the provider exports only the component). The context carries the
// current token (null when signed out) and sign-in/out actions.
import { createContext, useContext } from "react";

export interface AuthContextValue {
  // token is the active bearer token, or null when signed out.
  token: string | null;
  // signIn stores the token (sessionStorage) and flips the app to authed.
  signIn: (token: string) => void;
  // signOut clears the token and returns to the token gate.
  signOut: () => void;
}

export const AuthContext = createContext<AuthContextValue | null>(null);

// useAuth returns the auth value, throwing if used outside an <AuthProvider>.
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (ctx === null) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return ctx;
}
