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
const OPEN_COMMAND = "conductor.open";
const OPEN_SESSION_COMMAND = "conductor.openSession";
const NEW_WORK_COMMAND = "conductor.newWork";
const DISPATCH_COMMAND = "conductor.dispatch";
const REFRESH_SESSIONS_COMMAND = "conductor.refreshSessions";
const COMMAND_CENTER_TITLE = "Conductor";
const INTAKE_TITLE = "New Work";
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

  // Tab helpers (used by the N1 startup-open + N0 explicit-open proofs). We POLL because
  // workbench tab registration can lag the fire-and-forget panel open on a loaded test host
  // (a run logged "Extension host did not start in 10 seconds").
  const collectTabLabels = (): string[] =>
    vscode.window.tabGroups.all.flatMap((group) => group.tabs.map((tab) => tab.label));
  const waitFor = async (predicate: () => boolean, timeoutMs: number): Promise<boolean> => {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if (predicate()) return true;
      await new Promise((resolve) => setTimeout(resolve, 150));
    }
    return predicate();
  };

  // N1 (ADR-0032 — Command Center = default surface): activation auto-opens the Command Center in
  // the editor area. Prove it in a REAL host — a "Conductor" tab must appear after activate()
  // WITHOUT running any command (the startup `void executeCommand(conductor.open)` in activate()).
  const autoOpened = await waitFor(
    () => collectTabLabels().includes(COMMAND_CENTER_TITLE),
    10000,
  );
  console.log(
    `[vscode-smoke] tabs after activate (N1 startup): ${JSON.stringify(collectTabLabels())}`,
  );
  assert.ok(
    autoOpened,
    `activation should auto-open the editor-area "${COMMAND_CENTER_TITLE}" tab (N1 default surface); saw ${JSON.stringify(
      collectTabLabels(),
    )}`,
  );

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
  // N0: the editor-area Command Center open command must be registered.
  assert.ok(
    allCommands.includes(OPEN_COMMAND),
    `command ${OPEN_COMMAND} should be registered`,
  );
  // N2: the native sessions tree refresh command must be registered.
  assert.ok(
    allCommands.includes(REFRESH_SESSIONS_COMMAND),
    `command ${REFRESH_SESSIONS_COMMAND} should be registered`,
  );
  // N3: the deep-link openSession command must be registered.
  assert.ok(
    allCommands.includes(OPEN_SESSION_COMMAND),
    `command ${OPEN_SESSION_COMMAND} should be registered`,
  );
  // Faz-R: the native Dispatch Work command must be registered. (A menu/keybinding references it,
  // so a missing command-declaration would be a real packaging bug the mocked gate can't catch.)
  assert.ok(
    allCommands.includes(DISPATCH_COMMAND),
    `command ${DISPATCH_COMMAND} should be registered`,
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

  // N0 — EXPLICIT-OPEN + SINGLETON proof in a REAL VS Code host. The Command Center is already
  // open (N1 startup auto-open above); running `conductor.open` again must REVEAL the same panel,
  // not spawn a second one. (The cockpit bundle loads with no gateway running; that's fine — auth
  // is host-side, nothing throws.)
  await vscode.commands.executeCommand(OPEN_COMMAND);
  const opened = await waitFor(() => collectTabLabels().includes(COMMAND_CENTER_TITLE), 10000);
  console.log(`[vscode-smoke] tabs after ${OPEN_COMMAND}: ${JSON.stringify(collectTabLabels())}`);
  assert.ok(
    opened,
    `executing ${OPEN_COMMAND} should keep the editor-area "${COMMAND_CENTER_TITLE}" tab; saw ${JSON.stringify(
      collectTabLabels(),
    )}`,
  );

  // Singleton: opening again must REVEAL the one panel, not spawn a duplicate tab.
  await vscode.commands.executeCommand(OPEN_COMMAND);
  await new Promise((resolve) => setTimeout(resolve, 500));
  const conductorTabs = collectTabLabels().filter((label) => label === COMMAND_CENTER_TITLE);
  assert.strictEqual(
    conductorTabs.length,
    1,
    `re-running ${OPEN_COMMAND} must reveal the single Command Center tab, not duplicate it; saw ${JSON.stringify(
      collectTabLabels(),
    )}`,
  );

  // N2: the native sessions tree's refresh command runs cleanly in a real host (no gateway →
  // the read client no-ops and the tree empties to its welcome view; the command path must not
  // throw). The `conductor.sessions` view itself is package.json-contributed and createTreeView
  // attached to it during activation (which already succeeded, above).
  await vscode.commands.executeCommand(REFRESH_SESSIONS_COMMAND);

  // Q0: the deep-link command runs cleanly in a real host. It reveals the Command Center and
  // posts a `select` (project+task) message to its webview (no gateway → the cockpit has no data,
  // so no SessionView appears, but the command + host→webview post path must not throw).
  await vscode.commands.executeCommand(OPEN_SESSION_COMMAND, "smoke-project", "smoke-task");

  // Q1: `conductor.open` WITH a project id (the sessions-tree project-click path) selects that
  // project → the cockpit scopes its board to it. Real host: the command + project-only `select`
  // post path must not throw (no gateway → no data, but the wiring is exercised end to end).
  await vscode.commands.executeCommand(OPEN_COMMAND, "smoke-project");

  // Q3a: `conductor.newWork` opens the Intake "New Work" window in its OWN editor tab (the cockpit
  // bundle mounts the intake surface via data-surface). No gateway → the project list is empty, but
  // the panel + tab must appear (this also proves the new command + panel wiring don't throw).
  await vscode.commands.executeCommand(NEW_WORK_COMMAND);
  const intakeOpened = await waitFor(() => collectTabLabels().includes(INTAKE_TITLE), 10000);
  console.log(`[vscode-smoke] tabs after ${NEW_WORK_COMMAND}: ${JSON.stringify(collectTabLabels())}`);
  assert.ok(
    intakeOpened,
    `executing ${NEW_WORK_COMMAND} should open the "${INTAKE_TITLE}" tab; saw ${JSON.stringify(
      collectTabLabels(),
    )}`,
  );
}
