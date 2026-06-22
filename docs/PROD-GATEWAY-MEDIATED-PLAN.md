# PROD — Gateway-mediated execution (host-agent, no PG on hosts) — plan

Kullanıcı (2026-06-22): **"artık gerçek sistem istiyorum, denemekten sıkıldım, kullanmak istiyorum, sıfırdan
prod seviyesinde kuracağız."** İlk gerçek hedef = **optiway** (held-for-review), performer host = **davinci**
(Linux, tailnet'te; claude/go YOK, db1 PG'ye ULAŞAMAZ), control-plane = **canlı hetzner k8s gateway**.

Kullanıcı itirazı (mimari belirleyici): *"davinci neden DB'ye bağlansın? DB/API hetzner k8s'te olmayacak mı?"*
→ **KARAR: gateway-mediated mimari.** Host-agent YALNIZ gateway HTTP ile konuşur, Postgres'e ASLA dokunmaz;
tüm DB hetzner k8s'te kalır. Bu, prod-grade + minimum-blast-radius (DB credential'ı host'lara dağıtılmaz).

> ⚠️ DİSİPLİN (Q3c ile AYNI): UYDURMA YOK · frozen-additive (ADR-0021; mevcut daemon/gateway sözleşmeleri kırılmaz) ·
> token/secret disiplini · HER task: Go gate (build+vet+golangci-0+`-race`) + GERÇEK-PG + commit develop + **CI yeşil**
> (`gh run view`) + ledger/ADR. Mevcut `cmd/conductor` (shared-PG daemon) DOKUNULMAZ; gateway-mediated yol ADDITIVE.

## §0 — Mevcut mimari (grounding; Explore haritası)
- **daemon-merkezli:** `cmd/conductor` `Conductor.Tick()` (`internal/conductor/conductor.go:612`) tek süreçte:
  PickReady/lease (`internal/registry`) → Develop (`internal/engine` `claude -p`) → Verify gate
  (`internal/verify` + holdout `internal/holdout`) → governance (`internal/governance` T1/T2 auto, T3/T4 held)
  → SquashMerge (`internal/conductor/merger.go`, `[task:<id>]` trailer, opt-in push) → markDone. PG'ye doğrudan.
- **gateway** (`cmd/conductor-api`): store'u READ + control (onboard/intake/distill/pause/resume/abort/approve) +
  events (/ws, /events). `requireAuth` bearer. Daemon ile **paylaşılan store** (DSN).
- **held-for-review:** `handleHumanRequired` → Status=`awaiting-approval` + branch saklanır; `/approve` →
  `Approved=true` (durable store) → Tick `PendingApproval`'ı önce görür → `mergeApproved` (drift re-verify + merge).
- **reconcile/reap** (`internal/reconcile`, k8s cronjob): lease TTL/dead-host reap + git-trailer'dan done türetme.
- **statestore** (FROZEN): Task{Status, Branch, Tier, Requires, Deps, Approved, AbortRequested, ScenarioID…} +
  Lease{ProjectID-PK, HostID, TaskID, AcquiredAt} + Host{ID, Capabilities, LastHeartbeat}.

