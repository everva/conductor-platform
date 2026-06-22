# ADR-0048 — Gateway-mediated execution (host-agent, no Postgres on hosts)

## Bağlam
Kullanıcı (2026-06-22) gerçek prod kullanıma geçmek istedi (ilk hedef **optiway**, performer host **davinci**,
canlı **hetzner k8s** gateway) ve kritik bir mimari itiraz koydu: *"davinci neden DB'ye bağlansın? DB/API
hetzner k8s'te olmayacak mı?"* Mevcut conductor mimarisi **daemon-merkezli**: `cmd/conductor` tek süreçte
PickReady/lease (registry) → Develop (engine, `claude -p`) → Verify gate → governance → SquashMerge yapar ve
**Postgres'e doğrudan** (`-dsn`) bağlanır. Bu modelde her performer host PG'ye erişmek zorundadır — davinci
hetzner-iç PG'ye (db1 `10.0.0.20:6432`) ULAŞAMIYOR (tailnet'te yalnız `conductor-api` gateway erişilebilir),
ve prod açısından DB-credential'ını host'lara dağıtmak blast-radius'u büyütür.

## Karar
**Yürütmeyi gateway-mediated yap: performer host-agent YALNIZ gateway HTTP ile konuşur, Postgres'e ASLA
dokunmaz.** Tüm DB hetzner k8s'te kalır; gateway, agent'a ADDITIVE bir **agent-API** açar ve daemon'un
kullandığı AYNI registry/statestore seam'lerini sunucu tarafında reuse eder.

- **Sunucu (gateway, k8s, PG sahibi):** scheduling/lease defteri/reconcile-reap (mevcut cronjob)/governance
  kararı/holdout/events/approval. Agent için yeni HTTP endpoint'ler.
