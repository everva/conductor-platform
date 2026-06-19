// OPT-IN @vscode/test-electron integration smoke for the Conductor extension.
//
// WHY OPT-IN (and NOT in the deterministic gate): this downloads a full VS Code
// build and launches electron, which needs a display (or xvfb). The headless
// self-hosted CI runner has no guaranteed display, so running it there would be
// flaky/false-red. It therefore mirrors the repo's CP_REAL_CLAUDE opt-in smoke
// pattern (internal/sentinel/advisor_realclaude_smoke_test.go): it is double-gated —
//   - it is NOT part of `npm run gate` (deterministic gate = tsc + eslint + vitest +
//     esbuild build); it runs only via the separate `npm run test:vscode` script;
//   - even then it SKIPS cleanly (exit 0 with a clear log) unless CP_VSCODE_SMOKE=1.
//
// What it proves when run (CP_VSCODE_SMOKE=1, with a display): the packaged extension
// ACTIVATES in a real VS Code host and the `conductor.connect` command is registered.
//
// Build: compiled by `tsc -p tsconfig.test.json` into out-test/; the test:vscode
// script runs the emitted out-test/runVscodeSmoke.js.
import * as path from "node:path";
import { runTests } from "@vscode/test-electron";

async function main(): Promise<void> {
  if (process.env["CP_VSCODE_SMOKE"] !== "1") {
    console.log(
      "[vscode-smoke] SKIPPED (opt-in): set CP_VSCODE_SMOKE=1 to run the electron " +
        "smoke. It needs a display/xvfb and is excluded from the headless gate.",
    );
    process.exit(0);
  }

  // out-test/ layout mirrors test/: this file is out-test/runVscodeSmoke.js, the
  // in-host suite entry is out-test/suite/index.js. The extension dev path is the
  // editor/ root (one level up from out-test/). __dirname is available because this
  // harness is emitted as CommonJS (tsconfig.test.json).
  const extensionDevelopmentPath = path.resolve(__dirname, "..");
  const extensionTestsPath = path.resolve(__dirname, "./suite/index.js");

  try {
    await runTests({ extensionDevelopmentPath, extensionTestsPath });
    console.log("[vscode-smoke] PASSED: extension activated and command registered.");
  } catch (err) {
    console.error("[vscode-smoke] FAILED:", err);
    process.exit(1);
  }
}

void main();