## §1 — Hedef mimari (gateway-mediated)
**Sunucu tarafı (gateway, k8s, PG sahibi):** scheduling + lease defteri + reconcile/reap (mevcut cronjob) +
governance kararı + holdout servis + events + approval. Agent'a ADDITIVE bir **agent-API** açar.
**Host-agent (davinci, PG YOK):** gateway'den lease alır → optiway'i clone/worktree eder → Develop (claude) →
Verify (gate; holdout'u gateway'den çeker) → verdict'i gateway'e RAPOR eder → governance held ise awaiting-approval
rapor + decision poll → onayda merge+push (LOKAL; clone+git-cred host'ta) + done rapor. **engine/verify/merger/
provisioner paketleri AYNEN reuse** — yalnız "iş kaynağı" PG yerine gateway HTTP, "rapor" store yazımı yerine HTTP.

**Neden merge host-agent'ta:** clone + git-cred (gh token) zaten host'ta (develop için). "daemon'un diski" artık
"agent'ın diski". Gateway sadece merge SHA + done'ı kaydeder. (Sunucu git-cred tutmaz → blast-radius düşük.)

## §2 — Frozen-additive ÇİZGİSİ
- `cmd/conductor` (shared-PG daemon) + `internal/{engine,verify,governance,conductor,registry,provisioner,statestore}`
  sözleşmeleri DOKUNULMAZ (reuse edilir). Gateway mevcut route'ları DOKUNULMAZ.
- YENİ: gateway `agent-API` route'ları (additive) + `cmd/conductor-agent` (yeni binary) + agent HTTP client +
  (gerekirse) dar yeni store/registry yardımcıları (mevcut imzaları kırmadan).
- **Token:** MVP'de mevcut bearer token (tailscale ACL + header). Prod-sertleştirme follow-up: AYRI agent-token
  (yalnız agent-API scope). Git-cred (gh token) yalnız davinci'de (systemd EnvironmentFile 0600).

## §3 — FAZLAR (her faz gate+CI yeşil; otonom→reviewli)
**Faz G — gateway agent-API (Go, additive)** [ADR-0048]
- Yeni route'lar (auth'lu), registry+store reuse:
  - `POST /agent/lease` {host_id, capabilities[]} → host upsert + PickReady + AcquireLease → {task} | 204 (iş yok).
  - `POST /agent/heartbeat` {host_id} → HostHeartbeat.
  - `POST /agent/tasks/{id}/report` {phase, kind, payload} → events.Publish (editör backfill'i bunu görür) —
    progress/diff/log/decision. Token-free içerik; konuşma yok.
  - `POST /agent/tasks/{id}/result` {result:pass|changes, branch, checks[], diff} → store: pass+auto→? / pass+held→
    awaiting-approval (handleHumanRequired analoğu, ama agent merge edeceği için yalnız STATE) / fail→blocked.
  - `GET /agent/tasks/{id}/decision` → {state: approved|aborted|pending} (Approved/AbortRequested reuse).
  - `POST /agent/tasks/{id}/merged` {sha} → markDone (trailer zaten merge commit'inde).
  - `POST /agent/lease/release` {host_id, task_id} → ReleaseLeaseOwned.
  - `GET /agent/holdout?ref=` → holdout contents (HoldoutStore varsa; yoksa 404 → gate holdout'suz).
- `control_test.go` benzeri agent_test: lease/heartbeat/result(held|auto|fail)/decision/merged/release + auth + 404.
- Go gate + GERÇEK-PG. **conductor-api'ye additive; mevcut testler değişmez.**

**Faz A — conductor-agent host binary (Go, yeni cmd)** [ADR-0048]
- `cmd/conductor-agent`: config (`-gateway` URL, `-token`(env), `-host-id`, `-capabilities`, `-root`, `-develop-cmd`,
  `-repo`/`-base`, git-cred). Loop: heartbeat + `POST /agent/lease` → provisioner clone/worktree → engine Develop →
  verify Verify (holdout `GET /agent/holdout`) → `POST result` → governance held: poll `GET decision` → approved:
  merger SquashMerge(+push) → `POST merged`; auto: merge → merged; fail/abort: release. **PG client YOK.**
- Yeni `internal/agentclient` (HTTP, token header, retry/backoff, leak-guard). engine/verify/merger/provisioner reuse.
- Test: agentclient (stub gateway) + agent loop (fake engine/verify/merger; gerçek claude YOK) uçtan uca lease→
  develop→verify→result→decision→merge akışı. Go gate + `-race`.

**Faz D — deploy (prod)** [ops; kullanıcı + ben]
- Gateway: agent-API'li imajı GitOps (ArgoCD/keyless-OIDC) ile redeploy. (CI imaj + ArgoCD sync.)
- davinci kurulum: claude install + **login (KULLANICI; interaktif)** · Go (veya prebuilt linux/amd64 conductor-agent
  binary scp) · optiway gate toolchain (optiway'in testleri ne gerektiriyorsa — Faz O'da repo'dan tespit) · git-cred
  (gh token, optiway push) · tailscale gateway erişimi (conductor-api node mevcut) · token EnvironmentFile (0600) ·
  **systemd service** (conductor-agent, restart=always). Sağlık: heartbeat gateway'de görünür.

**Faz O — optiway ilk gerçek run** [reviewli]
- optiway gate'ini tespit (optiway repo'dan: build/test/lint komutları) + recipe.
- Onboard: gateway `POST /projects` repo=`github.com/everva/optiway`, base=`conductor/optiway`, governance=held.
- İlk gerçek senaryo intake (editör New Work veya conductorctl) → agent lease → develop → gate → **held** →
  editör/ben kullanıcıya sunar → **kullanıcı onayı** → agent merge `conductor/optiway`'e (main DOKUNULMAZ) → done.
- Gözlem: editör Command Center + Events + diff canlı; held→approve→merged uçtan uca GERÇEK.

## §4 — Prod-grade gereksinimler (her fazda gözetilecek)
- **Güvenlik:** host PG'ye dokunmaz · git-cred yalnız host'ta · token tailscale+header · envsafe claude'dan secret strip ·
  optiway main korunur (held-for-review) · agent-token scope (follow-up).
- **Dayanıklılık:** lease TTL + dead-host reap (mevcut reconcile cronjob host-heartbeat'i görür) · agent restart=always ·
  idempotent lease/merged · gateway 2-replica (mevcut).
- **Gözlemlenebilirlik:** her faz events (editör backfill) · agent heartbeat · gate verdict kayıtlı.
- **Geri-alınamazlık:** optiway ilk run held-for-review (auto-merge YOK) → kullanıcı onayı şart.

## DURUM
- Plan yazıldı. Mimari haritası grounded (Explore). **Faz G'den başlanacak** (gateway agent-API), her faz gate+CI yeşil.
- ADR-0048. develop @ (Faz G commit'i ile güncellenecek).
