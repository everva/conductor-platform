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
  __makeWebviewView,
  __makeSecretStorage,
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
  DIFF_SCHEME,
  DIFF_STORE_CAP,
  DIFF_EVICTED_PLACEHOLDER,
  FLEET_VIEW_ID,
  COMMAND_CENTER_VIEW_TYPE,
  COMMAND_CENTER_TITLE,
  OPEN_COMMAND,
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
  FleetViewProvider,
  CommandCenterPanel,
  activate,
  deactivate,
  diffStatusText,
  handleDiff,
  handleIntervention,
  interventionStatusText,
  makeDiffContentProvider,
  placeholderHtml,
  registerConductor,
  renderDiffDocument,
  runApproveTask,
  runConnect,
  runControl,
  runDisconnect,
  runShowDiff,
  runShowDiffForTask,
  statusBarText,
  webviewHtml,
} from "./extension";
import type { Intervention } from "./notifier";
import type { TaskDiff } from "./diffObserver";

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
import { ConnectionManager, type SecretStore } from "./connection";
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
  };
}

describe("registerConductor", () => {
  it("registers connect + disconnect + the 4 control commands + the diff scheme/command + the fleet view provider", () => {
    const { manager } = makeManager({});
    const disposables = registerConductor(
      diffApi,
      manager,
      "http://gw.test",
      makeControl(),
      new DiffStore(),
    );

    // connect + disconnect + pause + resume + abort + approve + diff-provider + show-diff +
    // open (N0) + fleet view = 10 (no fleetConfig → no Command Center panel disposable).
    expect(disposables).toHaveLength(10);
    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
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
    // N0: the editor-area Command Center open command.
    expect(commands.registerCommand).toHaveBeenCalledWith(OPEN_COMMAND, expect.any(Function));
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
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

describe("statusBarText", () => {
  it("renders a distinct label per connection state", () => {
    expect(statusBarText("connected")).toContain("connected");
    expect(statusBarText("connecting")).toContain("connecting");
    expect(statusBarText("disconnected")).toContain("disconnected");
    expect(statusBarText("error")).toContain("error");
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

  it("opens the single diff directly (no quick-pick) in a conductor-diff document", async () => {
    const store = new DiffStore();
    const uri = store.add(makeTaskDiff());

    await runShowDiff(diffApi, store);

    expect(window.showQuickPick).not.toHaveBeenCalled();
    // The conductor-diff URI was opened + shown (as a preview) + given the diff language.
    expect(workspace.openTextDocument).toHaveBeenCalledTimes(1);
    const opened = workspace.openTextDocument.mock.calls[0]?.[0] as { path: string };
    expect(opened.path).toBe(uri);
    expect(window.showTextDocument).toHaveBeenCalledWith(
      expect.objectContaining({ uri: expect.objectContaining({ path: uri }) }),
      expect.objectContaining({ preview: true }),
    );
    expect(languages.setTextDocumentLanguage).toHaveBeenCalledWith(expect.any(Object), "diff");
  });

  it("opens the MOST-RECENT diff when one is picked from the quick-pick (>1 diffs)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ task: "older" }));
    const newestUri = store.add(makeTaskDiff({ task: "newer" }));
    // The newest is listed first; the label is "<task> (<n> files)".
    window.showQuickPick.mockResolvedValueOnce("newer (2 files)");

    await runShowDiff(diffApi, store);

    // The quick-pick offered both, newest first.
    expect(window.showQuickPick).toHaveBeenCalledTimes(1);
    const labels = window.showQuickPick.mock.calls[0]?.[0] as string[];
    expect(labels[0]).toContain("newer");
    expect(labels[1]).toContain("older");
    // The picked (newest) URI was opened.
    const opened = workspace.openTextDocument.mock.calls[0]?.[0] as { path: string };
    expect(opened.path).toBe(newestUri);
  });

  it("is a quiet no-op when the quick-pick is cancelled (>1 diffs)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ task: "a" }));
    store.add(makeTaskDiff({ task: "b" }));
    window.showQuickPick.mockResolvedValueOnce(undefined); // cancelled.

    await runShowDiff(diffApi, store);

    expect(workspace.openTextDocument).not.toHaveBeenCalled();
    expect(window.showTextDocument).not.toHaveBeenCalled();
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
  it("opens the named task's most-recent diff (not a quick-pick of everything)", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "other" }));
    const target = store.add(makeTaskDiff({ project: "alpha", task: "t-1" }));

    await runShowDiffForTask(diffApi, store, "alpha", "t-1");

    expect(window.showQuickPick).not.toHaveBeenCalled();
    const opened = workspace.openTextDocument.mock.calls[0]?.[0] as { path: string };
    expect(opened.path).toBe(target);
    expect(window.showTextDocument).toHaveBeenCalledWith(
      expect.objectContaining({ uri: expect.objectContaining({ path: target }) }),
      expect.objectContaining({ preview: true }),
    );
  });

  it("shows a clear info message (no stale diff) when the task has none retained", async () => {
    const store = new DiffStore();
    store.add(makeTaskDiff({ project: "alpha", task: "other" }));

    await runShowDiffForTask(diffApi, store, "alpha", "t-1");

    expect(window.showInformationMessage).toHaveBeenCalledWith("No diff yet for alpha/t-1.");
    expect(workspace.openTextDocument).not.toHaveBeenCalled();
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
    const context = {
      subscriptions,
      secrets: __makeSecretStorage(),
      extensionUri: __makeExtensionUri(),
    } as ExtensionContext;

    activate(context as never);
    // Let the fire-and-forget restore() settle (no token stored → no fetch).
    await Promise.resolve();
    await Promise.resolve();

    // 4C-3 adds the intervention status-bar item; 4C-1b adds the diff one — so three are
    // created (connection + intervention + diff).
    expect(window.createStatusBarItem).toHaveBeenCalledTimes(3);
    const statusBar = window.createStatusBarItem.mock.results[0]?.value as {
      text: string;
      show: () => void;
    };
    expect(statusBar.show).toHaveBeenCalled();
    expect(statusBar.text).toContain("disconnected");

    expect(commands.registerCommand).toHaveBeenCalledWith(CONNECT_COMMAND, expect.any(Function));
    expect(commands.registerCommand).toHaveBeenCalledWith(DISCONNECT_COMMAND, expect.any(Function));
    expect(window.registerWebviewViewProvider).toHaveBeenCalledWith(
      FLEET_VIEW_ID,
      expect.any(FleetViewProvider),
    );
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
    // connection bar + intervention bar (4C-3) + diff bar (4C-1b) + notifier-dispose (4C-3) +
    // diff-observer-dispose (4C-1b) + connect + disconnect + pause + resume + abort + approve +
    // diff-provider (4C-1b) + show-diff (4C-1b) + open (N0) + fleet view + Command Center
    // panel (N0) = 16.
    expect(subscriptions).toHaveLength(16);
  });
});

describe("deactivate", () => {
  it("is a no-op that does not throw", () => {
    expect(() => deactivate()).not.toThrow();
  });
});

// A FleetViewConfig for the provider tests. extensionUri is the mock's UriLike (the real
// type is vscode.Uri; the headless mock is structurally compatible), so cast at the seam.
function makeFleetConfig(over: Partial<FleetViewConfig> = {}): FleetViewConfig {
  return {
    secrets: __makeSecretStorage(),
    gatewayUrl: "http://gw.test",
    extensionUri: __makeExtensionUri() as unknown as FleetViewConfig["extensionUri"],
    ...over,
  };
}

describe("FleetViewProvider", () => {
  it("enables scripts and renders the no-config placeholder HTML on resolve (bare, no bridge)", () => {
    const view = __makeWebviewView("vscode-resource:");
    // Bare construction (no config) renders the static placeholder: scripts on, no bundle
    // (no extensionUri to build asset URIs), and NO bridge attached.
    new FleetViewProvider().resolveWebviewView(view as never);
    expect(view.webview.options.enableScripts).toBe(true);
    expect(view.webview.html).toContain("Not connected");
    // The legacy placeholder carries no live <script> (no bundle on the no-config path).
    expect(view.webview.html).not.toMatch(/<script/i);
    // No config → no bridge → the host never subscribes to the webview's messages.
    expect(view.webview.onDidReceiveMessage).not.toHaveBeenCalled();
  });

  it("loads the cockpit bundle, scopes localResourceRoots, attaches a bridge, and disposes on view-dispose", () => {
    const view = __makeWebviewView("vscode-resource:");
    const attach = vi.fn();
    const dispose = vi.fn();
    const bridgeFactory = vi.fn(() => ({ attach, dispose }));

    new FleetViewProvider(makeFleetConfig({ bridgeFactory })).resolveWebviewView(view as never);

    // 4B-3: scripts on + localResourceRoots scoped to dist/webview; the HTML nonce-loads
    // the bundled cockpit script + CSS (asWebviewUri-mapped under dist/webview).
    expect(view.webview.options.enableScripts).toBe(true);
    expect(view.webview.options.localResourceRoots?.[0]?.path).toBe("/ext/dist/webview");
    expect(view.webview.html).toContain('src="vscode-resource:/ext/dist/webview/main.js"');
    expect(view.webview.html).toContain('href="vscode-resource:/ext/dist/webview/main.css"');
    expect(view.webview.html).toContain('<div id="root"></div>');

    // The factory was handed the webview and the bridge was attached.
    expect(bridgeFactory).toHaveBeenCalledTimes(1);
    expect(bridgeFactory).toHaveBeenCalledWith(view.webview);
    expect(attach).toHaveBeenCalledTimes(1);
    expect(dispose).not.toHaveBeenCalled();

    // Closing the view tears the bridge down.
    view.__fireDispose();
    expect(dispose).toHaveBeenCalledTimes(1);
  });

  it("disposes the previous bridge when re-resolved (review FAZ-4 MED: re-resolve leak)", () => {
    const disposes: ReturnType<typeof vi.fn>[] = [];
    const attaches: ReturnType<typeof vi.fn>[] = [];
    const bridgeFactory = vi.fn(() => {
      const attach = vi.fn();
      const dispose = vi.fn();
      attaches.push(attach);
      disposes.push(dispose);
      return { attach, dispose };
    });
    const provider = new FleetViewProvider(makeFleetConfig({ bridgeFactory }));

    provider.resolveWebviewView(__makeWebviewView() as never); // bridge 0
    // Re-resolve WITHOUT firing the first view's onDidDispose (VS Code can re-resolve a
    // hidden→revealed view): the provider must tear down the predecessor itself.
    provider.resolveWebviewView(__makeWebviewView() as never); // bridge 1

    expect(bridgeFactory).toHaveBeenCalledTimes(2);
    expect(disposes[0]).toHaveBeenCalledTimes(1); // predecessor disposed on re-resolve (no leak)
    expect(attaches[1]).toHaveBeenCalledTimes(1); // new bridge attached
    expect(disposes[1]).not.toHaveBeenCalled();
  });
});

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
});

