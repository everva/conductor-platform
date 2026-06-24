// Deterministic, headless unit tests for the extension (4B-0 scaffold + 4B-1 connect/
// auth wiring). The `vscode` module is aliased to test/vscode-mock.ts (vitest.config),
// so these run with no electron and no display. Behavior is driven against the mock's
// recorded calls + a real ConnectionManager backed by an in-memory SecretStore and a
// fake gateway probe — no network. The token-leak proof for the manager lives in
// connection.test.ts; here we additionally assert no user-facing message carries it.
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  commands,
  window,
  workspace,
  languages,
  Uri,
  ViewColumn,
  __reset,
  __makeSecretStorage,
  __makeGlobalState,
  __makeExtensionUri,
  __setConfig,
  type Disposable,
  type ExtensionContext,
} from "../test/vscode-mock";
import {
  ABORT_COMMAND,
  APPROVE_COMMAND,
  CONNECT_COMMAND,
  DISCONNECT_COMMAND,
  LOGIN_COMMAND,
  LOGOUT_COMMAND,
  CLAUDE_SETUP_TOKEN_CMD,
  CLAUDE_CREDENTIAL_KIND,
  PUSH_CREDENTIAL_COMMAND,
  REMOVE_CREDENTIAL_COMMAND,
  runLogin,
  runLogout,
  runPushCredential,
  runRemoveCredential,
  DIFF_SCHEME,
  DIFF_STORE_CAP,
  DIFF_EVICTED_PLACEHOLDER,
  COMMAND_CENTER_VIEW_TYPE,
  COMMAND_CENTER_TITLE,
  IntakePanel,
  INTAKE_VIEW_TYPE,
  INTAKE_TITLE,
  NEW_WORK_COMMAND,
  OPEN_COMMAND,
  OPEN_SESSION_COMMAND,
  OPEN_TASK_DIFF_COMMAND,
  REFRESH_SESSIONS_COMMAND,
  REFRESH_DIAGNOSTICS_COMMAND,
  FIRST_LAUNCH_KEY,
  OPEN_CONDUCTOR_ACTION,
  APPROVE_ACTION,
  OPEN_DIFF_ACTION,
  PAUSE_COMMAND,
  REVEAL_CONTAINER_COMMAND,
  RESUME_COMMAND,
  SHOW_DIFF_COMMAND,
  type Control,
  type ControlResult,
  type FleetViewConfig,
  type InterventionStatusBar,
  DiffStore,
  CommandCenterPanel,
  activate,
  deactivate,
  diffStatusText,
  fleetBarText,
  handleDiff,
  handleIntervention,
  interventionStatusText,
  makeDiffContentProvider,
  diffSideUri,
  diffUnifiedUri,
  parseDiffUri,
  registerConductor,
  renderDiffDocument,
  runApproveTask,
  OPEN_TOUR_COMMAND,
  WALKTHROUGH_ID,
  runConnect,
  runControl,
  runRetry,
  runDispatch,
  DISPATCH_COMMAND,
  runDisconnect,
  runShowDiff,
  runShowDiffForTask,
  openTaskDiffBeside,
  statusBarText,
  webviewHtml,
} from "./extension";
import type { Intervention } from "./notifier";
import type { TaskDiff } from "./diffObserver";
import { SESSIONS_VIEW_ID } from "./sessionsTree";
import { EVENTS_VIEW_ID } from "./eventsTree";
import { DIAGNOSTICS_VIEW_ID } from "./diagnosticsTree";
import { ACTIVITY_VIEW_ID } from "./activityTree";

// The slice of the vscode API the diff flows use, assembled from the mock. Cast at the
// seam because the headless mock is structurally (not nominally) the real `vscode` types.
// Typed as the intersection so it satisfies both runShowDiff (DiffVscodeApi) and
// registerConductor (VscodeApi & DiffVscodeApi).
const diffApi = { commands, window, workspace, languages, Uri } as unknown as Parameters<
  typeof registerConductor
>[0];

// A representative token-free TaskDiff (the shape DiffObserver distills + hands to handleDiff).
function makeTaskDiff(over: Partial<TaskDiff> = {}): TaskDiff {
  return {
    project: "alpha",
    task: "t-1",
    branch: "task/t-1",
    base: "main",
    files: [
      { path: "src/a.ts", status: "M", additions: 3, deletions: 1 },
      { path: "src/b.ts", status: "A", additions: 10, deletions: 0 },
    ],
    patch: "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n",
    truncated: false,
    ...over,
  };
}
import { CLAUDE_OAUTH_TOKEN_KEY, ConnectionManager, type SecretStore } from "./connection";
import type { CredentialClient, CredentialResult } from "./credentialClient";
import { ControlListError } from "./controlClient";
import { GATEWAY_TOKEN_KEY, type GatewayProbe, type TokenCheck } from "./gateway";

beforeEach(() => {
  __reset();
});

// Builds a REAL ConnectionManager backed by an in-memory secret map + a fake probe, so
// the command flows are exercised end-to-end without a network.
function makeManager(opts: {
  ready?: { ok: boolean; status: number };
  check?: TokenCheck;
  initial?: Record<string, string>;
}): { manager: ConnectionManager; map: Map<string, string> } {
  const map = new Map<string, string>(Object.entries(opts.initial ?? {}));
  const secrets: SecretStore = {
    get: (k) => Promise.resolve(map.get(k)),
    store: (k, v) => {
      map.set(k, v);
      return Promise.resolve();
    },
    delete: (k) => {
      map.delete(k);
      return Promise.resolve();
    },
  };
  const gateway: GatewayProbe = {
    pingReadyz: () => Promise.resolve(opts.ready ?? { ok: true, status: 200 }),
    validateToken: () => Promise.resolve(opts.check ?? "valid"),
  };
  return { manager: new ConnectionManager({ secrets, gateway }), map };
}

// A fake Control (the slice of ControlClient runControl uses). `listProjects` resolves a
// fixed list (or rejects when `listError` is set); each action records its calls and
// resolves a fixed ControlResult. No token ever flows through it.
function makeControl(opts: {
  projects?: { id: string }[];
  listError?: ControlListError;
  result?: ControlResult;
} = {}): Control & {
  pause: ReturnType<typeof vi.fn>;
  resume: ReturnType<typeof vi.fn>;
  abort: ReturnType<typeof vi.fn>;
  approve: ReturnType<typeof vi.fn>;
  retry: ReturnType<typeof vi.fn>;
  intake: ReturnType<typeof vi.fn>;
} {
  const result: ControlResult = opts.result ?? { ok: true, body: {} };
  const listProjects = vi.fn(() =>
    opts.listError ? Promise.reject(opts.listError) : Promise.resolve(opts.projects ?? [{ id: "p1" }]),
  );
  return {
    listProjects,
    pause: vi.fn(() => Promise.resolve(result)),
    resume: vi.fn(() => Promise.resolve(result)),
    abort: vi.fn(() => Promise.resolve(result)),
    approve: vi.fn(() => Promise.resolve(result)),
    retry: vi.fn(() => Promise.resolve(result)),
    intake: vi.fn(() => Promise.resolve(result)),
  };
}

