// IntakeChat: the CONVERSATIONAL intake view (Faz-Q / Q3b, evolving 3B-4b) — a Claude-Code-style
// "describe → the assistant drafts (or asks for more) → review the plan → approve" loop, replacing
// the old one-shot textarea+Distill form (the user's "intake çok basit, claude code gibi olsun").
//
//   1. pick a project to intake into;
//   2. CHAT the work: each message you Send is appended to a multi-turn thread and the WHOLE
//      conversation (all your turns) is handed to the gateway distiller (POST /distill) — so detail
//      ACCUMULATES across turns. The assistant replies in the thread: a draft summary when it
//      proposes scenarios, or — honoring NEVER-FABRICATE — a "needs more detail" CLARIFYING reply
//      when it can't (a 422 becomes guidance, not a crash). Nothing is persisted while you chat.
//   3. when scenarios are drafted, review them (id/lane/tier/deps/acceptance + the repo-EXTERNAL
//      hidden holdout) and EDIT the YAML — the YAML textarea is the AUTHORITATIVE input to /intake,
//      POSTed VERBATIM, never transformed;
//   4. Approve & add to ledger (confirm-gated) re-POSTs that exact YAML to POST /intake.
//
// The "Write spec directly" path (claude-free authoring straight into the YAML) is preserved.
// A 401 anywhere bubbles to onUnauthorized. The conversation/token are never logged.
//
// CLARIFYING QUESTIONS (Q3c, ADR-0047): when the distiller is too unsure to draft scenarios it may
// return SPECIFIC multiple-choice questions (Claude Code's AskUserQuestion — "ask, don't assume")
// instead of guessing. Those render as a QuestionCard in the thread; the director's answers fold back
// into the conversation as a turn ("Q: … → A: …") and re-distill. A 422 (the model produced NOTHING,
// not even questions) still surfaces as the never-fabricate guidance. Conversation accumulates
// CLIENT-side over the single-string /distill, which now also carries the optional `questions`.
import { useRef, useState } from "react";
import { ArrowRight } from "lucide-react";
import { ApiError, DistillNoScenariosError } from "../api/client.ts";
import { holdoutIdFromRef } from "./holdoutRef.ts";
import type { DistillResult, IntakeResult, Project, Question } from "../api/types.ts";
import { ConfirmDialog } from "../fleet/ConfirmDialog.tsx";
import type { PendingConfirm } from "../fleet/useFleetControls.ts";
import { ScenarioCard } from "./ScenarioCard.tsx";
import { QuestionCard, type QuestionAnswer } from "./QuestionCard.tsx";
import "./intake.css";

// IntakeClient is the NARROW surface IntakeChat consumes — the two endpoints of the
// converse→approve flow. ApiClient implements both with these exact shapes, so the
// real client is passed in production and a fake (no network) is passed in tests; we
// add no second HTTP path. `distill` still takes a single conversation string (the
// accumulated turns); a structured multi-turn request is a frozen-additive follow-up.
export interface IntakeClient {
  distill(projectId: string, conversation: string): Promise<DistillResult>;
  // distillStream is the OPTIONAL streaming distill (Q3c.4): the SAME result, with
  // live progress (the model-output line COUNT) via onProgress while the model works.
  // When absent — or when the transport can't stream (the fork postMessage bridge) —
  // IntakeChat uses distill and shows a plain spinner. ApiClient implements it.
  distillStream?(
    projectId: string,
    conversation: string,
    onProgress?: (lines: number) => void,
  ): Promise<DistillResult>;
  intake(projectId: string, yaml: string): Promise<IntakeResult>;
  // putHoldout stores the (reviewed) auto-generated hidden-holdout body before intake (Faz-S S5),
  // so the agent's gate can run it. OPTIONAL: absent on a fake/older client → the approve flow
  // simply skips the holdout PUT (the scenario still intakes).
  putHoldout?(id: string, files: Record<string, string>): Promise<{ locator: string }>;
  // enhance expands a rough request into a detailed Turkish spec by reading the project's REAL
  // code on an agent (code-aware). It creates a job + polls until done; the resolved string is
  // the enhanced spec the director then reviews/edits before distilling. OPTIONAL: absent on a
  // fake/older client → the Enhance button is hidden.
  enhance?(projectId: string, roughSpec: string): Promise<string>;
}

