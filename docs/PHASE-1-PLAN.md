# Faz-1 İnşa Planı — Conductor Platform çekirdeği

> Kod YAZILMADAN önce plan. ADR 0001–0012 kararlarını uygulanabilir iskelete çevirir.
> Orchestrator-only: kod sub-agent'lara delege edilir; bu doküman görev bölümü + sırayı tanımlar.
> **DOKUNMA:** optiway canlı Kontaktörü (davinci `~/optiway-conductor`) — doğrulama yeni throwaway test-repo ile.

## 1. Hedef (Faz-1 "ana yapı")
Tek host (davinci), merkezi Postgres. Bir test-projesini uçtan uca sür: **onboard → task → develop →
verify (deterministik gizli-holdout) → squash-merge → event akışı.** Canlıya dokunmadan.

## 2. Repo yapısı (Go monorepo + ince client)
```
conductor-platform/
├── cmd/
│   ├── conductor/        # ana daemon: conductor loop + governor + sentinel
│   └── conductorctl/     # CLI: onboard, intake, status, control
├── internal/
│   ├── registry/         # Postgres erişim: projects/hosts/leases/tasks/scenarios (ADR-0010)
│   ├── conductor/        # tick loop: pick→lease→engine→reconcile (ADR-0001 xirigo deseni)
│   ├── reconcile/        # runtime durumu git/gh'den TÜRET — drift yok (ADR-0010)
│   ├── engine/           # EngineAdapter interface + CommandEngine impl (ADR-0002)
│   ├── provisioner/      # workspace: clone + gh-token auth + branch + .conductor scaffold (ADR-0004/0009)
│   ├── scaffolder/       # stack-detect + per-stack reçete taslağı + readiness-gate (ADR-0009)
│   ├── verify/           # kalite kapısı orkestrasyonu: deterministik kanıt + taze-göz + gizli-holdout (ADR-0003/0012)
│   ├── sentinel/         # 3-katman liveness/recovery: deterministik + LLM-danışman + backstop (ADR-0006)
│   ├── events/           # Postgres LISTEN/NOTIFY publish/subscribe (ADR-0011)
│   ├── governor/         # concurrency/resource cap: global + repo-başına-1, host yük tavanı (ADR-0008)
│   └── intake/           # konuşma→senaryo+holdout asistanlı damıtma (ADR-0005/0012)
├── schema/               # JSON Schema TEK-KAYNAK: event, verdict, review, scenario, recipe (ADR-0011)
│   └── gen/{go,ts}/      # codegen çıktısı (Faz-3 UI için TS hazır)
├── migrations/           # Postgres DDL (goose/atlas)
├── recipes/              # per-stack profil kütüphanesi: node-react / go-api / ios-swift / next-web
└── docs/decisions/       # ADR'ler (mevcut)
```
LLM çağrıları = `claude -p` subprocess (ANTHROPIC_API_KEY yok). Host'ta tek Go binary; Postgres merkezde.

## 3. Postgres DDL taslağı (ADR-0010)
- `projects(id, repo, base_branch, host_id, readiness, recipe_pointer, governance_policy, created_at)`
- `hosts(id, name, capabilities text[])`
- `leases(project_id PK, host_id, task_id, acquired_at)` — repo-başına-tek-aktif, `FOR UPDATE`/advisory-lock (host-üstü)
- `tasks(id, project_id, lane, tier, status, requires text[], deps text[], branch, scenario_id, updated_at)`
- `scenarios(id, project_id, title, lane, tier, deps text[], acceptance jsonb, holdout_ref)`
- `events(id, ts, project_id, task_id, phase, kind, payload jsonb)` + `NOTIFY` trigger
- Audit/journal **DB'de değil** → repo `.conductor/journal/<task>.md` (ADR-0010)

## 4. JSON Schema tek-kaynak (ADR-0011, codegen → Go+TS)
`Event{ts,project,task,phase,kind,payload}` · `Verdict{result,branch,commit_sha,tests{...},files,summary}` ·
`ReviewResult{decision,evidence,notes}` · `Scenario{...}` · `Recipe{develop_cmd,verify_cmd,gates,governance}`
phase∈{plan,develop,test,review,verify,merge} · kind∈{started,progress,log,diff,decision,health,pr,merge,intervention-needed}

## 5. İnşa sırası (bağımlılık zinciri)
1. **schema/ + migrations/** — JSON Schema + Postgres DDL + codegen iskeleti. (Temel; her şey buna bağlı.)
2. **registry/** — Postgres CRUD + lease (FOR UPDATE). (Çekirdek veri katmanı.)
3. **engine/ (CommandEngine)** — interface dondu (ADR-0002); reçete komutlarını subprocess çağırır + JSON parse.
4. **conductor/ + reconcile/** — tick loop: pick(deps+lease+phase+readiness)→engine.Develop→engine.Verify→git'ten reconcile→ledger.
5. **provisioner/ + scaffolder/** — workspace kur + reçete taslağı + readiness-gate.
6. **events/** — NOTIFY publish (motor fazları + sentinel).
7. **verify/ + governor/ + sentinel/** — kalite kapısı + yük cap + 3-katman canlılık.
8. **intake/ + conductorctl/** — asistanlı damıtma + CLI.

## 6. Sub-agent görev bölümü (orchestrator-only)
- **Dalga A (sıralı, temel):** (1) schema+migrations → (2) registry. Tek sub-agent zinciri (bağımlı).
- **Dalga B (paralel, registry sonrası):** engine · provisioner+scaffolder · events — 3 paralel sub-agent.
- **Dalga C (entegrasyon):** conductor+reconcile (B'yi birleştirir) → verify+governor+sentinel → intake+CLI.
- Her sub-agent'a: ilgili ADR'ler + arayüz kontratı + "test yaz" + Rule#9 (bağımsız doğrula) verilir.

## 7. Uçtan uca doğrulama (canlıya DOKUNMADAN)
1. **Throwaway test-repo** oluştur (basit node veya go projesi, kendi GitHub'ımızda private).
2. `conductorctl onboard <repo>` → scaffolder reçete taslağı → insan onayı → `.conductor/` yazılır.
3. Readiness: testsizse "kalite altyapısı kur" bootstrap task'ı (ADR-0009).
4. `conductorctl intake` → bir senaryo + deterministik gizli-holdout.
5. Conductor tick → develop (per-task branch) → verify (gizli-holdout koşar) → squash-merge develop.
6. Event akışını izle (Postgres NOTIFY) + audit journal repo'da.
7. **Negatif test (Rule#9):** holdout'u kasıtlı kıran bir task → merge ENGELLENMELİ (blocked). Sessiz-yeşil olmamalı.

## 8. Faz-1 bitti sayılır kriteri
- Bir test-projesi onboard + en az 1 task uçtan uca otonom develop→verify→merge.
- Holdout-fail → merge engellendi (deterministik kapı kanıtı).
- Event akışı + audit journal üretiliyor.
- 3-4 paralel lease + repo-başına-1 governor çalışıyor.
- Hiçbir adımda canlı optiway'e dokunulmadı.

## 9. Açık/ertelenen
- Çok-host executor (ssh/agent) → Faz-2.
- Observability UI (VS Code/web) → Faz-3 (schema+control seam hazır).
- DF reçetesi (opsiyonel) → ihtiyaç olursa bir `recipes/` girdisi.