describe("registerConductor", () => {
  it("registers connect + disconnect + login + logout + the 4 control commands + the diff scheme/command + the editor-area commands", () => {
    const { manager } = makeManager({});
    const disposables = registerConductor(
      diffApi,
      manager,
      "http://gw.test",
      makeControl(),
      new DiffStore(),
    );

    // connect + disconnect + login + logout + pause + resume + abort + approve + diff-provider +
    // show-diff + open (N0) + openSession (N3) + openTaskDiff (P3) + showEventJson + retryTask (A/B) +
    // newWork (Q3a) + dispatch (Faz-R) + openTour = 18. (Q0.4: no sidebar webview view; the CC + Intake
    // panels are built only when a fleetConfig is supplied — registerConductor here gets none.)
    expect(disposables).toHaveLength(18);
    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    // L1: the editor-mediated claude login/logout commands.
    expect(commands.registerCommand).toHaveBeenCalledWith(LOGIN_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(LOGOUT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(PAUSE_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(RESUME_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(ABORT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(APPROVE_COMMAND, expect.any(Function));
    // 4C-1b: the diff scheme content provider + the show-diff command.
    expect(workspace.registerTextDocumentContentProvider).toHaveBeenCalledWith(
      DIFF_SCHEME,
      expect.any(Object),
    );
    expect(commands.registerCommand).toHaveBeenCalledWith(SHOW_DIFF_COMMAND, expect.any(Function));
    // N0: the editor-area Command Center open command + N3: the deep-link openSession command.
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_SESSION_COMMAND, expect.any(Function));
    // P3: the task context-menu "Open Diff" command.
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_TASK_DIFF_COMMAND, expect.any(Function));
    // Q3a: the Intake "New Work" window command.
    expect(commands.registerCommand).toHaveBeenCalledWith(NEW_WORK_COMMAND, expect.any(Function));
    // Faz-R: the native Dispatch Work command.
    expect(commands.registerCommand).toHaveBeenCalledWith(DISPATCH_COMMAND, expect.any(Function));
    // Q0.4: NO sidebar webview view is registered any more (the cockpit lives only in the panel).
    expect(window.registerWebviewViewProvider).not.toHaveBeenCalled();
  });

  it("conductor.open with a project id SELECTS it → the Command Center scopes to that project (Q1)", () => {
    const { manager } = makeManager({});
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    registerConductor(
      diffApi,
      manager,
      "http://gw.test",
      makeControl(),
      new DiffStore(),
      undefined, // fetchFullDiff (unused here)
      makeFleetConfig({ bridgeFactory }),
    );
    // Grab the registered conductor.open callback and invoke it with a project id (the sessions-tree
    // project-click path) — it must select that project on the Command Center (project-only `select`).
    const openCb = commands.registerCommand.mock.calls.find((c) => c[0] === OPEN_COMMAND)?.[1] as (
      arg?: unknown,
    ) => void;
    expect(openCb).toBeDefined();

    openCb("web-shop");
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { postMessage: ReturnType<typeof vi.fn> };
      __fireMessage: (m: unknown) => void;
    };
    created.__fireMessage({ kind: "webview-ready" });
    // Project-only select (no `task`) → the board scopes to web-shop.
    expect(created.webview.postMessage).toHaveBeenCalledWith({ kind: "select", project: "web-shop" });
  });

  it("openTour opens the native Getting Started walkthrough", () => {
    const { manager } = makeManager({});
    registerConductor(diffApi, manager, "http://gw.test", makeControl(), new DiffStore());

    const tourCb = commands.registerCommand.mock.calls.find((c) => c[0] === OPEN_TOUR_COMMAND)?.[1] as () => void;
    expect(tourCb).toBeDefined();
    tourCb();
    expect(commands.executeCommand).toHaveBeenCalledWith("workbench.action.openWalkthrough", WALKTHROUGH_ID, false);
  });
});

describe("runConnect", () => {
  it("prompts for the token with password:true and reports success (URL only, no token)", async () => {
    window.showInputBox.mockResolvedValueOnce("tok-secret");
    const { manager, map } = makeManager({ ready: { ok: true, status: 200 }, check: "valid" });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showInputBox).toHaveBeenCalledWith(expect.objectContaining({ password: true }));
    expect(map.get(GATEWAY_TOKEN_KEY)).toBe("tok-secret");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Connected to http://gw.test");
    // The success message must not contain the token.
    const msg = window.showInformationMessage.mock.calls[0]?.[0];
    expect(msg).not.toContain("tok-secret");
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("surfaces a token-free 'rejected' error on an invalid token and stores nothing", async () => {
    window.showInputBox.mockResolvedValueOnce("tok-bad");
    const { manager, map } = makeManager({ check: "unauthorized" });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showErrorMessage).toHaveBeenCalledWith("The gateway rejected that token.");
    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    const errMsg = window.showErrorMessage.mock.calls[0]?.[0];
    expect(errMsg).not.toContain("tok-bad");
  });

  it("surfaces an 'unreachable' error when the gateway is not ready", async () => {
    window.showInputBox.mockResolvedValueOnce("tok");
    const { manager } = makeManager({ ready: { ok: false, status: 503 } });

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "Could not reach the gateway at http://gw.test.",
    );
  });

  it("is a quiet no-op when the token prompt is cancelled", async () => {
    window.showInputBox.mockResolvedValueOnce(undefined);
    const { manager, map } = makeManager({});

    await runConnect({ commands, window }, manager, "http://gw.test");

    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).not.toHaveBeenCalled();
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("guides the user (and does NOT prompt) when the gateway URL is empty", async () => {
    const { manager } = makeManager({});

    await runConnect({ commands, window }, manager, "   ");

    expect(window.showInputBox).not.toHaveBeenCalled();
    expect(window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("conductor.gatewayUrl"),
    );
  });
});

describe("runDisconnect", () => {
  it("forgets the stored token and reports", async () => {
    const { manager, map } = makeManager({ initial: { [GATEWAY_TOKEN_KEY]: "tok" } });

    await runDisconnect({ commands, window }, manager);

    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).toHaveBeenCalledWith("Disconnected.");
  });
});

describe("runLogin (L1)", () => {
  it("opens a terminal running `claude setup-token`, prompts (password) + stores the token", async () => {
    window.showInputBox.mockResolvedValueOnce("sk-ant-oat-secret");
    const { manager, map } = makeManager({});

    await runLogin({ window }, manager);

    // The integrated terminal ran the (non-secret) mint command and was shown.
    const term = window.createTerminal.mock.results[0]?.value as {
      sendText: ReturnType<typeof vi.fn>;
      show: ReturnType<typeof vi.fn>;
    };
    expect(term.show).toHaveBeenCalled();
    expect(term.sendText).toHaveBeenCalledWith(CLAUDE_SETUP_TOKEN_CMD);
    // The token was prompted MASKED and stored under the claude key (NOT the gateway key).
    expect(window.showInputBox).toHaveBeenCalledWith(expect.objectContaining({ password: true }));
    expect(map.get(CLAUDE_OAUTH_TOKEN_KEY)).toBe("sk-ant-oat-secret");
    expect(map.has(GATEWAY_TOKEN_KEY)).toBe(false);
  });

  it("never echoes the token in any user-facing message (leak guard)", async () => {
    const SECRET = "sk-ant-oat-MUST-NOT-LEAK";
    window.showInputBox.mockResolvedValueOnce(SECRET);
    const { manager } = makeManager({});

    await runLogin({ window }, manager);

    // No info/error message — nor the terminal's sendText — carries the token. The only place
    // it reaches is SecretStorage (asserted above).
    const allMessages = [
      ...window.showInformationMessage.mock.calls.map((c) => c[0]),
      ...window.showErrorMessage.mock.calls.map((c) => c[0]),
    ];
    expect(allMessages.length).toBeGreaterThan(0);
    for (const m of allMessages) {
      expect(m).not.toContain(SECRET);
    }
    const term = window.createTerminal.mock.results[0]?.value as { sendText: ReturnType<typeof vi.fn> };
    expect(JSON.stringify(term.sendText.mock.calls)).not.toContain(SECRET);
  });

  it("trims the pasted token before storing", async () => {
    window.showInputBox.mockResolvedValueOnce("  sk-ant-oat-padded  ");
    const { manager, map } = makeManager({});

    await runLogin({ window }, manager);

    expect(map.get(CLAUDE_OAUTH_TOKEN_KEY)).toBe("sk-ant-oat-padded");
  });

  it("is a quiet no-op when the paste is cancelled / empty", async () => {
    window.showInputBox.mockResolvedValueOnce(undefined);
    const { manager, map } = makeManager({});

    await runLogin({ window }, manager);

    expect(map.has(CLAUDE_OAUTH_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).not.toHaveBeenCalled();
  });
});

describe("runLogout (L1)", () => {
  it("forgets the stored claude token and reports", async () => {
    const { manager, map } = makeManager({ initial: { [CLAUDE_OAUTH_TOKEN_KEY]: "sk-ant-oat-x" } });

    await runLogout({ window }, manager);

    expect(map.has(CLAUDE_OAUTH_TOKEN_KEY)).toBe(false);
    expect(window.showInformationMessage).toHaveBeenCalledWith("Conductor login cleared.");
  });
});

// A SecretStore over an in-memory map (the L3 push flow reads the claude token host-side).
function makeSecrets(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  return { get: (k: string) => Promise.resolve(map.get(k)) };
}
// A fake CredentialClient: records calls + returns a fixed result. Cast at the seam (the flow
// only calls upload/remove).
function makeCredential(result: CredentialResult = { ok: true }) {
  const upload = vi.fn(() => Promise.resolve(result));
  const remove = vi.fn(() => Promise.resolve(result));
  return { client: { upload, remove } as unknown as CredentialClient, upload, remove };
}

describe("runPushCredential (L3)", () => {
  it("uploads the stored claude token under the claude kind; success message (no token)", async () => {
    const secrets = makeSecrets({ [CLAUDE_OAUTH_TOKEN_KEY]: "sk-ant-oat-PUSH" });
    const { client, upload } = makeCredential({ ok: true });

    await runPushCredential({ window }, secrets, client);

    expect(upload).toHaveBeenCalledWith(CLAUDE_CREDENTIAL_KIND, "sk-ant-oat-PUSH");
    expect(window.showInformationMessage).toHaveBeenCalled();
    // The success message must not echo the token.
    const msg = window.showInformationMessage.mock.calls[0]?.[0];
    expect(msg).not.toContain("sk-ant-oat-PUSH");
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("with no stored claude token → guides to Log In and does NOT upload", async () => {
    const secrets = makeSecrets({}); // no claude token
    const { client, upload } = makeCredential();

    await runPushCredential({ window }, secrets, client);

    expect(upload).not.toHaveBeenCalled();
    expect(window.showErrorMessage).toHaveBeenCalledWith(expect.stringContaining("Log In"));
  });

  it("surfaces a token-free error when the gateway is not connected", async () => {
    const secrets = makeSecrets({ [CLAUDE_OAUTH_TOKEN_KEY]: "sk-ant-oat-PUSH" });
    const { client } = makeCredential({ ok: false, reason: "not-connected", status: 0 });

    await runPushCredential({ window }, secrets, client);

    expect(window.showErrorMessage).toHaveBeenCalledWith(expect.stringContaining("Connect to the gateway"));
    const err = window.showErrorMessage.mock.calls[0]?.[0];
    expect(err).not.toContain("sk-ant-oat-PUSH");
  });

  it("surfaces a token-free error when the gateway has no encryption configured", async () => {
    const secrets = makeSecrets({ [CLAUDE_OAUTH_TOKEN_KEY]: "sk-ant-oat-PUSH" });
    const { client } = makeCredential({ ok: false, reason: "not-configured", status: 503 });

    await runPushCredential({ window }, secrets, client);

    expect(window.showErrorMessage).toHaveBeenCalledWith(expect.stringContaining("CONDUCTOR_CREDENTIAL_KEY"));
  });
});

describe("runRemoveCredential (L3)", () => {
  it("removes the gateway credential and reports", async () => {
    const { client, remove } = makeCredential({ ok: true });
    await runRemoveCredential({ window }, client);
    expect(remove).toHaveBeenCalledWith(CLAUDE_CREDENTIAL_KIND);
    expect(window.showInformationMessage).toHaveBeenCalledWith("Login removed from the gateway.");
  });
});

describe("runControl", () => {
  // Asserts NO recorded vscode message (info/warn/error) contains the given needle.
  function expectNoMessageContains(needle: string): void {
    const all = [
      ...window.showInformationMessage.mock.calls,
      ...window.showWarningMessage.mock.calls,
      ...window.showErrorMessage.mock.calls,
    ];
    for (const call of all) {
      expect(String(call[0] ?? "")).not.toContain(needle);
    }
  }

  it("pause: picks a project then calls control.pause and reports — NO confirm prompt", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ projects: [{ id: "p1" }, { id: "p2" }] });

    await runControl({ commands, window }, control, "pause");

    expect(window.showQuickPick).toHaveBeenCalledWith(["p1", "p2"], expect.any(Object));
    expect(control.pause).toHaveBeenCalledWith("p1");
    expect(window.showWarningMessage).not.toHaveBeenCalled(); // pause is not confirm-gated.
    expect(window.showInformationMessage).toHaveBeenCalledWith("Pause requested for p1.");
  });

  it("P3: a preselected project (context menu) acts directly — NO listProjects, NO quick-pick", async () => {
    const control = makeControl({ projects: [{ id: "p1" }, { id: "p2" }] });

    await runControl({ commands, window }, control, "pause", "p2");

    expect(control.listProjects).not.toHaveBeenCalled();
    expect(window.showQuickPick).not.toHaveBeenCalled();
    expect(control.pause).toHaveBeenCalledWith("p2");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Pause requested for p2.");
  });

  it("P3: a preselected confirm-gated action still confirms before firing", async () => {
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl();

    await runControl({ commands, window }, control, "abort", "p9");

    expect(control.listProjects).not.toHaveBeenCalled();
    expect(window.showWarningMessage).toHaveBeenCalledWith("Abort p9?", { modal: true }, "Yes");
    expect(control.abort).toHaveBeenCalledWith("p9");
  });

  it("resume: happy path calls control.resume with no confirm", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl();

    await runControl({ commands, window }, control, "resume");

    expect(control.resume).toHaveBeenCalledWith("p1");
    expect(window.showWarningMessage).not.toHaveBeenCalled();
    expect(window.showInformationMessage).toHaveBeenCalledWith("Resume requested for p1.");
  });

  it("abort: is confirm-gated — 'Yes' fires the action", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl();

    await runControl({ commands, window }, control, "abort");

    expect(window.showWarningMessage).toHaveBeenCalledWith(
      "Abort p1?",
      expect.objectContaining({ modal: true }),
      "Yes",
    );
    expect(control.abort).toHaveBeenCalledWith("p1");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Abort requested for p1.");
  });

  it("approve: is confirm-gated — cancelling the confirm does NOT fire the action", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce(undefined); // dismissed.
    const control = makeControl();

    await runControl({ commands, window }, control, "approve");

    expect(window.showWarningMessage).toHaveBeenCalledTimes(1);
    expect(control.approve).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("abort: a 'conflict' result surfaces the abort-specific warning", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes"); // confirm
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runControl({ commands, window }, control, "abort");

    expect(control.abort).toHaveBeenCalledWith("p1");
    // The confirm warning + the conflict warning were both shown; assert the conflict copy.
    const warnings = window.showWarningMessage.mock.calls.map((c) => String(c[0]));
    expect(warnings).toContain("No task is running to abort.");
  });

  it("approve: a 'conflict' result surfaces the approve-specific warning", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runControl({ commands, window }, control, "approve");

    const warnings = window.showWarningMessage.mock.calls.map((c) => String(c[0]));
    expect(warnings).toContain("No single task is awaiting approval (none or multiple).");
  });

  it("pause: cancelling the quick-pick is a quiet no-op (no action, no message)", async () => {
    window.showQuickPick.mockResolvedValueOnce(undefined);
    const control = makeControl();

    await runControl({ commands, window }, control, "pause");

    expect(control.pause).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
    expect(window.showWarningMessage).not.toHaveBeenCalled();
    expect(window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("shows an info message and does nothing else when there are no projects", async () => {
    const control = makeControl({ projects: [] });

    await runControl({ commands, window }, control, "pause");

    expect(window.showInformationMessage).toHaveBeenCalledWith("No projects.");
    expect(window.showQuickPick).not.toHaveBeenCalled();
    expect(control.pause).not.toHaveBeenCalled();
  });

  it("guides to Connect (no token in the message) when listProjects is not-connected", async () => {
    const control = makeControl({ listError: new ControlListError("not-connected", 0) });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Connect to the gateway first.");
    expect(window.showQuickPick).not.toHaveBeenCalled();
  });

  it("guides to reconnect when listProjects is unauthorized", async () => {
    const control = makeControl({ listError: new ControlListError("unauthorized", 401) });

    await runControl({ commands, window }, control, "resume");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "The gateway rejected the stored token. Reconnect.",
    );
  });

  it("reports an unreachable gateway when listProjects is unreachable", async () => {
    const control = makeControl({ listError: new ControlListError("unreachable", 0) });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Could not reach the gateway.");
  });

  it("maps an 'unauthorized' action result to a reconnect error", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ result: { ok: false, reason: "unauthorized", status: 401 } });

    await runControl({ commands, window }, control, "resume");

    expect(window.showErrorMessage).toHaveBeenCalledWith(
      "The gateway rejected the stored token. Reconnect.",
    );
  });

  it("maps an 'unreachable' action result to a gateway error", async () => {
    window.showQuickPick.mockResolvedValueOnce("p1");
    const control = makeControl({ result: { ok: false, reason: "unreachable", status: 0 } });

    await runControl({ commands, window }, control, "pause");

    expect(window.showErrorMessage).toHaveBeenCalledWith("Could not reach the gateway.");
  });

  it("never puts a token in any control message (leak guard across paths)", async () => {
    const TOKEN = "tok-MUST-NOT-LEAK";
    // Happy path.
    window.showQuickPick.mockResolvedValueOnce("p1");
    await runControl({ commands, window }, makeControl(), "pause");
    // Conflict path (confirm + conflict warning).
    window.showQuickPick.mockResolvedValueOnce("p1");
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    await runControl(
      { commands, window },
      makeControl({ result: { ok: false, reason: "conflict", status: 409 } }),
      "abort",
    );
    // Auth-failure paths.
    await runControl(
      { commands, window },
      makeControl({ listError: new ControlListError("unauthorized", 401) }),
      "pause",
    );
    expectNoMessageContains(TOKEN);
  });
});

