# Faz-1 İnşa Planı — Conductor Platform çekirdeği

> Kod YAZILMADAN önce plan. ADR 0001–0018 kararlarını uygulanabilir iskelete çevirir.
> Orchestrator-only: kod sub-agent'lara delege; bu doküman görev bölümü + sırayı tanımlar.
> **DOKUNMA:** optiway canlı Kontaktörü (davinci `~/optiway-conductor`) — doğrulama yeni throwaway test-repo ile.
> Review sonrası revize (ADR-0013): **Faz-1a (walking skeleton) ÖNCE, Faz-1b (ölçek) SONRA.**

## 0. Terminoloji (karışmasın — review D4/O3)
- **`recipes/`** = platform monorepo'daki **stack profil ŞABLONLARI** (node-react, go-api, ios-swift).
- **`.conductor/`** = hedef repo içindeki **üretilmiş proje reçetesi** (şablondan + insan onayı, ADR-0009).
- **resource-governor** = concurrency/yük cap (ADR-0008). **governance-policy** = risk→merge-mode (ADR-0003). Farklı şeyler.

## 1. Repo yapısı (Go monorepo + ince client)
```
conductor-platform/
├── cmd/{conductor (daemon: loop+governor+sentinel), conductorctl (CLI)}
├── internal/
│   ├── statestore/   # ARAYÜZ + impl: 1a in-memory/dosya, 1b Postgres (ADR-0010/0013)
│   ├── registry/     # projects/hosts/lanes/leases/tasks/scenarios (statestore üstünde)
│   ├── conductor/    # tick loop: pick→lease→engine→reconcile (ADR-0001)
│   ├── reconcile/    # runtime git/gh'den TÜRET — ayrı job (ADR-0010/0016)
│   ├── engine/       # EngineAdapter + CommandEngine + performer kontratı + LLM-dayanıklılık (ADR-0002/0014)
│   ├── provisioner/  # per-project clone + per-task worktree + gh-token (ADR-0017)
│   ├── scaffolder/   # stack-detect + reçete taslağı + readiness-gate (ADR-0009) [1b]
│   ├── verify/       # deterministik kanıt + taze-göz + gizli-holdout enjeksiyon (ADR-0003/0018)
│   ├── sentinel/     # liveness/recovery: lock/watchdog/orphan + ayrı reconcile (ADR-0006/0016)
│   ├── governor/     # resource cap: global + repo-başına-1 (ADR-0008) [1b paralel]
│   ├── policy/       # governance: tier→merge-mode + human-merge (ADR-0003) 
│   ├── events/       # Postgres LISTEN/NOTIFY publish/subscribe (ADR-0011) [1b]
│   └── intake/       # konuşma→senaryo+holdout asistanlı damıtma (ADR-0005/0012) [1b]
├── schema/{*.json (JSON Schema tek-kaynak), gen/{go,ts}}   # [1b codegen]
├── migrations/       # Postgres DDL (goose) [1b]
├── recipes/          # stack profil ŞABLONLARI
├── deploy/           # macOS launchd plist + oauth-token + caffeinate (ADR-0015)
└── docs/decisions/   # ADR'ler
```
LLM = `claude -p` subprocess (subscription; ANTHROPIC_API_KEY yok). Host'ta tek Go binary; (1b) Postgres merkezde.

## 2. FAZ-1a — risk-öldüren walking skeleton (ÖNCE)
**Amaç:** en riskli varsayımları (engine mekaniği + LLM dayanıklılık + macOS gerçeği + holdout izolasyon) HEMEN test et.
- **State:** in-memory/dosya `StateStore` (Postgres YOK).
- **Intake/scaffolder:** YOK — senaryo + reçete + gizli-holdout ELLE yazılır (YAML + ayrı holdout dizini).
- **Engine:** tek `claude -p` performer (ADR-0014 kontratı: stdin senaryo+public-test, stdout schema-forced Verdict;
  pipeline performer-içinde; LLM-dayanıklılık: malformed/no-verdict→blocked, not-logged-in→dur+bildir).
- **Workspace:** per-project clone + per-task worktree (ADR-0017).
- **Verify:** deterministik kanıt (lokal test/lint/build exit-code) + gizli-holdout ayrı verify-worktree'ye enjekte (ADR-0018).
- **Merge:** per-task branch → squash-merge develop + `[task:<id>]` trailer (ADR-0004).
- **Runtime:** macOS launchd (FDA-scoped, oauth-token, PATH, caffeinate — ADR-0015) + deterministik liveness/recovery
  (mkdir-lock, stale-steal, progress-aware watchdog, orphan-sweep, AYRI reconcile job — ADR-0016).
- **Sentinel Katman-2 (LLM-danışman) YOK** (deterministik taban+backstop yeter).

