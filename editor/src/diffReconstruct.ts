// Conductor Platform — unified-patch → per-file before/after reconstruction (Faz-P, P2a).
//
// WHY: the task diff arrives as a single UNIFIED PATCH string (the Go GitDiffer runs
// `git diff <base>...<branch>`, ADR-0030) plus per-file stats. The 4C-1b render dumped that
// patch as ONE read-only text document (`.diff` language → coarse +/- coloring). The user
// asked for a NATIVE diff "like Claude Code / native git diff": per-file, side-by-side /
// inline, red-green gutters, navigable. VS Code's native `vscode.diff` renders the diff
// between TWO documents — so we need a `before` (base) and `after` (modified) text per file.
//
// THE INSIGHT (no backend change — frozen-additive, ADR-0021): a unified hunk already carries
// both sides. Context lines (" ") belong to BOTH; removed ("-") to the BEFORE only; added
// ("+") to the AFTER only. Concatenating a file's hunks (context + the right side) reconstructs
// each side AS THE PATCH SHOWS IT — hunks with their surrounding context, exactly what a
// terminal `git diff` / Claude Code shows. It is NOT the whole file (the unchanged regions
// between hunks aren't in the patch); full-file fidelity would need the producer to ship
// base+modified content (P2b, frozen-additive backend) — a possible later enhancement.
//
// This module is PURE (no vscode, no I/O) so vitest pins the parser hard. It is also token-free
// (a patch carries code, never a token).

/** One file's reconstruction from the unified patch. `before`/`after` are the hunk-scoped
 * base/modified text (context + the respective side). `binary` marks a file git reported as
 * binary (no textual hunks → not side-by-side diffable). `diffable` is the convenience the
 * caller branches on: a text file with at least one hunk line. */
export interface ReconstructedFile {
  readonly path: string;
  readonly before: string;
  readonly after: string;
  readonly binary: boolean;
  readonly diffable: boolean;
}

/**
 * Splits a unified `git diff` patch into per-file {@link ReconstructedFile} reconstructions.
 * Tolerant of the real producer output (full `diff --git` sections with `index`/`---`/`+++`/
 * `@@` headers) AND of minimal/partial input (a hunk with no `+++`, a patch truncated
 * mid-hunk — the producer caps patch bytes). Never throws; an empty/whitespace patch → [].
 *
 * Path: prefers the `+++ b/<path>` (modified) side; on a delete (`+++ /dev/null`) falls back
 * to the `--- a/<path>` (base) side; else the `diff --git a/<p> b/<p>` header's b-path.
 */
export function reconstructDiffFiles(patch: string): ReconstructedFile[] {
  if (patch.trim() === "") {
    return [];
  }
  const out: ReconstructedFile[] = [];
  let cur: MutableFile | undefined;

  const flush = (): void => {
    if (cur !== undefined) {
      out.push(finalize(cur));
      cur = undefined;
    }
  };

  for (const line of patch.split("\n")) {
    // A new file section. Start a fresh accumulator; the path is refined by a later +++/---.
    if (line.startsWith("diff --git ")) {
      flush();
      cur = newFile(pathFromDiffGit(line));
      continue;
    }
    if (cur === undefined) {
      // Defensive: a patch with no leading `diff --git` (e.g. a bare `git diff` fragment).
      // Start an anonymous file so a hunk-only fragment still reconstructs.
      if (line.startsWith("@@") || isHunkBody(line)) {
        cur = newFile("");
      } else {
        continue; // preamble we can't attribute to a file yet.
      }
    }
    // Per-file metadata lines (skipped; they carry no content).
    if (line.startsWith("--- ")) {
      const p = pathFromMarker(line);
      if (p !== undefined) {
        cur.basePath = p;
      }
      continue;
    }
    if (line.startsWith("+++ ")) {
      const p = pathFromMarker(line);
      if (p !== undefined) {
        cur.modPath = p;
      }
      continue;
    }
    if (line.startsWith("Binary files") || line.startsWith("GIT binary patch")) {
      cur.binary = true;
      continue;
    }
    if (isMetadata(line)) {
      continue;
    }
    if (line.startsWith("@@")) {
      cur.sawHunk = true;
      continue; // hunk header: we reconstruct hunk-scoped, so line numbers aren't needed.
    }
    // Hunk body.
    if (line.startsWith("-")) {
      cur.before.push(line.slice(1));
      cur.sawBody = true;
    } else if (line.startsWith("+")) {
      cur.after.push(line.slice(1));
      cur.sawBody = true;
    } else if (line.startsWith(" ")) {
      const ctx = line.slice(1);
      cur.before.push(ctx);
      cur.after.push(ctx);
      cur.sawBody = true;
    }
    // line === "" (split artifact / blank separator) and "\ No newline at end of file" fall
    // through ignored — a real empty context line is " " (a single space), handled above.
  }
  flush();
  return out;
}