describe("runRetry", () => {
  it("confirms, retries the blocked task, then refreshes the sessions tree", async () => {
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl();

    await runRetry({ commands, window }, control, "optiway", "VARDIYE-3");

    expect(window.showWarningMessage).toHaveBeenCalledWith("Retry VARDIYE-3?", { modal: true }, "Yes");
    expect(control.retry).toHaveBeenCalledWith("optiway", "VARDIYE-3");
    expect(window.showInformationMessage).toHaveBeenCalledWith("Retry requested for VARDIYE-3 (re-queued).");
    expect(commands.executeCommand).toHaveBeenCalledWith(REFRESH_SESSIONS_COMMAND);
  });

  it("declining the confirm is a quiet no-op (no retry, no refresh)", async () => {
    window.showWarningMessage.mockResolvedValueOnce(undefined);
    const control = makeControl();

    await runRetry({ commands, window }, control, "optiway", "VARDIYE-3");

    expect(control.retry).not.toHaveBeenCalled();
    expect(commands.executeCommand).not.toHaveBeenCalled();
  });

  it("a 409 conflict reports 'only a blocked task can be retried' and does NOT refresh", async () => {
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runRetry({ commands, window }, control, "optiway", "T-running");

    expect(control.retry).toHaveBeenCalledWith("optiway", "T-running");
    expect(window.showWarningMessage).toHaveBeenCalledWith("Only a blocked task can be retried.", {}, "OK");
    expect(commands.executeCommand).not.toHaveBeenCalled();
  });
});

