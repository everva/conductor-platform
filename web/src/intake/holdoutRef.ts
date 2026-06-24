// holdoutIdFromRef extracts the holdout id from a hidden_holdout_ref locator
// ("pg://holdouts/<id>" or "store://holdouts/<id>/<file>") so the editor PUTs the reviewed
// auto-generated holdout body to the matching /holdouts/{id} (Faz-S S5). Returns "" when the ref
// is not a holdouts locator. Kept in its own module so IntakeChat.tsx stays component-only
// (react-refresh) and this stays unit-testable.
export function holdoutIdFromRef(ref: string): string {
  const m = /holdouts\/([^/]+)/.exec(ref.trim());
  return m ? m[1]! : "";
}