- **Host-agent (davinci, PG YOK):** gateway'den lease → optiway clone/worktree → Develop (claude) → Verify
  (gate; holdout gateway'den) → verdict RAPOR → governance held ise awaiting-approval + decision poll → onayda
  merge+push (LOKAL; clone+git-cred host'ta) + done rapor. `engine/verify/merger/provisioner` paketleri AYNEN reuse.
- **Frozen-additive (ADR-0021):** mevcut `cmd/conductor` (shared-PG daemon) + internal execution paketleri +
  gateway mevcut route'ları DOKUNULMAZ. Shared-PG modeli backward-compat kalır; gateway-mediated yol additive.
- **Güvenlik:** host PG-credential tutmaz · git-cred yalnız host'ta · token tailscale+header · optiway main korunur
  (held-for-review). Prod-sertleştirme follow-up: AYRI agent-token (yalnız agent-API scope).

Plan: `docs/PROD-GATEWAY-MEDIATED-PLAN.md` (Faz G gateway agent-API → Faz A host-agent binary → Faz D deploy →
Faz O optiway ilk held-run).

## Sonuçlar (Faz G1 — lease lifecycle; bu commit)
- `cmd/conductor-api/agent.go`: lease lifecycle endpoint'leri (auth'lu), registry+store reuse:
  - `POST /projects/{id}/agent/lease` {host_id, capabilities[]} → host self-register (RegisterHost upsert) +
    `registry.PickReady(WithCapabilities)` + `AcquireLease` → 200 {task, lease} | **204** (iş yok / repo lease'li) |
    **409** (acquire race) | 404 (proje yok) | 400 (host_id/json) | 401.
  - `POST /agent/heartbeat` {host_id} → `HostHeartbeat` (reaper bunu görür) | 404 (kayıtsız) | 400.
  - `POST /projects/{id}/agent/lease/release` {host_id, task_id} → `ReleaseLeaseOwned` (owner-scoped, idempotent).
  - Paylaşılan `toTaskDTO` (handleProjectTasks de buna refactor — davranış birebir). Host hiç PG yazmaz; gateway yazar.
- **Doğrulama:** Go gate GREEN (build+vet+**golangci 0**+gofmt) + **`-race`** + **GERÇEK-PG** (conductor-api suite
  `TEST_DATABASE_URL`). `agent_test.go` 8 test: lease-ready / no-work-204-still-registers / capability-routing
  (linux→204, macos→200) / repo-busy-204 / unknown-404 / bad-requests(400/401) / heartbeat(200/404/400) /
  release(owner-drops, non-owner-idempotent-noop). Mevcut gateway testleri DEĞİŞMEDEN geçer (frozen-additive).

## Sonuçlar (Faz G2 — report/result/decision/merged; bu commit)
- `cmd/conductor-api/agent.go` (additive):
  - `POST .../agent/tasks/{task}/report` {phase,kind,payload} → `events.Event.Validate` + `bus.Publish` (editör
    backfill agent işini de görür). Geçersiz phase/kind → 400 (sessizce düşmez).
  - `POST .../agent/tasks/{task}/result` {result,branch,summary,checks} → KindDecision emit + **never-fake-green:**
    pass-değil → markBlocked/"blocked" · pass + `policyForProject` HumanRequired → awaiting-approval (branch saklanır,
    Approved=false) + KindInterventionNeeded → "hold" · pass + auto → "merge" (agent merge eder).
  - `GET .../agent/tasks/{task}/decision` → AbortRequested→"aborted" · awaiting-approval+Approved→"approved" · "pending".
  - `POST .../agent/tasks/{task}/merged` {sha} → markDone + Approved temizle + KindMerge.
  - `policyForProject`: **fail-safe = held-for-review** (boş/unknown GovernancePolicy → `governance.New(nil)` = TÜM
    tier'lar held; optiway bunu kullanır). "risk-layered" → DefaultPolicy (T1/T2 auto, T3/T4 held). "auto" → hepsi auto.
- **Doğrulama:** Go gate (golangci 0) + `-race` + GERÇEK-PG. `agent_test.go` G2: **held→/approve→decision-approved→
  merged→done TAM AKIŞ** (mevcut `/approve` endpoint reuse) + auto-merge + risk-layered(T1 merge/T4 hold) + blocked
  (status=blocked) + decision-aborted + bad-requests(400/404) + report(202/400 invalid-event). Merge AGENT'ta olur;
  gateway yalnız state + verdict-event. Mevcut gateway testleri değişmedi (frozen-additive).

## Sonuçlar (Faz A — conductor-agent host binary; bu commit)
- **`internal/agentclient`** (PROVEN): gateway agent-API'nin tipli HTTP client'ı (Lease 200/204/409 + Heartbeat +
  Scenarios + Report + Result + Decision + Merged + Release). **Token YALNIZ Authorization header'da, log/error'a sızmaz**
  (test: 404-error token içermez). httptest'e karşı 9 test (auth header + 204/409 no-work + error-map).
- **`internal/agent`** (orchestration PROVEN): `Runner` iki seam üstünde (`Gateway`=agentclient, `Executor`=develop/
  verify/merge) `Conductor.Tick`'in gateway-mediated analoğu. `RunOnce`: lease→scenario→develop→verify→Result→karar:
  **merge** (auto) | **hold→Decision-poll→approved→re-verify+merge | aborted** | **blocked** (never-fake-green) — lease
  HER yolda release (defer). 7 fake-test TÜM yolları + run-error→blocked + merge-error→yine-release kanıtlar.
- **`internal/agent` RealExecutor** (wiring; compile-PROVEN, runtime-validation davinci ilk-run): provisioner+engine+
  verify+merger'ı AYNEN reuse eder (compiler API-doğruluğunu teyit etti). Run=Workspace→Develop(claude stdin)→Verify
  (gate); Merge=approved ise base-merge-in+re-verify (drift guard, daemon mergeApproved aynası)+SquashMerge(+push).
  optiway main DOKUNULMAZ (base=conductor/optiway). Holdout noop (Faz G3 follow-up).
- **`cmd/conductor-agent`**: flag'ler (-gateway/-project/-repo/-base/-recipe-dir/-capabilities/-root/-develop-cmd/...);
  token+gh-token ENV'den (process-table'da değil); recipe `scaffolder.LoadRecipe`'tan (gate=tek merge-otoritesi, gate-siz
  recipe REDDEDİLİR); signal-handled `Loop` + heartbeat goroutine. **PG client YOK.**
- **Doğrulama:** Go gate (golangci 0 + gofmt) + `-race`. **DÜRÜST KAPSAM:** orchestration + client GERÇEKTEN test-edildi;
  RealExecutor compile-edildi + kanıtlı paketleri reuse eder ama develop/verify/merge'in optiway-recipe+claude ile
  uçtan uca koşusu **davinci ilk-run'da doğrulanır** (claude auth gerekir, otonom yapılamaz). Stub-performer entegrasyon
  testi = follow-up.

## Kalan
- **Faz G3 (ops.):** holdout-serve endpoint (gateway HoldoutStore) — ilk optiway run holdout'suz gate ile başlar.
- **Faz D/O:** deploy (gateway redeploy + davinci kurulum: claude login[KULLANICI] + agent systemd) + optiway
  held-for-review ilk gerçek run (KULLANICI onayı).
