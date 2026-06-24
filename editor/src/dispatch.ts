// Conductor Platform — native "Dispatch Work" model (Faz-R: maximum agentic-native control).
//
// The webview Intake (IntakePanel) is the rich, claude-assisted drafting surface. This is its
// NATIVE, webview-LESS complement: a command-palette flow (QuickPick + InputBox) that turns a
// few keystrokes into a valid intake YAML and POSTs it to the gateway — so the director can
// dispatch work from the keyboard without leaving the editor, the agentic-native loop's entry
// point. The orchestration (vscode prompts) lives in extension.ts; the PURE, deterministic bits
// — slug, YAML synthesis, tier vocabulary, result message — live here so vitest pins them with
// no vscode runtime.
//
// GROUNDED in the gateway's intake contract (verified, do NOT change the gateway):
//   * POST /projects/{id}/intake — body is raw scenario YAML; 200 → {created,skipped}; 400 on a
//     descriptive (secret-free) validation error; 404 unknown project (cmd/conductor-api/control.go).
//   * A scenario REQUIRES id/title/lane/tier(T1..T4)/≥1 acceptance/hidden_holdout_ref, and the
//     holdout ref must be repo-EXTERNAL (ADR-0018) — we default it to a store:// locator.
// Token-free: this module never sees the token (it builds a string; the host-side ControlClient
// owns the authed POST).

/** The closed set of risk tiers (ADR-0003 T1..T4), each with a one-line hint for the picker.
 * Tier maps to the governance merge policy, so it is a fixed vocabulary (the gateway rejects
 * anything else). The label is what the QuickPick shows; `parseTier` reads the tier back out. */
export const TIER_CHOICES: readonly string[] = [
  "T1 — low risk (auto-merge eligible)",
  "T2 — standard change",
  "T3 — elevated risk (human review)",
  "T4 — high risk (strict gate)",
];

/** Extracts the bare tier token (T1..T4) from a TIER_CHOICES label, or undefined if it isn't one. */
export function parseTier(label: string | undefined): string | undefined {
  if (label === undefined) {
    return undefined;
  }
  const m = /^(T[1-4])\b/.exec(label.trim());
  return m ? m[1] : undefined;
}

/** A fully-specified unit of work, ready to render as intake YAML. All strings are pre-trimmed
 * by the caller; `acceptance` is non-empty; `holdoutRef` is a repo-external locator. */
export interface DispatchSpec {
  readonly id: string;
  readonly title: string;
  readonly lane: string;
  readonly tier: string;
  readonly acceptance: readonly string[];
  readonly holdoutRef: string;
  readonly deps?: readonly string[];
}

/**
 * Derives a stable, gateway-friendly task id from a free-text title: upper-cased alnum runs joined
 * by '-', capped, with a leading "W-" so it reads as a work id even when the title is short/odd.
 * Deterministic + pure (no clock/random) so the orchestrator can prefill an editable InputBox and
 * a test can pin it. Empty/symbol-only titles fall back to "W-task".
 */
export function slugifyId(title: string): string {
  const slug = title
    .trim()
    .replace(/[^a-zA-Z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .toUpperCase()
    .slice(0, 32)
    .replace(/-+$/g, "");
  return slug === "" ? "W-task" : `W-${slug}`;
}

/** The default repo-external holdout locator for a task (ADR-0018): a store:// pointer keyed by id.
 * The holdout BODY lives out-of-repo; this is only the pointer the gateway validates + persists. */
export function defaultHoldoutRef(id: string): string {
  const safe = id.trim() === "" ? "task" : id.trim();
  return `store://holdouts/${safe}/holdout_test.go`;
}

/** Splits a free-text acceptance entry (the InputBox lets the user separate criteria by newlines or
 * ';') into a trimmed, non-empty list. Returns [] when nothing usable was entered. */
export function parseAcceptance(raw: string | undefined): string[] {
  if (raw === undefined) {
    return [];
  }
  return raw
    .split(/[\n;]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** Renders a single scalar as a SAFE YAML double-quoted string (escaping backslash, quote, and the
 * control chars that would otherwise break the document). Double-quoting every scalar means a title
 * with a colon, '#', or leading '-' can never be misparsed by the gateway's YAML loader. */
function yamlScalar(s: string): string {
  const escaped = s
    .replace(/\\/g, "\\\\")
    .replace(/"/g, '\\"')
    .replace(/\n/g, "\\n")
    .replace(/\r/g, "\\r")
    .replace(/\t/g, "\\t");
  return `"${escaped}"`;
}

/**
 * Synthesizes the intake YAML document for one scenario — EXACTLY the shape intake.LoadYAML accepts
 * (id/title/lane/tier/acceptance/hidden_holdout_ref, optional deps). Pure + deterministic: same spec
 * → same bytes. The gateway re-validates everything (tier vocabulary, holdout repo-external rule,
 * dangling deps), so this only has to be well-formed YAML; the human-facing validation happens there.
 */
export function buildIntakeYaml(spec: DispatchSpec): string {
  const lines: string[] = [
    `id: ${yamlScalar(spec.id)}`,
    `title: ${yamlScalar(spec.title)}`,
    `lane: ${yamlScalar(spec.lane)}`,
    `tier: ${yamlScalar(spec.tier)}`,
    "acceptance:",
    ...spec.acceptance.map((a) => `  - ${yamlScalar(a)}`),
  ];
  if (spec.deps && spec.deps.length > 0) {
    lines.push("deps:");
    for (const d of spec.deps) {
      lines.push(`  - ${yamlScalar(d)}`);
    }
  }
  lines.push(`hidden_holdout_ref: ${yamlScalar(spec.holdoutRef)}`);
  return lines.join("\n") + "\n";
}

/** Outcome of a successful intake POST, distilled from the gateway's {created,skipped} for a
 * token-free user message + a follow-up (jump to the first created session). */
export interface DispatchOutcome {
  readonly message: string;
  readonly createdIds: readonly string[];
}

/** Maps the gateway's intake result body ({created,skipped}) onto a human message + the created ids.
 * Tolerant of the unknown body shape (best-effort parse; never throws). A created id means the work
 * is now in the ledger; a skipped id means a duplicate the gateway ignored. */
export function dispatchOutcome(body: unknown): DispatchOutcome {
  const created = stringArray(body, "created");
  const skipped = stringArray(body, "skipped");
  if (created.length === 0 && skipped.length > 0) {
    return { message: `Already in the ledger (skipped): ${skipped.join(", ")}.`, createdIds: [] };
  }
  if (created.length === 0) {
    return { message: "Dispatched, but no new task was created.", createdIds: [] };
  }
  const base = `Dispatched ${created.length} task${created.length === 1 ? "" : "s"}: ${created.join(", ")}`;
  const tail = skipped.length > 0 ? ` (skipped ${skipped.join(", ")})` : "";
  return { message: `${base}${tail}.`, createdIds: created };
}

/** Reads a string[] field from an unknown object, dropping non-strings; [] when absent/ill-typed. */
function stringArray(body: unknown, key: string): string[] {
  if (body === null || typeof body !== "object") {
    return [];
  }
  const v = (body as Record<string, unknown>)[key];
  if (!Array.isArray(v)) {
    return [];
  }
  return v.filter((x): x is string => typeof x === "string");
}