describe("runDispatch (Faz-R native dispatch)", () => {
  // Drives the full prompt chain against the mock. showInputBox is called in order:
  // title → acceptance → lane → id; showQuickPick once for the tier; showWarningMessage
  // for the final confirm; showInformationMessage for the post-dispatch follow-up.
  function primePrompts(over: { title?: string; acceptance?: string; lane?: string; id?: string; tier?: string } = {}) {
    window.showInputBox
      .mockResolvedValueOnce(over.title ?? "Add a /healthz endpoint")
      .mockResolvedValueOnce(over.acceptance ?? "GET /healthz returns 200")
      .mockResolvedValueOnce(over.lane ?? "backend")
      .mockResolvedValueOnce(over.id ?? "W-HEALTHZ");
    window.showQuickPick.mockResolvedValueOnce(over.tier ?? "T2 — standard change");
  }

  it("preselected project: prompts, confirms, POSTs the synthesized YAML, refreshes, offers the session", async () => {
    primePrompts();
    window.showWarningMessage.mockResolvedValueOnce("Dispatch");
    window.showInformationMessage.mockResolvedValueOnce("Open Session");
    const control = makeControl({ result: { ok: true, body: { created: ["W-HEALTHZ"], skipped: [] } } });

    await runDispatch({ commands, window }, control, "web-shop");

    // No project quick-pick when preselected.
    expect(control.listProjects).not.toHaveBeenCalled();
    // The intake POST got the project + a well-formed YAML carrying every field.
    expect(control.intake).toHaveBeenCalledTimes(1);
    const [project, yaml] = control.intake.mock.calls[0] as [string, string];
    expect(project).toBe("web-shop");
    expect(yaml).toContain('id: "W-HEALTHZ"');
    expect(yaml).toContain('title: "Add a /healthz endpoint"');
    expect(yaml).toContain('tier: "T2"');
    expect(yaml).toContain('lane: "backend"');
    expect(yaml).toContain('  - "GET /healthz returns 200"');
    expect(yaml).toContain('hidden_holdout_ref: "store://holdouts/W-HEALTHZ/holdout_test.go"');
    // Confirm modal, then success → refresh + jump to the created session.
    expect(window.showWarningMessage).toHaveBeenCalledWith(expect.stringContaining("Dispatch"), { modal: true }, "Dispatch");
    expect(commands.executeCommand).toHaveBeenCalledWith(REFRESH_SESSIONS_COMMAND);
    expect(window.showInformationMessage).toHaveBeenCalledWith("Dispatched 1 task: W-HEALTHZ.", "Open Session");
    expect(commands.executeCommand).toHaveBeenCalledWith(OPEN_SESSION_COMMAND, "web-shop", "W-HEALTHZ");
  });

  it("palette path lists projects then quick-picks one before prompting", async () => {
    window.showQuickPick.mockResolvedValueOnce("p2"); // project pick FIRST (before tier)
    primePrompts();
    window.showWarningMessage.mockResolvedValueOnce("Dispatch");
    window.showInformationMessage.mockResolvedValueOnce(undefined);
    const control = makeControl({ projects: [{ id: "p1" }, { id: "p2" }], result: { ok: true, body: { created: ["W-HEALTHZ"], skipped: [] } } });

    await runDispatch({ commands, window }, control);

    expect(control.listProjects).toHaveBeenCalled();
    expect(window.showQuickPick).toHaveBeenCalledWith(["p1", "p2"], expect.any(Object));
    expect((control.intake.mock.calls[0] as [string, string])[0]).toBe("p2");
  });

  it("cancelling the title prompt is a quiet no-op (no intake)", async () => {
    window.showInputBox.mockResolvedValueOnce(undefined); // title cancelled
    const control = makeControl();

    await runDispatch({ commands, window }, control, "web-shop");

    expect(control.intake).not.toHaveBeenCalled();
    expect(window.showWarningMessage).not.toHaveBeenCalled();
  });

  it("empty acceptance is rejected before any POST", async () => {
    window.showInputBox
      .mockResolvedValueOnce("A title")
      .mockResolvedValueOnce("   "); // acceptance blank
    const control = makeControl();

    await runDispatch({ commands, window }, control, "web-shop");

    expect(control.intake).not.toHaveBeenCalled();
    expect(window.showErrorMessage).toHaveBeenCalledWith("At least one acceptance criterion is required.");
  });

  it("declining the final confirm does NOT POST", async () => {
    primePrompts();
    window.showWarningMessage.mockResolvedValueOnce(undefined); // confirm dismissed
    const control = makeControl();

    await runDispatch({ commands, window }, control, "web-shop");

    expect(control.intake).not.toHaveBeenCalled();
  });

  it("a gateway 400 surfaces the validation detail and does NOT refresh", async () => {
    primePrompts();
    window.showWarningMessage.mockResolvedValueOnce("Dispatch");
    const control = makeControl({
      result: { ok: false, reason: "invalid", status: 400, detail: 'unknown tier "T9"' },
    });

    await runDispatch({ commands, window }, control, "web-shop");

    expect(control.intake).toHaveBeenCalledTimes(1);
    expect(window.showErrorMessage).toHaveBeenCalledWith('Rejected: unknown tier "T9"');
    expect(commands.executeCommand).not.toHaveBeenCalledWith(REFRESH_SESSIONS_COMMAND);
  });
});

describe("statusBarText", () => {
  it("renders a distinct label per connection state", () => {
    expect(statusBarText("connected")).toContain("connected");
    expect(statusBarText("connecting")).toContain("connecting");
    expect(statusBarText("disconnected")).toContain("disconnected");
    expect(statusBarText("error")).toContain("error");
  });
});

describe("fleetBarText (Q4.2 native fleet glance)", () => {
  it("renders singular/plural for ≥1 and empty for ≤0 (caller hides it)", () => {
    expect(fleetBarText(0)).toBe("");
    expect(fleetBarText(-1)).toBe("");
    expect(fleetBarText(1)).toBe("$(server) 1 Conductor");
    expect(fleetBarText(3)).toBe("$(server) 3 Conductors");
  });
});

describe("interventionStatusText", () => {
  it("renders nothing for a zero count (the caller hides the item)", () => {
    expect(interventionStatusText(0)).toBe("");
    expect(interventionStatusText(-1)).toBe("");
  });

  it("renders a singular label for exactly one", () => {
    const text = interventionStatusText(1);
    expect(text).toContain("1");
    expect(text).toContain("intervention");
    expect(text).not.toContain("interventions"); // singular, not plural.
  });

  it("renders a plural label for N > 1", () => {
    const text = interventionStatusText(3);
    expect(text).toContain("3");
    expect(text).toContain("interventions");
  });
});

describe("handleIntervention", () => {
  // A minimal intervention status-bar item: records the text + spies show/hide. Satisfies
  // the InterventionStatusBar slice handleIntervention updates.
  function makeBar(): InterventionStatusBar & { show: ReturnType<typeof vi.fn>; hide: ReturnType<typeof vi.fn> } {
    return { text: "", show: vi.fn(), hide: vi.fn() };
  }
  // Spy actions for the toast's Approve / Open diff routes (4C-3 upgrade).
  function makeActions(): { approve: ReturnType<typeof vi.fn>; openDiff: ReturnType<typeof vi.fn> } {
    return { approve: vi.fn(), openDiff: vi.fn() };
  }

  const intervention: Intervention = { project: "alpha", task: "t-1", reason: "tests are red" };

  it("shows a warning toast naming project/task/reason + Approve/Open diff/Open Conductor actions, and sets the status text", async () => {
    const bar = makeBar();

    await handleIntervention({ commands, window }, bar, 2, intervention, makeActions());

    expect(window.showWarningMessage).toHaveBeenCalledWith(
      "Intervention needed — alpha/t-1: tests are red",
      APPROVE_ACTION,
      OPEN_DIFF_ACTION,
      OPEN_CONDUCTOR_ACTION,
    );
    // The status bar reflects the count and is shown.
    expect(bar.text).toBe(interventionStatusText(2));
    expect(bar.text).toContain("2");
    expect(bar.show).toHaveBeenCalledTimes(1);
  });

  it("reveals the activity-bar container when the user picks Open Conductor", async () => {
    window.showWarningMessage.mockResolvedValueOnce(OPEN_CONDUCTOR_ACTION);
    const actions = makeActions();

    await handleIntervention({ commands, window }, makeBar(), 1, intervention, actions);

    expect(commands.executeCommand).toHaveBeenCalledWith(REVEAL_CONTAINER_COMMAND);
    expect(REVEAL_CONTAINER_COMMAND).toBe("workbench.view.extension.conductor");
    expect(actions.approve).not.toHaveBeenCalled();
    expect(actions.openDiff).not.toHaveBeenCalled();
  });

  it("approves the intervention's OWN task in place when the user picks Approve", async () => {
    window.showWarningMessage.mockResolvedValueOnce(APPROVE_ACTION);
    const actions = makeActions();

    await handleIntervention({ commands, window }, makeBar(), 1, intervention, actions);

    expect(actions.approve).toHaveBeenCalledWith("alpha", "t-1");
    expect(commands.executeCommand).not.toHaveBeenCalled();
  });

  it("opens the intervention's OWN task diff (take over) when the user picks Open diff", async () => {
    window.showWarningMessage.mockResolvedValueOnce(OPEN_DIFF_ACTION);
    const actions = makeActions();

    await handleIntervention({ commands, window }, makeBar(), 1, intervention, actions);

    expect(actions.openDiff).toHaveBeenCalledWith("alpha", "t-1");
    expect(commands.executeCommand).not.toHaveBeenCalled();
  });

  it("does NOT act when the toast is dismissed", async () => {
    window.showWarningMessage.mockResolvedValueOnce(undefined); // dismissed.
    const actions = makeActions();

    await handleIntervention({ commands, window }, makeBar(), 1, intervention, actions);

    expect(commands.executeCommand).not.toHaveBeenCalled();
    expect(actions.approve).not.toHaveBeenCalled();
    expect(actions.openDiff).not.toHaveBeenCalled();
  });

  it("never puts the token in the toast or the status text (token-free input → token-free output)", async () => {
    // The notifier guarantees `intervention` carries no token; assert handleIntervention
    // does not somehow synthesize/echo one. Use a sentinel that must not appear anywhere.
    const TOKEN = "tok-MUST-NOT-LEAK";
    const bar = makeBar();

    await handleIntervention({ commands, window }, bar, 1, intervention, makeActions());

    const toast = String(window.showWarningMessage.mock.calls[0]?.[0] ?? "");
    expect(toast).not.toContain(TOKEN);
    expect(bar.text).not.toContain(TOKEN);
    // And the toast says exactly what we expect (reason included, nothing extra leaked).
    expect(toast).toContain("alpha/t-1");
    expect(toast).toContain("tests are red");
  });
});

