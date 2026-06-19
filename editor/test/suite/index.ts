// In-host test entry for the @vscode/test-electron smoke. `runTests` loads this
// module INSIDE the launched VS Code extension host and awaits the exported `run()`.
// To avoid pulling in a test framework (mocha) just for one smoke, this uses plain
// assertions and rejects on failure so the runner reports a non-zero exit.
//
// Compiled into out-test/suite/index.js by tsc -p tsconfig.test.json.
import { smoke } from "./extension.test";

export async function run(): Promise<void> {
  await smoke();
}