**1a "bitti" kriteri:**
1. Bir throwaway test-repo'da 1 task: develop→verify→squash-merge uçtan uca otonom.
2. **Negatif test (Rule#9):** gizli-holdout'u kıran task → merge ENGELLENDİ (blocked) + holdout repo-dışı, performer görmedi.
3. **LLM-dayanıklılık testi:** bozuk/boş Verdict → sahte-yeşil DEĞİL, blocked.
4. macOS launchd'de reboot/uyku sonrası daemon ayakta + reconcile çalışıyor.
5. Canlı optiway'e dokunulmadı.

## 3. FAZ-1b — ölçek (SONRA)
- `StateStore` impl swap → **merkezi Postgres** (ADR-0010; DDL §5) + lease reaper + global-cap atomik.
- **resource-governor**: 3-4 paralel worker, repo-başına-1 (ADR-0008).
- **intake** asistanlı damıtma + **scaffolder** per-stack + readiness-bootstrap (meta-holdout, ADR-0009).
- **events**: Postgres LISTEN/NOTIFY + JSON Schema codegen (Go+TS, ADR-0011).
- **policy**: tier→merge-mode + **T3/T4 human-merge** akışı (ADR-0003) + `intervention-needed` event.
- Bağımsız heartbeat dosyası + harici stall-alert (xirigo deseni).

**1b "bitti" kriteri:** 3-4 proje paralel (repo-başına-1), Postgres lease atomik + reaper, intake'ten gelen
senaryo uçtan uca, T3 task → human-merge bekliyor, event-stream + UI-hazır şema.

## 4. macOS runtime (ADR-0015) — 1a'da kurulur
FDA-scoped binary · `claude setup-token`→`~/.conductor/oauth-token`(0600)→`CLAUDE_CODE_OAUTH_TOKEN` · plist explicit
PATH · `caffeinate -dimsu -w $$` · RunAtLoad+KeepAlive+StartInterval · preflight (claude/gh/[1b]postgres).

## 5. Postgres DDL taslağı (1b — ADR-0010)
- `projects(id, repo, base_branch, host_id, readiness, recipe_pointer, governance_policy)`
- `hosts(id, name, capabilities[])` · `lanes(name, capabilities[])`  ← lane→capability (ADR-0008)
- `leases(project_id PK, host_id, task_id, acquired_at)` — host-üstü; reaper (acquired_at TTL); global-cap atomik
- `tasks(id, project_id, lane, tier, status, requires[], deps[], branch, scenario_id, retry_count)` — status=türetilmiş
- `scenarios(id, project_id, title, lane, tier, deps[], acceptance jsonb, holdout_ref)` — holdout_ref repo-DIŞI store
- `events(id, ts, project_id, task_id, phase, kind, payload jsonb)` + NOTIFY trigger
- `commands(...)` — conductorctl↔daemon control kanalı (ADR-0011)
- Audit/journal DB'de değil → repo `.conductor/journal/<task>.md`; gizli holdout repo-dışı store

## 6. JSON Schema tek-kaynak (1b — ADR-0011, codegen Go+TS)
`Event` · `Verdict{result, branch, commit_sha, checks:[{name,result,evidence}], files, summary}` (jenerik checks — Y5) ·
`ReviewResult` · `Scenario` · `Recipe{develop_cmd, verify_cmd, gates, governance}`.
phase∈{plan,develop,test,review,verify,merge} · kind∈{started,progress,log,diff,decision,health,pr,merge,intervention-needed}

## 7. Sub-agent görev bölümü (orchestrator-only)
- **ÖN-ADIM (tek elden):** paylaşılan kontratlar — `EngineAdapter` interface + `Verdict/ReviewResult/Event` Go struct'ları
  + `StateStore` interface DERLENEBİLİR iskelet olarak dondurulur; sub-agent'lar "imzayı DEĞİŞTİRME, doldur" (review #7).
- **Faz-1a Dalga A (sıralı):** statestore(in-memory) → registry → engine(CommandEngine+performer kontratı+LLM-dayanıklılık).
- **Faz-1a Dalga B:** provisioner(worktree) + verify(+holdout enjeksiyon) + sentinel/reconcile + macOS deploy — paralel.
- **Faz-1a Dalga C:** conductor loop entegrasyon + conductorctl(minimal) → uçtan-uca + negatif test.
- **Faz-1b:** Postgres statestore swap + governor + intake + scaffolder + events + policy + heartbeat.
Her sub-agent'a: ilgili ADR'ler + kontrat + "test yaz" + Rule#9 (bağımsız doğrula).

## 8. Uçtan uca doğrulama (1a — canlıya DOKUNMADAN)
1. Throwaway test-repo (basit, kendi GitHub'ımızda private).
2. ELLE reçete (`.conductor/`) + senaryo + public test + **ayrı gizli-holdout**.
3. Conductor tick → develop (worktree) → verify (gizli-holdout enjekte) → squash-merge.
4. **Negatif (Rule#9):** holdout-kıran task → blocked; bozuk-Verdict → blocked (sahte-yeşil yok).
5. macOS reboot/uyku → daemon + reconcile ayakta.

## 9. Açık / ertelenen
- Çok-host executor (ssh/agent) → **Faz-2**.
- Sentinel Katman-2 (LLM-danışman) → **Faz-1b**.
- Observability UI (VS Code/web) → **Faz-3** (event+control şema hazır).
- Migration aracı: **goose** (basit). Dogfooding: platform Faz-1'de kendini conductor'la DEĞİL, normal Go test+CI ile geliştirir.
