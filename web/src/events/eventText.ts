// Web cockpit event humanizer (PO audit — A1/A4). Turns ONE gateway event into a single
// plain-English line + a severity level, so the live stream reads like a status feed instead
// of dumping raw JSON. It is the WEB-canonical mirror of the editor's native describeEvent
// (editor/src/activity.ts): same kind/step/result switch, English copy for the sold product
// (the native tree keeps Turkish for the host-side dev surface — see ADR-0048 / X1).
//
// Pure + token-free: it reads only the event's classifying fields and a few well-known,
// secret-free payload keys (snake_case, exactly as the gateway emits them — elapsed_seconds /
// files_changed / result / summary / step). Anything it doesn't recognize degrades to
// "phase / kind", never a crash. The FULL payload is still available verbatim via the row's
// expand (the <pre> JSON), so nothing is hidden — this only makes the COLLAPSED line legible.
import type { Event } from "../api/types.ts";

/** Severity of an event line → drives the row's text color. Mirrors the native ActivityLevel. */
export type EventLevel = "active" | "attention" | "done" | "idle";

/** One event described in plain English + a severity level. */
export interface EventDescription {
  readonly text: string;
  readonly level: EventLevel;
}

/** Reads a numeric payload field, or undefined when absent/ill-typed. */
function num(payload: Record<string, unknown>, key: string): number | undefined {
  const v = payload[key];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

/** Reads a non-empty string payload field, or undefined. */
function str(payload: Record<string, unknown>, key: string): string | undefined {
  const v = payload[key];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/** Humanizes an elapsed-seconds count: "45s" / "4m". Empty when unknown. */
function humanElapsed(sec?: number): string {
  if (sec === undefined || sec < 0) {
    return "";
  }
  if (sec < 60) {
    return `${sec}s`;
  }
  return `${Math.floor(sec / 60)}m`;
}

/** Collapses whitespace + caps a one-line reason so the row stays digestible. */
function truncate(s: string, n = 120): string {
  const oneLine = s.replace(/\s+/g, " ").trim();
  return oneLine.length > n ? `${oneLine.slice(0, n - 1)}…` : oneLine;
}

/** A progress event → an English verb + elapsed + (for develop) files-changed. Reads the granular
 * `step` (provisioning|developing|verifying) from the payload, falling back to the coarse phase. */
function progressText(payload: Record<string, unknown>, phase: string): string {
  const el = humanElapsed(num(payload, "elapsed_seconds"));
  const suffix = el ? ` · ${el}` : "";
  const files = num(payload, "files_changed");
  const filesSuffix = files && files > 0 ? ` · ${files} ${files === 1 ? "file" : "files"}` : "";
  const stage = str(payload, "step") ?? phase;
  switch (stage) {
    case "provisioning":
    case "plan":
      return `Preparing workspace${suffix}`;
    case "developing":
    case "develop":
      return `Writing code${suffix}${filesSuffix}`;
    case "verifying":
    case "verify":
      return `Running checks${suffix}`;
    default:
      return `Working${suffix}`;
  }
}

/**
 * Describes a SINGLE event in plain English + a severity level. PURE + token-free; reads only the
 * event's kind/phase + a few secret-free payload keys. The web-canonical humanizer (X1): the live
 * Event stream renders this as the row line, and the full payload stays one click away (expand).
 */
export function describeEvent(e: Event): EventDescription {
  const p = e.payload && typeof e.payload === "object" ? e.payload : {};
  switch (e.kind) {
    case "progress":
      return { text: progressText(p, e.phase), level: "active" };
    case "started":
      return { text: "Started…", level: "active" };
    case "decision": {
      const result = str(p, "result");
      if (result === "blocked" || result === "changes-requested") {
        const why = str(p, "summary");
        return { text: why ? `Blocked: ${truncate(why)}` : "Blocked", level: "attention" };
      }
      // A passing verdict in held-for-review means the task now AWAITS the director's approval.
      return { text: "Awaiting approval", level: "attention" };
    }
    case "intervention-needed":
      return { text: "Needs your review", level: "attention" };
    case "diff": {
      const files = num(p, "files_changed");
      const n = files && files > 0 ? ` (${files} ${files === 1 ? "file" : "files"})` : "";
      return { text: `Changes ready${n}`, level: "active" };
    }
    case "merge":
      return { text: "Merged ✓", level: "done" };
    case "pr":
      return { text: "Pull request opened", level: "done" };
    case "health": {
      const s = str(p, "summary");
      return { text: s ? truncate(s) : "Health check", level: "idle" };
    }
    case "log": {
      const s = str(p, "summary") ?? str(p, "message");
      return { text: s ? truncate(s) : "Log", level: "idle" };
    }
    default:
      return { text: e.phase ? `${e.phase} / ${e.kind}` : e.kind, level: "active" };
  }
}
