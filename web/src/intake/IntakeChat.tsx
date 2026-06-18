// IntakeChat: the conversational intake view (3B-4b) — the CORE Conductor flow
// "converse → distill → review scenarios+holdout → edit YAML → human approve →
// ledger" (ADR-0005, ADR-0012). It is the human-in-the-loop drafting surface:
//
//   1. pick a project to intake into;
//   2. describe the work in free text and press Distill — the gateway's assisted
//      distiller (POST /distill) PROPOSES shape-validated scenarios + an
//      intake-ready YAML, persisting NOTHING;
//   3. review the proposed scenarios (id/lane/tier/deps/acceptance + the
//      repo-EXTERNAL hidden holdout ref) and EDIT the YAML — the YAML textarea is
//      the AUTHORITATIVE input to /intake; it is POSTed VERBATIM, never transformed;
//   4. Approve & add to ledger (confirm-gated) re-POSTs that exact YAML to
//      POST /intake, creating real tasks; the created/skipped ids are shown.
//
// Never-fabricate is surfaced honestly: a 422 from distill (the model gave nothing
// approvable) renders as "add more detail" GUIDANCE, not a crash or fake scenario.
// A 401 anywhere bubbles to onUnauthorized. The conversation/token are never logged.
import { useState } from "react";
import { ApiError, DistillNoScenariosError } from "../api/client.ts";
import type { DistillResult, IntakeResult, Project } from "../api/types.ts";
import { ConfirmDialog } from "../fleet/ConfirmDialog.tsx";
import type { PendingConfirm } from "../fleet/useFleetControls.ts";
import { ScenarioCard } from "./ScenarioCard.tsx";
import "./intake.css";

// IntakeClient is the NARROW surface IntakeChat consumes — the two endpoints of the
// converse→approve flow. ApiClient implements both with these exact shapes, so the
// real client is passed in production and a fake (no network) is passed in tests; we
// add no second HTTP path.
export interface IntakeClient {
  distill(projectId: string, conversation: string): Promise<DistillResult>;
  intake(projectId: string, yaml: string): Promise<IntakeResult>;
}

export interface IntakeChatProps {
  // projects the human may intake into (from the dashboard's known projects).
  projects: Project[];
  // client is the gateway surface (defaults supplied by the dashboard wrapper).
  client: IntakeClient;
  // onUnauthorized fires on a 401 so the app can sign out.
  onUnauthorized: () => void;
  // onViewTasks, when provided, offers a "View tasks" affordance after a successful
  // intake so the human can jump to the Fleet view for that project.
  onViewTasks?: (projectId: string) => void;
}

// Notice is a one-shot inline message (mirrors the fleet notice tones) for the
// distill/intake outcomes that are not a full proposal render.
interface Notice {
  tone: "ok" | "warn" | "error";
  message: string;
}