describe("placeholderHtml", () => {
  it("contains a strict CSP meta with default-src 'none'", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toMatch(/Content-Security-Policy/);
    expect(html).toMatch(/default-src 'none'/);
  });

  it("contains the not-connected copy", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toContain("Not connected");
  });

  it("threads the cspSource into the style/img directives", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toContain("img-src vscode-resource:");
    expect(html).toContain("style-src 'nonce-");
  });

  it("does NOT contain any inline <script> (no script without a nonce)", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).not.toMatch(/<script/i);
  });

  it("uses a nonce on its inline <style> rather than allowing 'unsafe-inline'", () => {
    const html = placeholderHtml("vscode-resource:");
    expect(html).toMatch(/<style nonce="[A-Za-z0-9]{32}">/);
    expect(html).not.toContain("unsafe-inline");
  });

  it("produces a fresh nonce per render", () => {
    const randomSpy = vi.spyOn(Math, "random");
    const a = placeholderHtml("vscode-resource:");
    const b = placeholderHtml("vscode-resource:");
    randomSpy.mockRestore();
    const nonceA = /<style nonce="([A-Za-z0-9]{32})">/.exec(a)?.[1];
    const nonceB = /<style nonce="([A-Za-z0-9]{32})">/.exec(b)?.[1];
    expect(nonceA).toBeDefined();
    expect(nonceB).toBeDefined();
    expect(nonceA).not.toEqual(nonceB);
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

  it("nonce-gates the stylesheet and threads cspSource into style/img/font directives", () => {
    const html = webviewHtml(opts);
    expect(html).toContain(`<link rel="stylesheet" nonce="${opts.nonce}"`);
    expect(html).toContain(`style-src 'nonce-${opts.nonce}' vscode-resource:`);
    expect(html).toContain("img-src vscode-resource: data:");
    expect(html).toContain("font-src vscode-resource:");
  });
});