describe("diffStatusText", () => {
  it("renders nothing for a zero/negative count (the caller hides the item)", () => {
    expect(diffStatusText(0)).toBe("");
    expect(diffStatusText(-1)).toBe("");
  });

  it("renders a singular label for exactly one", () => {
    const text = diffStatusText(1);
    expect(text).toContain("1");
    expect(text).toContain("diff");
    expect(text).not.toContain("diffs"); // singular, not plural.
  });

  it("renders a plural label for N > 1", () => {
    const text = diffStatusText(4);
    expect(text).toContain("4");
    expect(text).toContain("diffs");
  });

  it("uses a distinct codicon from the connection/intervention items", () => {
    expect(diffStatusText(2)).toContain("$(git-compare)");
  });
});

describe("renderDiffDocument", () => {
  it("includes a header with project/task, base...branch, file count, and the per-file stats + patch", () => {
    const body = renderDiffDocument(makeTaskDiff());

    // Human header: project/task + the base...branch range + a file count.
    expect(body).toContain("alpha/t-1");
    expect(body).toContain("main...task/t-1");
    expect(body).toContain("2 files");
    // One stat line per file: "<status>  <path>  +adds -dels".
    expect(body).toContain("src/a.ts");
    expect(body).toContain("+3 -1");
    expect(body).toContain("src/b.ts");
    expect(body).toContain("+10 -0");
    // The unified patch is appended after a blank separator line.
    expect(body).toContain("diff --git a/src/a.ts b/src/a.ts");
    expect(body).toMatch(/\n\ndiff --git/); // blank line before the patch.
  });

  it("says 'truncated' in the header only when the diff was capped", () => {
    expect(renderDiffDocument(makeTaskDiff({ truncated: false }))).not.toContain("truncated");
    expect(renderDiffDocument(makeTaskDiff({ truncated: true }))).toContain("truncated");
  });

  it("uses a singular 'file' for a single-file diff", () => {
    const body = renderDiffDocument(
      makeTaskDiff({ files: [{ path: "only.ts", status: "M", additions: 1, deletions: 0 }] }),
    );
    expect(body).toContain("1 file");
    expect(body).not.toContain("1 files");
  });

  it("renders an empty-file/empty-patch diff without throwing", () => {
    const body = renderDiffDocument(makeTaskDiff({ files: [], patch: "" }));
    expect(body).toContain("0 files");
    expect(typeof body).toBe("string");
  });
});

describe("DiffStore + content provider", () => {
  it("mints a unique .diff URI per add and serves the rendered content for it", () => {
    const store = new DiffStore();
    const provider = makeDiffContentProvider(store);

    const uri = store.add(makeTaskDiff());

    // The URI uses the conductor-diff scheme and ends in .diff (so VS Code applies the diff language).
    expect(uri.startsWith(`${DIFF_SCHEME}:`)).toBe(true);
    expect(uri.endsWith(".diff")).toBe(true);
    // The provider returns the stored content (the rendered document) for the known URI.
    const content = provider.provideTextDocumentContent(Uri.parse(uri) as never);
    expect(content).toContain("main...task/t-1");
    expect(content).toBe(renderDiffDocument(makeTaskDiff()));
  });

  it("returns the placeholder for an unknown/evicted URI", () => {
    const store = new DiffStore();
    const provider = makeDiffContentProvider(store);

    const content = provider.provideTextDocumentContent(
      Uri.parse(`${DIFF_SCHEME}:/never/added/9.diff`) as never,
    );
    expect(content).toBe(DIFF_EVICTED_PLACEHOLDER);
  });

  it("mints distinct URIs for repeated diffs of the same task (no stale-document collision)", () => {
    const store = new DiffStore();
    const a = store.add(makeTaskDiff());
    const b = store.add(makeTaskDiff());
    expect(a).not.toBe(b);
  });

  it("evicts the oldest past the cap (bounded) and the evicted URI yields the placeholder", () => {
    const store = new DiffStore();
    const provider = makeDiffContentProvider(store);

    const first = store.add(makeTaskDiff({ task: "t-0" }));
    // Add exactly CAP more so the first is evicted (size stays at the cap).
    for (let i = 1; i <= DIFF_STORE_CAP; i++) {
      store.add(makeTaskDiff({ task: `t-${i}` }));
    }

    expect(store.size).toBe(DIFF_STORE_CAP);
    // The first (oldest) entry was evicted → placeholder.
    expect(provider.provideTextDocumentContent(Uri.parse(first) as never)).toBe(
      DIFF_EVICTED_PLACEHOLDER,
    );
  });

  it("lists recents newest-first", () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ task: "old" }));
    store.add(makeTaskDiff({ task: "new" }));
    const recents = store.recents();
    expect(recents[0]?.task).toBe("new");
    expect(recents[1]?.task).toBe("old");
  });

  it("serves each file's before/after side from its per-file URI (P2a native diff)", () => {
    const store = new DiffStore();
    const provider = makeDiffContentProvider(store);
    store.add(makeTaskDiff()); // n=1, file 0 = src/a.ts (before "old", after "new")

    const before = provider.provideTextDocumentContent(
      Uri.parse(diffSideUri(1, 0, "before", "src/a.ts")) as never,
    );
    const after = provider.provideTextDocumentContent(
      Uri.parse(diffSideUri(1, 0, "after", "src/a.ts")) as never,
    );
    expect(before).toBe("old");
    expect(after).toBe("new");
    // The unified fallback URI still serves the rendered patch document.
    expect(
      provider.provideTextDocumentContent(Uri.parse(diffUnifiedUri(1)) as never),
    ).toBe(renderDiffDocument(makeTaskDiff()));
  });

  it("yields the placeholder for an out-of-range file index", () => {
    const store = new DiffStore();
    const provider = makeDiffContentProvider(store);
    store.add(makeTaskDiff());
    expect(
      provider.provideTextDocumentContent(Uri.parse(diffSideUri(1, 9, "before", "x")) as never),
    ).toBe(DIFF_EVICTED_PLACEHOLDER);
  });
});

describe("parseDiffUri / diffSideUri / diffUnifiedUri", () => {
  it("round-trips a per-file side URI", () => {
    const u = diffSideUri(3, 2, "after", "src/deep/file.ts");
    expect(parseDiffUri(u)).toEqual({ kind: "side", n: 3, fileIdx: 2, side: "after" });
  });

  it("parses the unified URI", () => {
    expect(parseDiffUri(diffUnifiedUri(5))).toEqual({ kind: "unified", n: 5 });
  });

  it("rejects a non-conductor / malformed URI (NaN id, wrong scheme)", () => {
    expect(parseDiffUri("file:///x")).toBeUndefined();
    expect(parseDiffUri(`${DIFF_SCHEME}:/nan/unified.diff`)).toBeUndefined();
    expect(parseDiffUri(`${DIFF_SCHEME}:/1/bogus/0/before/x`)).toBeUndefined();
  });
});

describe("handleDiff", () => {
  // A minimal status-bar item: records the text + spies show/hide (the InterventionStatusBar
  // slice handleDiff updates — reused for the diff bar).
  function makeBar(): InterventionStatusBar & {
    show: ReturnType<typeof vi.fn>;
    hide: ReturnType<typeof vi.fn>;
  } {
    return { text: "", show: vi.fn(), hide: vi.fn() };
  }

  it("stores the diff and sets+shows the status-bar count (NO toast)", () => {
    const store = new DiffStore();
    const bar = makeBar();

    handleDiff(store, bar, makeTaskDiff());

    expect(store.size).toBe(1);
    expect(bar.text).toBe(diffStatusText(1));
    expect(bar.text).toContain("1");
    expect(bar.show).toHaveBeenCalledTimes(1);
    // QUIET: handleDiff must not toast (the status bar is the signal).
    expect(window.showInformationMessage).not.toHaveBeenCalled();
    expect(window.showWarningMessage).not.toHaveBeenCalled();
  });

  it("bumps the count as more diffs arrive", () => {
    const store = new DiffStore();
    const bar = makeBar();

    handleDiff(store, bar, makeTaskDiff({ task: "t-1" }));
    handleDiff(store, bar, makeTaskDiff({ task: "t-2" }));

    expect(store.size).toBe(2);
    expect(bar.text).toContain("2");
  });

  it("never lets a token reach the store content or the status text (token-free in → out)", () => {
    const TOKEN = "tok-MUST-NOT-LEAK";
    const store = new DiffStore();
    const bar = makeBar();

    const uri = store.add(makeTaskDiff());
    handleDiff(store, bar, makeTaskDiff());

    expect(store.content(uri)).not.toContain(TOKEN);
    expect(bar.text).not.toContain(TOKEN);
  });
});

