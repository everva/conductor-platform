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
}
