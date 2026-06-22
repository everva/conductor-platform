// Pins the P2a unified-patch → per-file before/after reconstructor (diffReconstruct.ts).
// The reconstruction is what feeds the native `vscode.diff` (left = before, right = after), so
// these cases lock the load-bearing behavior: context to both sides, removed to before, added
// to after, add/delete via /dev/null, binary/rename non-diffable, and tolerance of minimal
// and full git output. Pure — no vscode.
import { describe, it, expect } from "vitest";
import { reconstructDiffFiles } from "./diffReconstruct";

describe("reconstructDiffFiles", () => {
  it("reconstructs a single modify: removed→before, added→after, path from the header", () => {
    // The exact shape the editor's TaskDiff fixture uses (minimal git diff).
    const files = reconstructDiffFiles("diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n");
    expect(files).toHaveLength(1);
    expect(files[0]).toMatchObject({
      path: "src/a.ts",
      before: "old",
      after: "new",
      binary: false,
      diffable: true,
    });
  });

  it("sends context lines to BOTH sides; removed only to before, added only to after", () => {
    const patch = [
      "diff --git a/f.ts b/f.ts",
      "--- a/f.ts",
      "+++ b/f.ts",
      "@@ -1,4 +1,4 @@",
      " keep-1",
      "-drop",
      "+insert",
      " keep-2",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.before).toBe("keep-1\ndrop\nkeep-2");
    expect(f?.after).toBe("keep-1\ninsert\nkeep-2");
    expect(f?.path).toBe("f.ts");
  });

  it("reconstructs a NEW file (--- /dev/null): empty before, added lines after, b-path", () => {
    const patch = [
      "diff --git a/new.ts b/new.ts",
      "new file mode 100644",
      "index 0000000..abc1234",
      "--- /dev/null",
      "+++ b/new.ts",
      "@@ -0,0 +1,2 @@",
      "+line one",
      "+line two",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.path).toBe("new.ts");
    expect(f?.before).toBe("");
    expect(f?.after).toBe("line one\nline two");
    expect(f?.diffable).toBe(true);
  });

  it("reconstructs a DELETED file (+++ /dev/null): removed lines before, empty after, a-path", () => {
    const patch = [
      "diff --git a/gone.ts b/gone.ts",
      "deleted file mode 100644",
      "--- a/gone.ts",
      "+++ /dev/null",
      "@@ -1,2 +0,0 @@",
      "-was one",
      "-was two",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.path).toBe("gone.ts");
    expect(f?.before).toBe("was one\nwas two");
    expect(f?.after).toBe("");
    expect(f?.diffable).toBe(true);
  });

  it("splits a MULTI-FILE patch into one reconstruction per file", () => {
    const patch = [
      "diff --git a/one.ts b/one.ts",
      "@@ -1 +1 @@",
      "-a",
      "+b",
      "diff --git a/two.ts b/two.ts",
      "@@ -1 +1 @@",
      "-c",
      "+d",
      "",
    ].join("\n");
    const files = reconstructDiffFiles(patch);
    expect(files.map((f) => f.path)).toEqual(["one.ts", "two.ts"]);
    expect(files[0]).toMatchObject({ before: "a", after: "b" });
    expect(files[1]).toMatchObject({ before: "c", after: "d" });
  });

  it("concatenates MULTIPLE HUNKS of one file (hunk-scoped, not whole-file)", () => {
    const patch = [
      "diff --git a/f.ts b/f.ts",
      "@@ -1,2 +1,2 @@",
      " top",
      "-x",
      "+X",
      "@@ -50,2 +50,2 @@",
      " mid",
      "-y",
      "+Y",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    // The two hunks' context+sides concatenate (the gap between them is not in the patch).
    expect(f?.before).toBe("top\nx\nmid\ny");
    expect(f?.after).toBe("top\nX\nmid\nY");
  });

  it("marks a BINARY file non-diffable (no textual sides)", () => {
    const patch = [
      "diff --git a/img.png b/img.png",
      "index 1111111..2222222 100644",
      "Binary files a/img.png and b/img.png differ",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.path).toBe("img.png");
    expect(f?.binary).toBe(true);
    expect(f?.diffable).toBe(false);
  });

  it("marks a pure RENAME (no content change → no hunk) non-diffable", () => {
    const patch = [
      "diff --git a/old.ts b/new.ts",
      "similarity index 100%",
      "rename from old.ts",
      "rename to new.ts",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.path).toBe("new.ts");
    expect(f?.diffable).toBe(false);
  });

  it("preserves EMPTY context/added lines (' ' and '+' with no payload)", () => {
    const patch = ["diff --git a/f.ts b/f.ts", "@@ -1,2 +1,3 @@", " a", "+", " b", ""].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.before).toBe("a\nb");
    expect(f?.after).toBe("a\n\nb"); // the empty added line is preserved
  });

  it("ignores the 'No newline at end of file' marker", () => {
    const patch = [
      "diff --git a/f.ts b/f.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "\\ No newline at end of file",
      "",
    ].join("\n");
    const [f] = reconstructDiffFiles(patch);
    expect(f?.before).toBe("old");
    expect(f?.after).toBe("new");
  });

  it("returns [] for an empty/whitespace patch (e.g. dropped over budget)", () => {
    expect(reconstructDiffFiles("")).toEqual([]);
    expect(reconstructDiffFiles("   \n  ")).toEqual([]);
  });

  it("is tolerant of a patch TRUNCATED mid-hunk (best-effort, no throw)", () => {
    // The producer caps patch bytes; the tail can be cut off. Reconstruct what's there.
    const patch = "diff --git a/f.ts b/f.ts\n@@ -1,3 +1,3 @@\n keep\n-drop\n+ins"; // no trailing newline
    const [f] = reconstructDiffFiles(patch);
    expect(f?.before).toBe("keep\ndrop");
    expect(f?.after).toBe("keep\nins");
  });

  it("strips a/ and b/ prefixes and resolves the b-side path even with a-side present", () => {
    const patch = ["diff --git a/x b/y", "--- a/x", "+++ b/y", "@@ -1 +1 @@", "-1", "+2", ""].join(
      "\n",
    );
    const [f] = reconstructDiffFiles(patch);
    expect(f?.path).toBe("y"); // the +++ (b/) path wins
  });
});