export interface IntakeChatProps {
  // projects the human may intake into (from the dashboard's known projects).
  projects: Project[];
  // client is the gateway surface (defaults supplied by the dashboard wrapper).
  client: IntakeClient;
  // onUnauthorized fires on a 401 so the app can sign out.
  onUnauthorized: () => void;
  // onViewTasks, when provided, offers a "View tasks" affordance after a successful
  // intake so the human can jump to the board for that project.
  onViewTasks?: (projectId: string) => void;
}

// A turn in the intake conversation. "you" is the director; "assistant" is the distiller's
// reply (a draft summary, a clarifying "needs more detail" guidance [tone:"warn"], or an
// error [tone:"error"]). Token-free — only the human-authored / gateway-derived text.
type ChatRole = "you" | "assistant";
interface ChatMsg {
  id: number;
  role: ChatRole;
  text: string;
  tone?: "warn" | "error";
}

// SPEC_TEMPLATE seeds the "Write spec directly" path — an operator who already knows
// the work (or has no claude-assisted distiller available) authors the scenario YAML
// straight into the authoritative editor and dispatches it. It is a valid one-scenario
// shape (ADR-0012): id/title/lane/tier + ≥1 acceptance + a repo-EXTERNAL holdout ref.
const SPEC_TEMPLATE = `id: NEW-1
title: Describe the work in one line
lane: backend
tier: T2
acceptance:
  - A concrete, checkable acceptance criterion
hidden_holdout_ref: store://holdouts/NEW-1/holdout_test.go
`;