export function IntakeChat({
  projects,
  client,
  onUnauthorized,
  onViewTasks,
}: IntakeChatProps) {
  const [projectId, setProjectId] = useState<string>(projects[0]?.id ?? "");
  const [conversation, setConversation] = useState<string>("");
  const [proposal, setProposal] = useState<DistillResult | null>(null);
  // editedYaml is the AUTHORITATIVE input to /intake — initialised from the
  // distilled yaml, then freely edited by the human. We POST it verbatim.
  const [editedYaml, setEditedYaml] = useState<string>("");
  const [distilling, setDistilling] = useState<boolean>(false);
  const [approving, setApproving] = useState<boolean>(false);
  const [notice, setNotice] = useState<Notice | null>(null);
  // yamlError holds the inline parse error from a 400 on /intake so the human can
  // fix the YAML in place.
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [result, setResult] = useState<IntakeResult | null>(null);
  const [pending, setPending] = useState<PendingConfirm | null>(null);

  const hasProjects = projects.length > 0;
  const canDistill =
    hasProjects && projectId !== "" && conversation.trim() !== "" && !distilling;

  async function runDistill() {
    if (!canDistill) {
      return;
    }
    setDistilling(true);
    setNotice(null);
    setResult(null);
    setYamlError(null);
    try {
      const res = await client.distill(projectId, conversation);
      setProposal(res);
      setEditedYaml(res.yaml);
    } catch (err: unknown) {
      // Clear any stale proposal so the human re-distills cleanly.
      setProposal(null);
      setEditedYaml("");
      if (err instanceof DistillNoScenariosError) {
        // NEVER-FABRICATE contract surfaced as guidance, not a crash.
        setNotice({
          tone: "warn",
          message:
            "Couldn't distill an approvable scenario from that. Add more detail — " +
            "explicit acceptance criteria, a capability lane, and a risk tier — " +
            `then distill again. (gateway: ${err.message})`,
        });
      } else if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
      } else if (err instanceof ApiError) {
        setNotice({
          tone: "error",
          message: `Distill failed (${err.status}): ${err.message}`,
        });
      } else {
        setNotice({ tone: "error", message: "Could not reach the gateway." });
      }
    } finally {
      // Spinner is ALWAYS cleared — never a stuck spinner.
      setDistilling(false);
    }
  }

  // requestApprove opens the confirm gate; the actual POST happens on confirm. This
  // creates REAL tasks in the ledger, so it is confirm-gated like Abort/Approve.
  function requestApprove() {
    if (editedYaml.trim() === "" || approving) {
      return;
    }
    setPending({
      // Reuse the fleet's confirm shape; "approve" is the matching action verb and
      // ConfirmDialog only renders title/body/confirmLabel/tone.
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
    setNotice(null);
    setYamlError(null);
    try {
      // POST the EDITED yaml verbatim — the human-reviewed source of truth.
      const res = await client.intake(projectId, editedYaml);
      setResult(res);
      setProposal(null);
      setNotice({
        tone: "ok",
        message: `Added to ledger: ${res.created.length} created, ${res.skipped.length} skipped.`,
      });
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 400) {
        // Invalid YAML — show the parse error inline so the human can fix it.
        setYamlError(err.message);
      } else if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
      } else if (err instanceof ApiError) {
        setNotice({
          tone: "error",
          message: `Intake failed (${err.status}): ${err.message}`,
        });
      } else {
        setNotice({ tone: "error", message: "Could not reach the gateway." });
      }
    } finally {
      setApproving(false);
    }
  }

  return (
    <section className="intake" aria-label="Intake">
      <div className="intake-intro fleet-panel">
        <div className="fleet-panel-head">
          <h2>Intake — converse → distill → review → approve</h2>
        </div>
        <p className="intake-help fleet-panel-body">
          Describe the work in plain language. The assistant proposes shape-validated
          scenarios for you to review and edit; nothing is persisted until you approve.
        </p>
      </div>

      {!hasProjects && (
        <p role="status" className="intake-empty">
          No projects yet. Onboard a project from the Fleet view first, then return here
          to intake work into it.
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

              <label className="intake-field">
                <span className="intake-label">Conversation</span>
                <textarea
                  className="intake-textarea"
                  aria-label="Conversation"
                  placeholder="Describe the work: what to build, acceptance criteria, the capability lane, the risk tier…"
                  rows={8}
                  value={conversation}
                  onChange={(e) => setConversation(e.target.value)}
                />
              </label>

              <div className="intake-actions">
                <button
                  type="button"
                  className="fleet-btn primary"
                  disabled={!canDistill}
                  onClick={() => void runDistill()}
                >
                  {distilling
                    ? "Distilling…"
                    : proposal !== null
                      ? "Re-distill"
                      : "Distill"}
                </button>
                {distilling && (
                  <span className="intake-spinner" role="status" aria-live="polite">
                    Distilling…
                  </span>
                )}
              </div>
            </div>
          </div>

          {notice !== null && (
            <div
              className={`intake-notice ${notice.tone}`}
              role={notice.tone === "error" ? "alert" : "status"}
              aria-live="polite"
            >
              <span>{notice.message}</span>
              <button
                type="button"
                className="intake-notice-dismiss"
                aria-label="Dismiss"
                onClick={() => setNotice(null)}
              >
                ×
              </button>
            </div>
          )}

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
                    className="fleet-btn"
                    onClick={() => onViewTasks(projectId)}
                  >
                    View tasks for {projectId}
                  </button>
                )}
              </div>
            </div>
          )}

          {proposal !== null && (
            <div className="intake-proposal">
              <div className="fleet-panel">
                <div className="fleet-panel-head">
                  <h2>Proposed scenarios — review before approving</h2>
                  <span className="muted intake-proposal-note">
                    The assistant proposed these. Review the holdout refs and edit the
                    YAML below; nothing is in the ledger yet.
                  </span>
                </div>
                <div className="fleet-panel-body intake-cards">
                  {proposal.scenarios.length === 0 ? (
                    <p className="fleet-empty">No scenarios in the proposal.</p>
                  ) : (
                    proposal.scenarios.map((s) => (
                      <ScenarioCard key={s.ID} scenario={s} />
                    ))
                  )}
                </div>
              </div>

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