describe("runShowDiff", () => {
  it("shows an info message when there are no diffs yet", async () => {
    await runShowDiff(diffApi, new DiffStore());

    expect(window.showInformationMessage).toHaveBeenCalledWith("No task diffs yet.");
    expect(workspace.openTextDocument).not.toHaveBeenCalled();
  });

  it("opens the single diff directly (no quick-pick) as a NATIVE vscode.diff", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff()); // fixture patch → one diffable file (src/a.ts)

    await runShowDiff(diffApi, store);

    expect(window.showQuickPick).not.toHaveBeenCalled();
    // Opened natively: vscode.diff(beforeUri, afterUri, title, {preview:true}).
    expect(commands.executeCommand).toHaveBeenCalledTimes(1);
    const [cmd, left, right, title, opts] = commands.executeCommand.mock.calls[0]!;
    expect(cmd).toBe("vscode.diff");
    expect((left as { path: string }).path).toContain("/file/0/before/src/a.ts");
    expect((right as { path: string }).path).toContain("/file/0/after/src/a.ts");
    expect(title).toContain("src/a.ts");
    expect(opts).toMatchObject({ preview: true });
    // The native path doesn't fall back to a text document for a diffable file.
    expect(workspace.openTextDocument).not.toHaveBeenCalled();
  });

  it("opens the MOST-RECENT diff natively when one is picked from the quick-pick (>1 diffs)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ task: "older" }));
    store.add(makeTaskDiff({ task: "newer" }));
    // The newest is listed first; the label is "<task> (<n> files)".
    window.showQuickPick.mockResolvedValueOnce("newer (2 files)");

    await runShowDiff(diffApi, store);

    // The quick-pick offered both diffs, newest first.
    expect(window.showQuickPick).toHaveBeenCalledTimes(1);
    const labels = window.showQuickPick.mock.calls[0]?.[0] as string[];
    expect(labels[0]).toContain("newer");
    expect(labels[1]).toContain("older");
    // The picked (newest) diff opened natively — its single diffable file → vscode.diff whose
    // title names the newest task.
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "vscode.diff");
    expect(call?.[3]).toContain("newer");
  });

  it("is a quiet no-op when the diff quick-pick is cancelled (>1 diffs)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ task: "a" }));
    store.add(makeTaskDiff({ task: "b" }));
    window.showQuickPick.mockResolvedValueOnce(undefined); // cancelled.

    await runShowDiff(diffApi, store);

    expect(commands.executeCommand).not.toHaveBeenCalled();
    expect(workspace.openTextDocument).not.toHaveBeenCalled();
  });

  // A two-file diff fixture (P2c).
  function twoFileDiff(): TaskDiff {
    return makeTaskDiff({
      files: [
        { path: "x.ts", status: "M", additions: 1, deletions: 1 },
        { path: "y.ts", status: "M", additions: 1, deletions: 1 },
      ],
      patch: "diff --git a/x.ts b/x.ts\n@@ -1 +1 @@\n-a\n+b\n" + "diff --git a/y.ts b/y.ts\n@@ -1 +1 @@\n-c\n+d\n",
    });
  }

  it("P2c: opens the NATIVE MULTI-FILE diff editor when a diff touches several files (no per-file pick)", async () => {
    const store = new DiffStore();
    store.add(twoFileDiff());

    await runShowDiff(diffApi, store);

    // One retained diff → no diff-level pick; several files → the multi-diff editor, NOT a file pick.
    expect(window.showQuickPick).not.toHaveBeenCalled();
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "_workbench.openMultiDiffEditor");
    expect(call, "the multi-file diff editor should open").toBeDefined();
    const arg = call?.[1] as {
      title: string;
      resources: { originalUri: { path: string }; modifiedUri: { path: string } }[];
    };
    expect(arg.resources).toHaveLength(2);
    expect(arg.resources[0]!.originalUri.path).toContain("/file/0/before/x.ts");
    expect(arg.resources[1]!.modifiedUri.path).toContain("/file/1/after/y.ts");
    expect(arg.title).toContain("2 files");
  });

  it("P2c: falls back to a per-file quick-pick when the multi-diff editor is unavailable", async () => {
    const store = new DiffStore();
    store.add(twoFileDiff());
    // Simulate an older host where the multi-diff command is unknown (rejects); the next
    // executeCommand (vscode.diff) uses the default resolving impl.
    commands.executeCommand.mockImplementationOnce(() => Promise.reject(new Error("no command")));
    window.showQuickPick.mockResolvedValueOnce("y.ts");

    await runShowDiff(diffApi, store);

    expect(window.showQuickPick).toHaveBeenCalledTimes(1);
    const labels = window.showQuickPick.mock.calls[0]?.[0] as string[];
    expect(labels).toEqual(["x.ts", "y.ts"]);
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "vscode.diff");
    expect((call?.[1] as { path: string }).path).toContain("/file/1/before/y.ts");
    expect(call?.[3]).toContain("y.ts");
  });

  it("falls back to the unified document when no file is natively diffable (binary)", async () => {
    const store = new DiffStore();
    store.add(
      makeTaskDiff({
        files: [{ path: "img.png", status: "M", additions: 0, deletions: 0 }],
        patch: "diff --git a/img.png b/img.png\nBinary files a/img.png and b/img.png differ\n",
      }),
    );

    await runShowDiff(diffApi, store);

    // Nothing to render side-by-side → the unified text document is shown instead of vscode.diff.
    expect(commands.executeCommand).not.toHaveBeenCalled();
    expect(workspace.openTextDocument).toHaveBeenCalledTimes(1);
    expect(languages.setTextDocumentLanguage).toHaveBeenCalledWith(expect.any(Object), "diff");
  });

  it("P2b: upgrades to the FULL-context patch (full-file) when fetchFull returns one", async () => {
    const store = new DiffStore();
    // Bounded patch = a 1-line hunk (only the change); full patch = the whole file with context.
    store.add(makeTaskDiff({ patch: "diff --git a/src/a.ts b/src/a.ts\n@@ -2 +2 @@\n-old\n+new\n" }));
    const fullPatch = "diff --git a/src/a.ts b/src/a.ts\n@@ -1,3 +1,3 @@\n line1\n-old\n+new\n line3\n";
    const fetchFull = vi.fn(() =>
      Promise.resolve({ patch: fullPatch, base: "develop", branch: "b", truncated: false }),
    );

    await runShowDiff(diffApi, store, fetchFull);

    expect(fetchFull).toHaveBeenCalledWith("alpha", "t-1");
    // The content provider now serves the FULL reconstruction (context lines included), not the
    // bounded 1-line hunk — the editor renders a full-file native diff.
    const provider = makeDiffContentProvider(store);
    const before = provider.provideTextDocumentContent(
      Uri.parse(diffSideUri(1, 0, "before", "src/a.ts")) as never,
    );
    const after = provider.provideTextDocumentContent(
      Uri.parse(diffSideUri(1, 0, "after", "src/a.ts")) as never,
    );
    expect(before).toBe("line1\nold\nline3");
    expect(after).toBe("line1\nnew\nline3");
  });

  it("P2b: keeps the bounded reconstruction when fetchFull misses (graceful fallback)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff()); // bounded src/a.ts: before "old", after "new"
    const fetchFull = vi.fn(() => Promise.resolve(undefined)); // 404 / no token / network.

    await runShowDiff(diffApi, store, fetchFull);

    expect(fetchFull).toHaveBeenCalled();
    // Still opens natively from the bounded patch (vscode.diff); the bounded content stands.
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "vscode.diff");
    expect(call?.[3]).toContain("src/a.ts");
    const provider = makeDiffContentProvider(store);
    expect(
      provider.provideTextDocumentContent(Uri.parse(diffSideUri(1, 0, "before", "src/a.ts")) as never),
    ).toBe("old");
  });
});

describe("DiffStore.latestForTask (4C-3 take over)", () => {
  it("returns the most-recent diff for a project/task, scoped by both", () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "t-1" }));
    store.add(makeTaskDiff({ project: "beta", task: "t-1" })); // same task id, OTHER project
    const newest = store.add(makeTaskDiff({ project: "alpha", task: "t-1" }));

    expect(store.latestForTask("alpha", "t-1")?.uri).toBe(newest);
    expect(store.latestForTask("alpha", "t-1")?.project).toBe("alpha");
    expect(store.latestForTask("gamma", "t-1")).toBeUndefined();
  });
});