export function IntakeChat({
  projects,
  client,
  onUnauthorized,
  onViewTasks,
}: IntakeChatProps) {
  const [projectId, setProjectId] = useState<string>(projects[0]?.id ?? "");
  // The multi-turn conversation thread (Q3b). Your turns accumulate; the assistant replies inline.
  const [messages, setMessages] = useState<ChatMsg[]>([]);
  const [draft, setDraft] = useState<string>("");
  // pendingQuestions holds the distiller's clarifying questions (Q3c) until answered;
  // null means there are none to answer right now.
  const [pendingQuestions, setPendingQuestions] = useState<Question[] | null>(null);
  const [proposal, setProposal] = useState<DistillResult | null>(null);
  // editedYaml is the AUTHORITATIVE input to /intake — initialised from the
  // distilled yaml, then freely edited by the human. We POST it verbatim.
  const [editedYaml, setEditedYaml] = useState<string>("");
  const [distilling, setDistilling] = useState<boolean>(false);
  // enhancing is true while an agent reads the project code and expands the rough draft into a
  // detailed Turkish spec (code-aware enhance); it can take a couple of minutes.
  const [enhancing, setEnhancing] = useState<boolean>(false);
  // progressLines is the live model-output line count during a streaming distill
  // (Q3c.4); null when not streaming (or before the first progress event).
  const [progressLines, setProgressLines] = useState<number | null>(null);
  const [approving, setApproving] = useState<boolean>(false);
  // yamlError holds the inline parse error from a 400 on /intake so the human can fix it in place.
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [result, setResult] = useState<IntakeResult | null>(null);
  const [pending, setPending] = useState<PendingConfirm | null>(null);
  // directMode shows the authoritative YAML editor + Dispatch WITHOUT a distill
  // proposal — the "Write spec directly" path (no claude-assisted distiller needed).
  const [directMode, setDirectMode] = useState<boolean>(false);
  const nextMsgId = useRef<number>(0);

  const hasProjects = projects.length > 0;
  const canSend = hasProjects && projectId !== "" && draft.trim() !== "" && !distilling;
  const canPickProject = hasProjects && projectId !== "";
  // The authoritative YAML editor + Dispatch show for EITHER path: an assisted
  // proposal, or a direct-authored spec.
  const showEditor = proposal !== null || directMode;

  function addMsg(role: ChatRole, text: string, tone?: "warn" | "error") {
    setMessages((prev) => [
      ...prev,
      { id: nextMsgId.current++, role, text, ...(tone ? { tone } : {}) },
    ]);
  }

  // runEnhance sends the rough draft to an agent that READS the project's real code and returns a
  // detailed Turkish spec, which replaces the draft for the director to review/edit before sending.
  // Code-aware: the grounding (real file/model names) comes from the agent, not a guess.
  async function runEnhance() {
    if (!client.enhance || projectId === "" || draft.trim() === "" || enhancing || distilling) return;
    const rough = draft.trim();
    setEnhancing(true);
    addMsg("assistant", "Talebin kodu incelenerek detaylandırılıyor… (birkaç dakika sürebilir)");
    try {
      const enhanced = await client.enhance(projectId, rough);
      setDraft(enhanced);
      addMsg("assistant", "Detaylı Türkçe spec hazır — aşağıda gözden geçir, gerekirse düzenle ve Gönder.");
    } catch (err) {
      if (err instanceof Error && (err as { status?: number }).status === 401) {
        onUnauthorized?.();
        return;
      }
      addMsg("assistant", `Enhance başarısız: ${err instanceof Error ? err.message : String(err)}`, "error");
    } finally {
      setEnhancing(false);
    }
  }

  // writeSpecDirectly opens the YAML editor with a starter template (or keeps the
  // current edits) so an operator can author + dispatch a spec without distilling.
  function writeSpecDirectly() {
    setProposal(null);
    setResult(null);
    setYamlError(null);
    setDirectMode(true);
    setEditedYaml((cur) => (cur.trim() === "" ? SPEC_TEMPLATE : cur));
  }

  // runDistill hands the ACCUMULATED conversation to the gateway distiller and folds the
  // outcome into the thread. Scenarios → a draft summary + the plan-preview below; a 422 →
  // the never-fabricate guidance as a clarifying reply; other failures → an error reply.
  async function runDistill(conversation: string) {
    setDistilling(true);
    setResult(null);
    setYamlError(null);
    setProgressLines(null);
    // Each distill starts clean: drop any stale clarifying questions from a prior turn.
    setPendingQuestions(null);
    try {
      // Prefer the streaming distill (live progress) when the client supports it;
      // otherwise the plain distill. Both resolve to the SAME DistillResult.
      const res = client.distillStream
        ? await client.distillStream(projectId, conversation, (n) => setProgressLines(n))
        : await client.distill(projectId, conversation);
      // Clarifying turn (Q3c): the distiller asked for more detail instead of drafting.
      if (res.questions !== undefined && res.questions.length > 0) {
        setPendingQuestions(res.questions);
        setProposal(null);
        setEditedYaml("");
        setDirectMode(false);
        addMsg(
          "assistant",
          "A few quick questions to get this right — answer below, or add detail in the message box.",
        );
        return;
      }
      setProposal(res);
      setEditedYaml(res.yaml);
      setDirectMode(false);
      addMsg(
        "assistant",
        res.scenarios.length > 0
          ? `Drafted ${res.scenarios.length} scenario${res.scenarios.length === 1 ? "" : "s"} — review the plan below, edit the YAML, and approve.`
          : "That distilled to an empty proposal. Add more detail and send again.",
      );
    } catch (err: unknown) {
      // Clear any stale proposal so the human re-sends cleanly.
      setProposal(null);
      setEditedYaml("");
      if (err instanceof DistillNoScenariosError) {
        // NEVER-FABRICATE surfaced as a clarifying reply, not a crash (CC "ask, don't assume").
        addMsg(
          "assistant",
          "I couldn't draft an approvable scenario from that yet. Add more detail — explicit " +
            "acceptance criteria, a capability lane, and a risk tier — then send again. " +
            `(gateway: ${err.message})`,
          "warn",
        );
      } else if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
      } else if (err instanceof ApiError) {
        // A 5xx means the distiller itself is unavailable (e.g. no claude reachable). The
        // claude-FREE "Write spec directly" path still works, so point the director at it
        // instead of leaving them stuck — the exact escape hatch for a claude-less deployment.
        const hint =
          err.status >= 500 ? ' The distiller is unavailable — use "Write spec directly" below to author the spec yourself.' : "";
        addMsg("assistant", `Distill failed (${err.status}): ${err.message}.${hint}`, "error");
      } else {
        addMsg(
          "assistant",
          'Could not reach the distiller. Use "Write spec directly" below to author the spec yourself.',
          "error",
        );
      }
    } finally {
      // Spinner is ALWAYS cleared — never a stuck spinner.
      setDistilling(false);
    }
  }

  // sendYouTurn appends text as a director turn and re-distills with the WHOLE conversation
  // (all your turns), so detail accumulates across the thread. The existing single-string
  // /distill is reused. youTurns is computed BEFORE addMsg (setState is async).
  function sendYouTurn(text: string) {
    const youTurns = [...messages.filter((m) => m.role === "you").map((m) => m.text), text];
    addMsg("you", text);
    void runDistill(youTurns.join("\n\n"));
  }

  // send dispatches the composer draft as a director turn.
  function send() {
    if (!canSend) {
      return;
    }
    const text = draft.trim();
    setDraft("");
    sendYouTurn(text);
  }

  // submitAnswers folds the clarifying answers into the conversation as a director turn
  // ("Q: … → A: …") so the distiller has the clarifications on the next pass, then re-distills.
  function submitAnswers(answers: QuestionAnswer[]) {
    const answerText = answers.map((a) => `Q: ${a.question}\nA: ${a.answer}`).join("\n\n");
    setPendingQuestions(null);
    sendYouTurn(answerText);
  }

  // requestApprove opens the confirm gate; the actual POST happens on confirm. This
  // creates REAL tasks in the ledger, so it is confirm-gated like Abort/Approve.
  function requestApprove() {
    if (editedYaml.trim() === "" || approving) {
      return;
    }
    setPending({
      kind: "approve",
      projectId,
      taskId: undefined,
      title: "Approve & add to ledger",
      body:
        "This creates real tasks for project " +
        `"${projectId}" from the YAML below, exactly as edited. Proceed?`,
      confirmLabel: "Approve & add",
      tone: "primary",
    });
  }

  async function confirmApprove() {
    setPending(null);
    setApproving(true);
    setYamlError(null);
    try {
      // Faz-S S5: if the distiller auto-generated a holdout AND the proposal's first scenario
      // points at a holdouts ref, STORE the reviewed holdout body first so the gate can run it.
      // Best-effort but reported: a holdout store failure is surfaced (the director chose it).
      const holdout = proposal?.holdout;
      const ref = proposal?.scenarios?.[0]?.hidden_holdout_ref ?? "";
      const holdoutId = holdoutIdFromRef(ref);
      if (holdout && Object.keys(holdout).length > 0 && holdoutId !== "" && client.putHoldout) {
        await client.putHoldout(holdoutId, holdout);
      }
      // POST the EDITED yaml verbatim — the human-reviewed source of truth.
      const res = await client.intake(projectId, editedYaml);
      setResult(res);
      setProposal(null);
      addMsg(
        "assistant",
        `Added to ledger: ${res.created.length} created, ${res.skipped.length} skipped.`,
      );
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 400) {
        // Invalid YAML — show the parse error inline so the human can fix it.
        setYamlError(err.message);
      } else if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
      } else if (err instanceof ApiError) {
        addMsg("assistant", `Intake failed (${err.status}): ${err.message}`, "error");
      } else {
        addMsg("assistant", "Could not reach the gateway.", "error");
      }
    } finally {
      setApproving(false);
    }
  }

  return (
    <section className="intake" aria-label="Intake">
      <div className="intake-intro fleet-panel">
        <div className="fleet-panel-head">
          <h2>Intake — describe the work, review the plan, approve</h2>
        </div>
        <p className="intake-help fleet-panel-body">
          Chat the work in plain language. The assistant proposes shape-validated scenarios (or
          asks for more detail); nothing is persisted until you approve the plan.
        </p>
      </div>

      {!hasProjects && (
        <p role="status" className="intake-empty">
          No projects yet. Onboard a project from the board first, then return here to intake work
          into it.
        </p>
      )}

      {hasProjects && (
        <>
          <div className="intake-controls fleet-panel">
            <div className="fleet-panel-body intake-form">
              <label className="intake-field">
                <span className="intake-label">Project</span>
                <select
                  className="intake-select"
                  aria-label="Project to intake into"
                  value={projectId}
                  onChange={(e) => setProjectId(e.target.value)}
                >
                  {projects.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.id} ({p.repo})
                    </option>
                  ))}
                </select>
              </label>

              {messages.length > 0 && (
                <div className="intake-thread" role="log" aria-label="Intake conversation">
                  {messages.map((m) => (
                    <div
                      key={m.id}
                      className={`intake-msg ${m.role}${m.tone ? ` ${m.tone}` : ""}`}
                      data-testid="intake-msg"
                    >
                      <span className="intake-msg-role">
                        {m.role === "you" ? "You" : "Assistant"}
                      </span>
                      <span className="intake-msg-text">{m.text}</span>
                    </div>
                  ))}
                </div>
              )}

              {pendingQuestions !== null && (
                <QuestionCard
                  questions={pendingQuestions}
                  onSubmit={submitAnswers}
                  disabled={distilling}
                />
              )}

              <label className="intake-field">
                <span className="intake-label">Message</span>
                <textarea
                  className="intake-textarea"
                  aria-label="Message"
                  placeholder="Describe the work: what to build, acceptance criteria, the capability lane, the risk tier…  (⌘/Ctrl+Enter to send)"
                  rows={5}
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                      e.preventDefault();
                      send();
                    }
                  }}
                />
              </label>

              <div className="intake-actions">
                <button
                  type="button"
                  className="fleet-btn primary"
                  disabled={!canSend}
                  onClick={send}
                >
                  {distilling ? "Sending…" : "Send"}
                </button>
                {typeof client.enhance === "function" && (
                  <button
                    type="button"
                    className="fleet-btn"
                    title="Kodu inceleyip talebini detaylı Türkçe spec'e çevirir"
                    disabled={!canPickProject || draft.trim() === "" || distilling || enhancing}
                    onClick={runEnhance}
                  >
                    {enhancing ? "Geliştiriliyor…" : "✨ Geliştir (Türkçe)"}
                  </button>
                )}
                <span className="intake-or">or</span>
                <button
                  type="button"
                  className="fleet-btn"
                  disabled={!canPickProject}
                  onClick={writeSpecDirectly}
                >
                  Write spec directly
                </button>
                {distilling && (
                  <span className="intake-spinner" role="status" aria-live="polite">
                    {progressLines !== null
                      ? `Distilling… (${progressLines} ${progressLines === 1 ? "line" : "lines"})`
                      : "Distilling…"}
                  </span>
                )}
                {enhancing && (
                  <span className="intake-spinner" role="status" aria-live="polite">
                    Kod inceleniyor…
                  </span>
                )}
              </div>
            </div>
          </div>

          {result !== null && (
            <div className="intake-result fleet-panel" data-testid="intake-result">
              <div className="fleet-panel-head">
                <h2>Added to ledger</h2>
              </div>
              <div className="fleet-panel-body intake-result-body">
                <p>
                  Created{" "}
                  {result.created.length === 0 ? (
                    <span className="muted">none</span>
                  ) : (
                    result.created.map((id) => (
                      <span key={id} className="chip" data-testid="created-id">
                        {id}
                      </span>
                    ))
                  )}
                </p>
                {result.skipped.length > 0 && (
                  <p>
                    Skipped{" "}
                    {result.skipped.map((id) => (
                      <span key={id} className="chip muted" data-testid="skipped-id">
                        {id}
                      </span>
                    ))}
                  </p>
                )}
                {onViewTasks !== undefined && (
                  <button
                    type="button"
                    className="fleet-btn primary"
                    onClick={() => onViewTasks(projectId)}
                  >
                    View on board <ArrowRight size={14} strokeWidth={2.2} />
                  </button>
                )}
              </div>
            </div>
          )}

          {showEditor && (
            <div className="intake-proposal">
              {proposal !== null && (
                <div className="fleet-panel">
                  <div className="fleet-panel-head">
                    <h2>Proposed plan — review before approving</h2>
                    <span className="muted intake-proposal-note">
                      The assistant proposed these. Review the holdout refs and edit the YAML
                      below; nothing is in the ledger yet.
                    </span>
                  </div>
                  <div className="fleet-panel-body intake-cards">
                    {proposal.scenarios.length === 0 ? (
                      <p className="fleet-empty">No scenarios in the proposal.</p>
                    ) : (
                      proposal.scenarios.map((s) => <ScenarioCard key={s.id} scenario={s} />)
                    )}
                  </div>
                </div>
              )}

              {/* Faz-S S5: the auto-generated hidden holdout — the test that will PROVE the work.
                  Review it before approving; on approve it is stored so the gate runs it. */}
              {proposal?.holdout && Object.keys(proposal.holdout).length > 0 && (
                <div className="fleet-panel" data-testid="holdout-review">
                  <div className="fleet-panel-head">
                    <h2>Auto-generated holdout test — review before approving</h2>
                    <span className="muted intake-proposal-note">
                      The hidden test the gate runs to prove this work. It is stored on approve;
                      you (the director) are the check on it.
                    </span>
                  </div>
                  <div className="fleet-panel-body">
                    {Object.entries(proposal.holdout).map(([path, content]) => (
                      <div key={path} className="intake-holdout-file">
                        <span className="intake-label mono">{path}</span>
                        <pre className="intake-textarea mono" data-testid="holdout-file">
                          {content}
                        </pre>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              <div className="fleet-panel">
                <div className="fleet-panel-head">
                  <h2>Intake YAML — authoritative; edit before approving</h2>
                </div>
                <div className="fleet-panel-body intake-yaml-body">
                  <label className="intake-field">
                    <span className="intake-label">
                      This exact YAML is what gets added to the ledger.
                    </span>
                    <textarea
                      className="intake-textarea mono intake-yaml"
                      aria-label="Intake YAML"
                      rows={16}
                      value={editedYaml}
                      onChange={(e) => {
                        setEditedYaml(e.target.value);
                        setYamlError(null);
                      }}
                    />
                  </label>
                  {yamlError !== null && (
                    <p className="intake-yaml-error" role="alert">
                      Invalid YAML: {yamlError}
                    </p>
                  )}
                  <div className="intake-actions">
                    <button
                      type="button"
                      className="fleet-btn primary"
                      disabled={approving || editedYaml.trim() === ""}
                      onClick={requestApprove}
                    >
                      {approving ? "Adding…" : "Approve & add to ledger"}
                    </button>
                  </div>
                </div>
              </div>
            </div>
          )}
        </>
      )}

      <ConfirmDialog
        pending={pending}
        onConfirm={() => void confirmApprove()}
        onCancel={() => setPending(null)}
      />
    </section>
  );
}