/** The mutable per-file accumulator used while walking the patch. */
interface MutableFile {
  basePath: string | undefined; // from `--- a/<path>`
  modPath: string | undefined; // from `+++ b/<path>`
  headerPath: string; // from `diff --git`
  before: string[];
  after: string[];
  binary: boolean;
  sawHunk: boolean;
  sawBody: boolean;
}

function newFile(headerPath: string): MutableFile {
  return {
    basePath: undefined,
    modPath: undefined,
    headerPath,
    before: [],
    after: [],
    binary: false,
    sawHunk: false,
    sawBody: false,
  };
}

/** Resolves the final path + diffable flag for an accumulated file. */
function finalize(f: MutableFile): ReconstructedFile {
  // Prefer the modified (b/) path; on a delete (+++ /dev/null) use the base (a/) path; else
  // the diff --git header path. An empty string is the last resort (anonymous fragment).
  const path = f.modPath || f.basePath || f.headerPath || "";
  return {
    path,
    before: f.before.join("\n"),
    after: f.after.join("\n"),
    binary: f.binary,
    // Diffable = a text file with actual hunk content (so vscode.diff has something to show).
    // A binary file, or a pure-metadata section (rename with no edits → no hunk body), is not.
    diffable: !f.binary && f.sawBody,
  };
}

/** Parses the path from a `diff --git a/<p> b/<p>` header — the b-side. Tolerant of paths
 * without the a//b/ prefixes; returns "" when it can't tell (a later +++ usually fixes it). */
function pathFromDiffGit(line: string): string {
  const rest = line.slice("diff --git ".length).trim();
  // Common case: "a/<p> b/<p>". Take the b/ token. Quoted/space paths are best-effort.
  const bIdx = rest.lastIndexOf(" b/");
  if (bIdx >= 0) {
    return rest.slice(bIdx + 3);
  }
  return "";
}

/** Parses the path from a `--- a/<path>` or `+++ b/<path>` marker. Returns undefined for
 * `/dev/null` (an add's base / a delete's modified side carry no path). Strips the a//b/
 * prefix and a possible trailing tab (git appends one only for odd paths). */
function pathFromMarker(line: string): string | undefined {
  let p = line.slice(4).trim(); // after "--- " / "+++ "
  const tab = p.indexOf("\t");
  if (tab >= 0) {
    p = p.slice(0, tab);
  }
  if (p === "/dev/null") {
    return undefined;
  }
  if (p.startsWith("a/") || p.startsWith("b/")) {
    p = p.slice(2);
  }
  return p;
}

/** True for the per-file metadata lines between `diff --git` and the first hunk (skipped). */
function isMetadata(line: string): boolean {
  return (
    line.startsWith("index ") ||
    line.startsWith("new file mode") ||
    line.startsWith("deleted file mode") ||
    line.startsWith("old mode") ||
    line.startsWith("new mode") ||
    line.startsWith("similarity index") ||
    line.startsWith("dissimilarity index") ||
    line.startsWith("rename from") ||
    line.startsWith("rename to") ||
    line.startsWith("copy from") ||
    line.startsWith("copy to")
  );
}

/** True for a unified-hunk body line (context/removed/added). Used only to start an anonymous
 * file when a patch fragment has no `diff --git` header. */
function isHunkBody(line: string): boolean {
  return line.startsWith(" ") || line.startsWith("-") || line.startsWith("+");
}