describe("runShowDiffForTask (4C-3 take over)", () => {
  it("opens the named task's most-recent diff natively (not a quick-pick of everything)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "other" }));
    store.add(makeTaskDiff({ project: "alpha", task: "t-1" }));

    await runShowDiffForTask(diffApi, store, "alpha", "t-1");

    // The target has a single diffable file → opens directly (no file pick), title names t-1.
    expect(window.showQuickPick).not.toHaveBeenCalled();
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "vscode.diff");
    expect(call?.[3]).toContain("t-1");
  });

  it("shows a clear info message (no stale diff) when the task has none retained", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "other" }));

    await runShowDiffForTask(diffApi, store, "alpha", "t-1");

    expect(window.showInformationMessage).toHaveBeenCalledWith("No diff yet for alpha/t-1.");
    expect(workspace.openTextDocument).not.toHaveBeenCalled();
  });
});

describe("openTaskDiffBeside (N3b session=workspace split)", () => {
  it("opens the task's latest diff natively BESIDE the command center (ViewColumn.Beside)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "t-1" }));
    await openTaskDiffBeside(diffApi, store, "alpha", "t-1", ViewColumn.Beside);
    // Native vscode.diff opened in the Beside column (no file prompt — deep-link opens the primary).
    const call = commands.executeCommand.mock.calls.find((c) => c[0] === "vscode.diff");
    expect(call?.[4]).toMatchObject({ preview: true, viewColumn: ViewColumn.Beside });
    expect(window.showQuickPick).not.toHaveBeenCalled();
  });

  it("is a quiet no-op when no diff is retained for the task (no open, no message)", async () => {
    const store = new DiffStore();
    await openTaskDiffBeside(diffApi, store, "alpha", "absent", ViewColumn.Beside);
    expect(commands.executeCommand).not.toHaveBeenCalled();
    expect(window.showTextDocument).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
  });
});

describe("runApproveTask (4C-3 in-place approve)", () => {
  it("confirms (modal) then approves the EXACT task and reports", async () => {
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl();

    await runApproveTask({ commands, window }, control, "alpha", "t-1");

    expect(window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Approve alpha/t-1?"),
      { modal: true },
      "Yes",
    );
    expect(control.approve).toHaveBeenCalledWith("alpha", "t-1");
    expect(window.showInformationMessage).toHaveBeenCalledWith(
      "Approve requested for alpha/t-1.",
    );
  });

  it("is a quiet no-op when the confirm is declined", async () => {
    window.showWarningMessage.mockResolvedValueOnce(undefined); // dismissed.
    const control = makeControl();

    await runApproveTask({ commands, window }, control, "alpha", "t-1");

    expect(control.approve).not.toHaveBeenCalled();
    expect(window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("surfaces a 409 conflict as guidance without throwing", async () => {
    window.showWarningMessage.mockResolvedValueOnce("Yes");
    const control = makeControl({ result: { ok: false, reason: "conflict", status: 409 } });

    await runApproveTask({ commands, window }, control, "alpha", "t-1");

    expect(control.approve).toHaveBeenCalledWith("alpha", "t-1");
    const warns = window.showWarningMessage.mock.calls.map((c) => String(c[0]));
    expect(warns.some((w) => /awaiting approval/i.test(w))).toBe(true);
  });
});

describe("activate", () => {
  it("creates a status bar, registers commands + view, and pushes all disposables", async () => {
    __setConfig({ "conductor.gatewayUrl": "http://localhost:8080" });
    const subscriptions: Disposable[] = [];
    const globalState = __makeGlobalState();
    const context = {
      subscriptions,
      secrets: __makeSecretStorage(),
      extensionUri: __makeExtensionUri(),
      globalState,
    } as ExtensionContext;

    activate(context as never);
    // Let the fire-and-forget restore() settle (no token stored → no fetch).
    await Promise.resolve();
    await Promise.resolve();

    // 4C-3 adds the intervention status-bar item; 4C-1b adds the diff one; Q4.2 adds the native
    // fleet-glance one; the "Now" activity adds one — so five are created (connection +
    // intervention + diff + fleet + now).
    expect(window.createStatusBarItem).toHaveBeenCalledTimes(5);
    const statusBar = window.createStatusBarItem.mock.results[0]?.value as {
      text: string;
      show: () => void;
    };
    expect(statusBar.show).toHaveBeenCalled();
    expect(statusBar.text).toContain("disconnected");

    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    // Q0.4: the sidebar webview Fleet view is gone — no webview-VIEW provider is registered (the
    // cockpit mounts only in the editor-area Command Center panel).
    expect(window.registerWebviewViewProvider).not.toHaveBeenCalled();
    // 4C-2 also registers the pause/resume/abort/approve commands.
    expect(commands.registerCommand).toHaveBeenCalledWith(PAUSE_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(APPROVE_COMMAND, expect.any(Function));
    // 4C-1b registers the diff scheme content provider + the show-diff command.
    expect(workspace.registerTextDocumentContentProvider).toHaveBeenCalledWith(
      DIFF_SCHEME,
      expect.any(Object),
    );
    expect(commands.registerCommand).toHaveBeenCalledWith(SHOW_DIFF_COMMAND, expect.any(Function));
    // N0: the open command is registered. N1 (ADR-0032 — default surface): activate flips the
    // Command Center to the default surface by REQUESTING it on startup — it executes
    // `conductor.open`, opening the singleton panel in the editor area. The mock records the
    // executeCommand; the real-host startup open is proven by the electron smoke (a "Conductor"
    // tab appears after activate()).
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_COMMAND, expect.any(Function));
    expect(commands.executeCommand).toHaveBeenCalledWith(OPEN_COMMAND);
    // N3: the deep-link openSession command is registered.
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_SESSION_COMMAND, expect.any(Function));
    // N5: a fresh install reveals the Conductor activity-bar container once + records the flag.
    expect(commands.executeCommand).toHaveBeenCalledWith(REVEAL_CONTAINER_COMMAND);
    expect(globalState._map.get(FIRST_LAUNCH_KEY)).toBe(true);
    // N2: the native sessions tree view + its refresh command are registered.
    expect(window.createTreeView).toHaveBeenCalledWith(SESSIONS_VIEW_ID, {
      treeDataProvider: expect.anything(),
    });
    expect(commands.registerCommand).toHaveBeenCalledWith(
      REFRESH_SESSIONS_COMMAND,
      expect.any(Function),
    );
    // Q2 (ADR-0045): the native "Conductor Events" panel tree view is registered too.
    expect(window.createTreeView).toHaveBeenCalledWith(EVENTS_VIEW_ID, {
      treeDataProvider: expect.anything(),
    });
    // Q3a (ADR-0046): the Intake "New Work" window command is registered.
    expect(commands.registerCommand).toHaveBeenCalledWith(NEW_WORK_COMMAND, expect.any(Function));
    // L1: the editor-mediated claude login/logout commands are registered too.
    expect(commands.registerCommand).toHaveBeenCalledWith(LOGIN_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(LOGOUT_COMMAND, expect.any(Function));
    // L3: the push/remove credential commands (registered in activate, not registerConductor).
    expect(commands.registerCommand).toHaveBeenCalledWith(PUSH_CREDENTIAL_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(REMOVE_CREDENTIAL_COMMAND, expect.any(Function));
    // Diagnostics: the native Status tree view + its refresh command are registered.
    expect(window.createTreeView).toHaveBeenCalledWith(DIAGNOSTICS_VIEW_ID, {
      treeDataProvider: expect.anything(),
    });
    expect(commands.registerCommand).toHaveBeenCalledWith(REFRESH_DIAGNOSTICS_COMMAND, expect.any(Function));
    // Activity ("Now"): the native Activity tree view is registered (+ a status-bar item).
    expect(window.createTreeView).toHaveBeenCalledWith(ACTIVITY_VIEW_ID, {
      treeDataProvider: expect.anything(),
    });
    // 33 (post-diagnostics) + Activity's three (view + provider + nowBar status item) = 36,
    // + the M2 auto-reconnect controller's dispose = 37, + the A/B Stream commands
    // (showEventJson + retryTask) = 39, + the openTour command = 40, + the Faz-R dispatch command = 41.
    expect(subscriptions).toHaveLength(41);
  });

  it("does NOT re-reveal the activity bar after the first launch (N5)", async () => {
    __setConfig({ "conductor.gatewayUrl": "http://localhost:8080" });
    const context = {
      subscriptions: [] as Disposable[],
      secrets: __makeSecretStorage(),
      extensionUri: __makeExtensionUri(),
      globalState: __makeGlobalState({ [FIRST_LAUNCH_KEY]: true }),
    } as ExtensionContext;

    activate(context as never);
    await Promise.resolve();

    // The Command Center still auto-opens (N1)…
    expect(commands.executeCommand).toHaveBeenCalledWith(OPEN_COMMAND);
    // …but a returning user's last-used view container is respected — no re-reveal (N5).
    expect(commands.executeCommand).not.toHaveBeenCalledWith(REVEAL_CONTAINER_COMMAND);
  });
});

describe("deactivate", () => {
  it("is a no-op that does not throw", () => {
    expect(() => deactivate()).not.toThrow();
  });
});

// A FleetViewConfig for the CommandCenterPanel tests. extensionUri is the mock's UriLike (the
// real type is vscode.Uri; the headless mock is structurally compatible), so cast at the seam.
function makeFleetConfig(over: Partial<FleetViewConfig> = {}): FleetViewConfig {
  return {
    secrets: __makeSecretStorage(),
    gatewayUrl: "http://gw.test",
    extensionUri: __makeExtensionUri() as unknown as FleetViewConfig["extensionUri"],
    ...over,
  };
}

describe("CommandCenterPanel (N0 — editor-area Command Center)", () => {
  it("opens an editor-area WebviewPanel that loads the cockpit bundle + attaches a bridge", () => {
    const attach = vi.fn();
    const dispose = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach, dispose }));

    new CommandCenterPanel(makeFleetConfig({ bridgeFactory })).open();

    // Created in the MAIN editor area (ViewColumn.One): scripts on, asset loading scoped to
    // dist/webview, retainContextWhenHidden so the live cockpit + WS bridge survive a tab-hide.
    expect(window.createWebviewPanel).toHaveBeenCalledTimes(1);
    expect(window.createWebviewPanel).toHaveBeenCalledWith(
      COMMAND_CENTER_VIEW_TYPE,
      COMMAND_CENTER_TITLE,
      ViewColumn.One,
      expect.objectContaining({
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [expect.objectContaining({ path: "/ext/dist/webview" })],
      }),
    );

    // Serves the SAME strict-CSP cockpit HTML as the sidebar (bundle script + CSS under
    // dist/webview, the #root mount), and the bridge is built over the panel webview + attached.
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { html: string };
    };
    expect(created.webview.html).toContain('src="vscode-resource:/ext/dist/webview/main.js"');
    expect(created.webview.html).toContain('href="vscode-resource:/ext/dist/webview/main.css"');
    expect(created.webview.html).toContain('<div id="root"></div>');
    expect(bridgeFactory).toHaveBeenCalledTimes(1);
    expect(bridgeFactory).toHaveBeenCalledWith(created.webview);
    expect(attach).toHaveBeenCalledTimes(1);
  });

  it("is a singleton: a second open reveals the existing panel instead of creating another", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.open();
    panel.open();

    expect(window.createWebviewPanel).toHaveBeenCalledTimes(1);
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      reveal: ReturnType<typeof vi.fn>;
    };
    expect(created.reveal).toHaveBeenCalledTimes(1);
    expect(created.reveal).toHaveBeenCalledWith(ViewColumn.One);
  });

  it("tears the bridge down when the panel is closed, and a later open builds a fresh one", () => {
    const dispose = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.open();
    const created = window.createWebviewPanel.mock.results[0]?.value as { __fireDispose: () => void };
    created.__fireDispose(); // user closes the tab
    expect(dispose).toHaveBeenCalledTimes(1);

    panel.open(); // singleton cleared → a fresh panel is built
    expect(window.createWebviewPanel).toHaveBeenCalledTimes(2);
  });

  it("dispose() closes the panel and tears the bridge down", () => {
    const dispose = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.open();
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      dispose: ReturnType<typeof vi.fn>;
    };
    panel.dispose();
    expect(created.dispose).toHaveBeenCalledTimes(1);
    expect(dispose).toHaveBeenCalledTimes(1);
  });

  it("is idempotent on dispose when never opened (no panel created)", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));
    expect(() => panel.dispose()).not.toThrow();
    expect(window.createWebviewPanel).not.toHaveBeenCalled();
  });

  it("serves token-free HTML: strict CSP with connect-src 'none' (not a new token surface)", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    new CommandCenterPanel(makeFleetConfig({ bridgeFactory })).open();
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { html: string };
    };
    // Same hardened CSP as the sidebar: no webview-originated network → no path for a token to
    // leave the host. Auth is owned by the host-side bridge.
    expect(created.webview.html).toContain("connect-src 'none'");
    expect(created.webview.html).toMatch(/default-src 'none'/);
  });

  it("select buffers the selection until the webview is ready, then flushes it (Q0)", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.select("web-shop", "W-1");
    // Cold start: the fresh webview hasn't signalled ready → nothing is posted yet.
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { postMessage: ReturnType<typeof vi.fn> };
      __fireMessage: (m: unknown) => void;
    };
    expect(created.webview.postMessage).not.toHaveBeenCalled();

    // The webview announces ready → the buffered selection is flushed (token-free).
    created.__fireMessage({ kind: "webview-ready" });
    expect(created.webview.postMessage).toHaveBeenCalledWith({
      kind: "select",
      project: "web-shop",
      task: "W-1",
    });
  });

  it("select posts immediately once the webview is ready, without a second panel (Q0)", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.open();
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { postMessage: ReturnType<typeof vi.fn> };
      __fireMessage: (m: unknown) => void;
    };
    created.__fireMessage({ kind: "webview-ready" });

    panel.select("api", "T-2");
    expect(created.webview.postMessage).toHaveBeenCalledWith({
      kind: "select",
      project: "api",
      task: "T-2",
    });
    // Singleton: select revealed the existing panel, it did not spawn a second one.
    expect(window.createWebviewPanel).toHaveBeenCalledTimes(1);
  });

  it("select with no task posts a project-only selection (no `task` field) (Q0)", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new CommandCenterPanel(makeFleetConfig({ bridgeFactory }));

    panel.open();
    const created = window.createWebviewPanel.mock.results[0]?.value as {
      webview: { postMessage: ReturnType<typeof vi.fn> };
      __fireMessage: (m: unknown) => void;
    };
    created.__fireMessage({ kind: "webview-ready" });

    panel.select("web-shop");
    // Project-only: the message carries exactly { kind, project } — no `task` on the wire.
    expect(created.webview.postMessage).toHaveBeenCalledWith({ kind: "select", project: "web-shop" });
  });
});

