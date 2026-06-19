// The actual in-host smoke assertions, run inside a real VS Code extension host by
// @vscode/test-electron. It imports the REAL `vscode` module (provided by the host at
// runtime; typed via @types/vscode) — NOT the unit-test mock. It asserts that the
// extension activated and that the `conductor.connect` command is registered.
//
// Plain assertions (no mocha) so the harness needs no extra deps; `smoke()` throws on
// failure, which the runner surfaces as a non-zero exit.
import * as assert from "node:assert";
import * as vscode from "vscode";

const EXTENSION_ID = "everva.conductor-editor";
const CONNECT_COMMAND = "conductor.connect";
const SHOW_DIFF_COMMAND = "conductor.showDiff";
const DIFF_SCHEME = "conductor-diff";
// The user-facing body the diff content provider serves for a URI not in the bounded
// store (mirrors DIFF_EVICTED_PLACEHOLDER in src/extension.ts; hardcoded here so the
// opt-in smoke doesn't pull the whole extension module into its compile).
const DIFF_PLACEHOLDER = "(diff no longer available)";

export async function smoke(): Promise<void> {
  const ext = vscode.extensions.getExtension(EXTENSION_ID);
  assert.ok(ext, `extension ${EXTENSION_ID} should be present in the host`);
  await ext.activate();
  assert.ok(ext.isActive, "extension should be active after activate()");

  const allCommands = await vscode.commands.getCommands(true);
  assert.ok(
    allCommands.includes(CONNECT_COMMAND),
    `command ${CONNECT_COMMAND} should be registered`,
  );
  // 4C-1b: the show-diff command must be registered too.
  assert.ok(
    allCommands.includes(SHOW_DIFF_COMMAND),
    `command ${SHOW_DIFF_COMMAND} should be registered`,
  );

  // 4C-1b — NATIVE DIFF RENDER proof in a REAL VS Code host. The activated extension
  // registered the `conductor-diff` read-only TextDocumentContentProvider, so opening a
  // conductor-diff URI routes to OUR provider and VS Code renders the virtual document.
  // The bounded store is empty here, so an arbitrary diff URI yields the eviction
  // placeholder (proving the provider actually served it), and the `.diff` suffix makes
  // VS Code apply the `diff` language. This exercises the REAL
  // workspace.registerTextDocumentContentProvider / openTextDocument / language mapping
  // that the unit tests can only mock — the live-render check for the native diff path.
  const diffUri = vscode.Uri.parse(`${DIFF_SCHEME}:/smoke/T-1/1.diff`);
  const doc = await vscode.workspace.openTextDocument(diffUri);
  assert.strictEqual(
    doc.languageId,
    "diff",
    "a .diff conductor-diff document should resolve to the `diff` language",
  );
  const body = doc.getText();
  assert.ok(
    body.includes(DIFF_PLACEHOLDER),
    "an unknown diff URI should render the placeholder (proves OUR content provider served it)",
  );

  // 4C-1b: the show-diff command runs without throwing in a real host (empty store → it
  // surfaces a "no diffs yet" info message and returns; no diff is open to assert, but the
  // command path must execute cleanly against the real vscode API).
  await vscode.commands.executeCommand(SHOW_DIFF_COMMAND);
}