describe("IntakePanel (Q3a — editor-area New Work window)", () => {
  it("opens an editor-area WebviewPanel mounting the INTAKE surface + attaches a bridge", () => {
    const attach = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach, dispose: vi.fn() }));

    new IntakePanel(makeFleetConfig({ bridgeFactory })).open();

    expect(window.createWebviewPanel).toHaveBeenCalledTimes(1);
    expect(window.createWebviewPanel).toHaveBeenCalledWith(
      INTAKE_VIEW_TYPE,
      INTAKE_TITLE,
      ViewColumn.One,
      expect.objectContaining({ enableScripts: true, retainContextWhenHidden: true }),
    );
    const created = window.createWebviewPanel.mock.results[0]?.value as { webview: { html: string } };
    // The SAME bundle, but the #root carries data-surface="intake" → main.tsx mounts IntakeChat.
    expect(created.webview.html).toContain('<div id="root" data-surface="intake"></div>');
    expect(created.webview.html).toContain('src="vscode-resource:/ext/dist/webview/main.js"');
    expect(bridgeFactory).toHaveBeenCalledTimes(1);
    expect(attach).toHaveBeenCalledTimes(1);
  });

  it("is a singleton: a second open reveals the existing panel instead of creating another", () => {
    const bridgeFactory = vi.fn(() => ({ attach: vi.fn(), dispose: vi.fn() }));
    const panel = new IntakePanel(makeFleetConfig({ bridgeFactory }));
    panel.open();
    panel.open();
    expect(window.createWebviewPanel).toHaveBeenCalledTimes(1);
  });
});

describe("webviewHtml", () => {
  // A representative opts bundle (the runtime values resolveWebviewView passes).
  const opts = {
    cspSource: "vscode-resource:",
    nonce: "ABCDEF0123456789ABCDEF0123456789",
    scriptUri: "vscode-resource:/ext/dist/webview/main.js",
    styleUri: "vscode-resource:/ext/dist/webview/main.css",
  };

  it("locks default-src and connect-src to 'none' (webview does NO direct network)", () => {
    const html = webviewHtml(opts);
    expect(html).toMatch(/Content-Security-Policy/);
    expect(html).toContain("default-src 'none'");
    // connect-src 'none' is the enforcement that all data flows over the host bridge.
    expect(html).toContain("connect-src 'none'");
  });

  it("allows only a nonce'd script (script-src 'nonce-…', no 'unsafe-inline')", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`script-src 'nonce-${opts.nonce}'`);
    expect(html).not.toContain("unsafe-inline");
  });

  it("references the bundle script + style URIs and a #root mount, with no inline script body", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`src="${opts.scriptUri}"`);
    expect(html).toContain(`href="${opts.styleUri}"`);
    expect(html).toContain('<div id="root"></div>');
    // The ONLY <script> is the nonce'd external bundle — no inline JS between script tags.
    expect(html).toMatch(new RegExp(`<script nonce="${opts.nonce}" src="[^"]+"></script>`));
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)/i);
  });

  it("Q3a: surface:'intake' tags #root with data-surface; default emits no attribute", () => {
    expect(webviewHtml({ ...opts, surface: "intake" })).toContain(
      '<div id="root" data-surface="intake"></div>',
    );
    // Default (no surface) → the plain cockpit mount; an unknown surface is treated as default.
    expect(webviewHtml(opts)).toContain('<div id="root"></div>');
    expect(webviewHtml({ ...opts, surface: "bogus" })).toContain('<div id="root"></div>');
  });

  it("nonce-gates the stylesheet and threads cspSource into style/img/font directives", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`<link rel="stylesheet" nonce="${opts.nonce}"`);
    expect(html).toContain(`style-src 'nonce-${opts.nonce}' vscode-resource:`);
    expect(html).toContain("img-src vscode-resource: data:");
    expect(html).toContain("font-src vscode-resource:");
  });
});
