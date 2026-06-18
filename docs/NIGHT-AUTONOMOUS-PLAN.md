# Gece-Otonom Çalışma Planı (compact-proof çıpa)

> Kullanıcı uyurken otonom ilerleme. Bu dosya + `docs/decisions/README.md` + memory = compact sonrası tam bağlam.
> Orchestrator (ben) işleri Agent'larla kodlar, gate'ler, merge eder, her 30 dk izler. Durmadan, sabaha kadar.

## MEVCUT DURUM (2026-06-17 gecesi)
- **Faz-1a ÇEKİRDEK TAMAM (9/9) + uçtan-uca doğrulandı.** Repo `everva/conductor-platform`, branch **develop** (push'lu).
- 8 paket: `internal/{statestore,registry,engine,provisioner,verify,reconcile,conductor}` + `cmd/conductorctl`.
- Tüm gate'ler yeşil (go build/test/vet/golangci-lint 0 issues). e2e: `internal/conductor/e2e_test.go` (onboard→develop→verify→merge + negatif/sahte-yeşil engeli).
- **Builder (`tools/builder/`) tick-bug var** (~1dk tick-ölümü) → GÜVENİLMEZ. Bu yüzden gece işleri **Agent'larla** kodlanır (builder by-pass). Builder şu an durdurulmuş (launchd unload + PAUSE).

## MEKANİZMA (her iş için)
1. İş için kısa spec belirle (acceptance + hangi paket/dosya).
2. **Agent'a kodlat** (general-purpose): frozen kontratları oku → TDD → kodla → gate (`go build ./... && go test ./... && go vet ./... && golangci-lint run`) yeşil → develop'a commit `[task:<id>]`. tools/builder'a dokunma.
3. **Rule#9: gate'i BAĞIMSIZ kendin re-run et** (Agent raporuna güvenme). Yeşilse → push develop. Değilse → Agent'a düzelttir / böl.
4. Sıradaki işe geç. Her ~30 dk ilerleme + tıkanıklık kontrol (`/loop 30m`).

## İŞ LİSTESİ (öncelik sırası — production'a doğru)
### P0 — production-kritik altyapı
- **N-1: `cmd/conductor` daemon entry-point** — `internal/conductor.Tick`'i çalıştıran çalışabilir binary YOK; production için şart. Loop/tek-shot tick runner + flag/config.
- **N-2: CI (GitHub Actions)** — `.github/workflows/ci.yml`: build+test+vet+golangci-lint (her PR/push). + golangci config.
- **N-3: README + dağıtım dokümanı** — ne, nasıl kurulur/çalışır, mimari özet (ADR'lere link).

### P1 — Faz-1b çekirdek
- **N-4: Postgres StateStore** — `internal/statestore` Postgres impl (in-memory yanına), goose migration, lease atomik (`FOR UPDATE`/advisory-lock). StateStore arayüzü zaten soyut (ADR-0010/0013).
- **N-5: Resource-governor** — global cap + repo-başına-1 + host yük tavanı (ADR-0008).
- **N-6: Gerçek `claude -p` uçtan-uca** — ürünün CommandEngine'ini gerçek LLM performer + throwaway test-repo ile sür (e2e'de sh-performer kullanılmıştı). Timeout/auth-wall/malformed gerçek davranış.

### P2 — Faz-1b tam
- **N-7: Intake** (konuşma→senaryo+holdout damıtma, ADR-0005/0012).
- **N-8: Scaffolder** (config'siz repo onboard, stack-detect, ADR-0009).
- **N-9: Events** (Postgres LISTEN/NOTIFY + JSON Schema → Go+TS codegen, ADR-0011).
- **N-10: Governance-policy** (tier→merge-mode, T3/T4 human-merge, ADR-0003).
- **N-11: Heartbeat + harici stall-alert** (ADR-0016).

### Opsiyonel / fırsat olursa
- Builder tick-bug kök-çözümü (temiz ortamda); çözülürse kalan işler builder'a devredilebilir (dogfood).

## KURALLAR (production-kararlarını kendim alırım)
- **Her merge gate'li + bağımsız doğrulanmış (Rule#9). Sahte-yeşil ASLA.**
- Canlı optiway'e (davinci `~/optiway-conductor`) ve xirigo'ya **DOKUNMA**.
- **Secret-leak yok** (token/credential commit'leme).
- Yalnız `develop` branch'ine merge + push. PR/protected-branch yok (free-plan).
- Frozen kontratları (engine.go/statestore.go imzaları) bozmadan genişlet.
- Bir iş tıkanırsa: farklı yaklaşım dene VEYA daha küçük parçalara böl (A-3 dersi: tek-Agent çok büyük scope'ta zorlanır).
- Mimari kararlar ADR'lere uygun; yeni karar gerekirse `docs/decisions/`'a ADR ekle.

## İLERLEME KAYDI (her iş bitince güncelle)
- (Faz-1a) PRE-0..C-2: ✅ done (develop). e2e: ✅.
- Gece başlangıç: develop @ 39f37ca, builder durdurulmuş, heartbeat-loop kuruldu (cron 7,37 * * * *).
- **N-1 (cmd/conductor daemon):** ✅ done (5fa2ef3, push'lu). tick-loop + `-once` + graceful-shutdown + 7 hermetik test. Bağımsız gate yeşil (build/test -count=1/vet/lint 0 + e2e compile + -once smoke noop). slog yapılandırılmış log; develop-cmd flag/env (secret yok).
- **N-3 (README):** ✅ done (e2e63f8, push'lu). 189-satır README; mimari/tick-akışı/flag&subcommand/durum — kod ile doğrulandı, Postgres & gerçek claude -p "pending" doğru işaretli. cherry-pick ile merge.
- **N-2 (CI GitHub Actions):** ✅ done (6d9c9eb, push'lu). .github/workflows/ci.yml (build/test-race/vet/e2e-tags/golangci v2.12.0, push+PR, read-only perms, secret yok) + .golangci.yml v2 (default:standard). YAML actionlint+yaml-parse temiz, gate bağımsız 0 issue. cherry-pick ile merge.
- **EK:** gofmt-fix internal/verify + .golangci.yml'a gofmt formatter eklendi (CI artık format de zorluyor) — 43291a2 push'lu. (golangci stale-cache artefaktı temizlendi: nil-ctx zaten //nolint'li, gerçek sorun yok.)
- **🎉 P0 TAMAM (N-1,N-2,N-3).** Sıra: P1.
- **NOT (öğrenildi):** Agent `isolation:"worktree"` SESSION repo'sunu (k8s) izole ediyor, conductor-platform'u DEĞİL. Agent'lar kendi worktree'lerini açtı (cherry-pick ile merge edildi). Bundan sonra worktree-isolation YOK; agent'lar conductor-platform'da direkt, seri çalışır.
- **N-4 (Postgres StateStore):** ✅ done (6e364fc, push'lu). pgxpool + goose migration (00001_init) + PostgresStore (full frozen interface) + 13-case conformance suite (memory+PG ortak). Lease atomik = PK(project_id)+`ON CONFLICT DO NOTHING` (32-goroutine yarış→tam-1-kazanan). **Bağımsız doğrulandı:** kendi docker PG'imde 13/13 PASS (memory+PG, -race), DB-unset gate yeşil+skip, go.mod tidy, gofmt clean. Frozen interface değişmedi.
- **N-5 (Resource-governor):** ✅ done (e7a6d1a, push'lu). internal/governor (Admit/Decision/Reason/LoadProbe); global-cap (ListLeases'ten türetilir, drift yok) + repo-per-1 + host-load (loadavg/numCPU, strict>, okunamazsa permissive). conductor.Tick'e optional-nil Admitter seam (nil=admit-all → mevcut testler değişmeden geçer). daemon'a -global-cap/-load-ceiling flag. **Bağımsız doğrulandı:** 10 paket gate yeşil, e2e -count=1 PASS, governor sınır-testleri + conductor deny/admit testleri PASS, gofmt clean, dep yok, frozen değişmedi.
- **N-6 (gerçek claude -p e2e):** ✅ done (3c00293, push'lu). (A) failure_modes_e2e_test.go: malformed→blocked, auth-wall→stopped(ready, re-auth), timeout→blocked, lie-but-broken→blocked, positive→merged — hepsi no-merge/no-trailer assert'li. (B) realclaude_smoke_test.go (tag+CP_REAL_CLAUDE guard, gate-dışı). **Bağımsız doğrulandı:** offline gate yeşil + e2e 5/5 PASS + realclaude default/e2e'den hariç + **kendi GERÇEK claude -p çalıştırmam** (17s, taze commit 87358ec) parser'ı geçti. GERÇEK BULGU: claude 2.1.181 = prose + son satırda JSON verdict; ParseVerdict son-result-objesi taraması bunu doğru ayıklıyor. Yalnız test dosyaları, frozen değişmedi, secret yok.
- **🎉 P1 TAMAM (N-4,N-5,N-6).** Sıra: P2 (N-7..N-11).
- **N-7 (Intake):** ✅ done (2539bdc, push'lu). internal/intake — rich ADR-0012 şema + Validate/ValidateSet + holdout repo-external kuralı (store://,pg://,private:; mutlak/repo-relative red) + multi-doc YAML file-intake (idempotent) + Distiller seam (CommandDistiller, ParseScenarios last-block-wins; gerçek claude -p gate-dışı). frozen Scenario'ya ToStateScenario/ToStateTask projeksiyon. **Bağımsız doğrulandı:** gate yeşil + ~25 intake testi (validation/dangling-dep-no-write/idempotency/distiller malformed) + e2e PASS + tidy + gofmt clean. TRADE-OFF: conductorctl'in lenient intake'i kasıtlı korundu (testleri bozmamak için); follow-up: fixture'ları zenginleştirip delege et.
- **N-8 (Scaffolder):** ✅ done (39c97c7, push'lu). internal/scaffolder — DetectStack(go>rust>python>node, multistack) + ProfileFor (stack→build/test/vet/lint argv) + AssessReadiness (stack-bazlı test-altyapısı kontrolü, yoksa NOT-READY+ADR-0009 sebep) + GenerateDraft/.conductor/config.yaml (inert develop placeholder, overwrite-refuse). **Bağımsız doğrulandı:** testdata-trap güvenli (go list fixture sızdırmıyor), gate yeşil, ~30 test (detect/profile/readiness/draft), e2e PASS, dep yok, gofmt clean, frozen untouched. onboard'a kasıtlı bağlanmadı (decoupled lib, API kırılmasın).
- **N-9 (Events):** ✅ done (ff45150, push'lu). internal/events — Event envelope + Phase(6: plan/develop/test/review/verify/merge) + Kind(9, intervention-needed dahil) + InterventionNeeded() + EventBus (memory: fan-out/filter/drop-slow; Postgres LISTEN/NOTIFY: pg_notify+persist, per-sub LISTEN conn). conductor.Tick'e optional Emitter (nil=no-op; develop/verify/merge/intervention emit). migration 00002_events. TS codegen cmd/eventgen + types.gen.ts golden-test. **Bağımsız doğrulandı:** offline gate yeşil+PG skip, e2e PASS, **kendi docker PG'imde** LISTEN realtime+persist+fan-out (-race) PASS, frozen untouched, dep yok, gofmt clean. (Control reverse-channel ADR-0011§4 kapsam-dışı bırakıldı — engine.Command frozen tipi zaten var.)
- **N-10 (Governance-policy):** ✅ done (185495f, push'lu). internal/governance — Policy.MergeMode(task): T1/T2→AutoMerge, T3/T4→HumanRequired, unknown/empty→**fail-safe HumanRequired**. conductor.runTask'ta verify-PASS sonrası/merge öncesi: human ise SquashMerge YOK → task blocked + OutcomeHeld/HoldReason + review/intervention-needed event (N-9). nil-policy=auto-all (mevcut testler değişmedi). **Bağımsız doğrulandı:** tier-tablo + held-not-merged + auto-still-merges + nil-auto testleri PASS, e2e PASS, gate yeşil, dep yok, frozen untouched, gofmt clean.
- **N-11 (Heartbeat + harici stall-alert):** ✅ done (b20fa75, push'lu). internal/heartbeat — Record(host/pid/project/tick/last_outcome/updated_at) + atomic Writer (temp+rename) + Checker (inject-clock, FRESH/STALE/MISSING + progress-aware: tick ilerlemiyorsa stuck) + Notifier seam (LogNotifier/Nop) + Detect() pure-core. Daemon her tick + shutdown heartbeat yazar (-heartbeat/env, boş=no-op). Bağımsız `conductor -check -heartbeat <path>` → exit 0/1/2/3 (FRESH/STALE/MISSING/ERROR). **Bağımsız doğrulandı:** heartbeat+checker+progress+exit-code testleri PASS, gate yeşil, e2e PASS, dep yok, frozen untouched.

## 🎉🎉 GECE PLANI TAMAM — N-1..N-11 HEPSI ✅ (her biri bağımsız gate'ten geçti, develop'a push'lu)
**Final aggregate doğrulama (Rule#9):** 16 paket build, 15 test-paketi yeşil, vet 0, golangci 0 issue, gofmt clean, e2e PASS, realclaude gate-dışı, **kendi docker PG'imde statestore+events real-DB suite'leri PASS**, gerçek `claude -p` parser doğrulandı. Working tree clean.

## P3 — PRODUCTION ENTEGRASYON FOLLOW-UP'LARI (agent'ların dürüstçe işaretlediği boşluklar)
Yeni yetenekler (N-4 PG, N-9 events, N-10 governance) kuruldu AMA daemon (cmd/conductor) hepsini henüz KULLANMIYOR (in-memory store, emitter/policy bağlı değil). Üretime tam hazır olması için:
- **P3-1: Daemon Postgres wiring** — ✅ done (50b2270, push'lu). `-dsn`/CONDUCTOR_DSN → PostgresStore + başlangıçta Migrate; boş=in-memory default. newStore helper + Daemon.Close (pgxpool release). Proje-seed CreateProject(ON CONFLICT idempotent) her iki backend'de. DSN/şifre asla loglanmaz/commit'lenmez. **Bağımsız doğrulandı:** offline gate+e2e yeşil, **kendi docker PG'imde** real-DB -once PASS + şema gerçekten migrate oldu (projects/tasks/leases/scenarios/events tabloları), commit'te gerçek DSN yok.
- **P3-2: Daemon observability+governance wiring** — ✅ done (1f40102, push'lu). Deps.Policy←governance.DefaultPolicy() (-governance flag, default true); Deps.Emitter←events.EventBus (memory veya -dsn→PG LISTEN/NOTIFY; Publish doğrudan Emitter seam'i karşılıyor, adapter yok). Bus closer daemon.Close'a zincirlendi (leak yok). DSN loglanmıyor. **Bağımsız doğrulandı:** offline gate+e2e+wiring testleri yeşil, **kendi docker PG'imde** real-DB -once (PG store+PG bus) PASS.
- **P3-4: conductorctl paylaşılan store** — ✅ done (df44906, push'lu). conductorctl global `-dsn`→PostgresStore+Migrate (extractDSN pre-pass: subcommand dispatch'i bozmadan -dsn'i argv'den ayıklar; flag>env>empty). DSN/şifre loglanmaz (pgx redact). **Bağımsız doğrulandı:** offline gate+e2e yeşil + **gerçek binary'lerle kendi docker PG'imde cross-process**: proc1 onboard→proc2(ayrı process) status GÖRDÜ→proc3 daemon -once aynı PG'de tick. Operatör→daemon döngüsü gerçek.
- **P3-5: conductorctl rich-intake delege** (ŞİMDİ 🔄) — conductorctl intake'in lenient parser'ını internal/intake (rich ADR-0012 validate) ile değiştir; fixture'ları geçerli rich senaryolara yükselt; tek intake yolu. (N-7 borcu.)
- **P3-5: conductorctl rich-intake delege** — ✅ done (1214a46, push'lu). 179-satır lenient parser (scenario.go) SİLİNDİ; intake→intake.IntakeFile delege; fixture'lar rich'e yükseltildi (title/acceptance/holdout). net -282/+163, parser kalıntısı yok. **Bağımsız doğrulandı:** gate yeşil, fresh e2e PASS, 7 intake testi (no-partial-write/repo-internal-holdout-red/unknown-project) PASS, frozen+intake-API untouched.
- **P3-3: Control reverse-channel** — ✅ done (166bf40, push'lu). BULGU: pause yalnız conductorctl in-memory'deydi + conductor.Tick pause-check yoktu. **Frozen kısıt:** StateStore'da UpdateProject yok (Project immutable) → pause kapsüllenmiş **marker-task** (`__conductor.paused__:<id>`, ProjectID boş=ledger'da görünmez) ile kalıcı/paylaşımlı (`conductor.StorePauser`). Tick honor (OutcomePaused, nil-Pauser=eski davranış). engine.Command vokabüleri (control.go: ActionPause/Resume/Abort). **Abort ertelendi** (process-group sinyali, follow-up). Karar kaydı: **ADR-0020**. **Bağımsız doğrulandı:** frozen 0 değişiklik, gate+fresh-e2e+pause-testleri yeşil, **gerçek binary'lerle PG'de** pause→daemon outcome=paused→resume→ilerliyor, marker görünmez.

## 🎉🎉🎉 P3 TAMAM (P3-1..P3-5) — DAEMON ÜRETİM-ŞEKLİNDE
Postgres-backed + operatör-CLI paylaşımlı store + events (PG LISTEN/NOTIFY) + governance (tier-gate) + governor + heartbeat + pause/resume kontrol. Her biri bağımsız + gerçek-PG/gerçek-binary doğrulandı. 21 paket, gate yeşil. ADR-0020 eklendi.

## P4 — ÜRETİM SERTLEŞTİRME (autonomous-safe, kullanıcı kararı GEREKMEYEN işler)
- **P4-1: Dockerfile (multi-stage) + docker-compose** — ✅ done (603e131, push'lu). golang:1.26-alpine→alpine:3.21 (git+ca-certs+tini), static CGO-off, non-root uid 65532, ~41.7MB, secret yok. compose: postgres:16-alpine (healthcheck+volume) + conductor (migrate-on-start). **Bağımsız doğrulandı:** kendi docker build'im (aynı sha) + non-root + compose config valid + **kendi compose up'ım**: daemon started, backend=postgres (store+events), tick noop. Go-gate yeşil, .go değişmedi.
- **P4-2: Daemon health/observability HTTP** — ✅ done (340a360, push'lu). cmd/conductor/httpserver.go: -http-addr/env (boş=kapalı), /healthz (her zaman 200, store'a dokunmaz=liveness flap yok), /readyz (ListProjects 2s, 200/503), /status JSON (project/backend/governance/uptime/tick/last_outcome, DSN YOK). loop-mode'da goroutine, ctx-cancel'de graceful drain. Dockerfile EXPOSE 8080 + compose healthcheck /healthz. **Bağımsız doğrulandı:** gate+e2e+http-testleri yeşil, **canlı daemon'a kendi curl'üm**: /healthz 200 ok, /readyz 200 ready, /status temiz JSON (DSN sızıntısı yok). frozen untouched.
- **P4-3: Makefile + DEPLOY dokümanı** — ✅ done (0df9ae7, push'lu). Makefile (help/build/test/e2e/vet/lint/fmt/gate/run/check/docker-build/compose-up-down/db-up-down/itest/clean, ?= override) + docs/DEPLOY.md + .gitignore (/bin, /heartbeat.json). **Bağımsız doğrulandı:** kendi `make gate`'im GREEN, help 9 hedef, .gitignore güvenli, tree clean.
- **P4-4: k8s manifestleri** (ŞİMDİ 🔄) — Deployment (/healthz liveness + /readyz readiness probe, non-root securityContext uid 65532) + Service + ConfigMap (gizli-olmayan env) + Secret (placeholder: DSN/gh-token, gerçek değer YOK) + Postgres (StatefulSet+PVC, dev) + stall-detector CronJob (`conductor -check`, ADR-0016) + kustomization. kubectl/kustomize ile dry-run doğrula. Kullanıcı k8s/ArgoCD ortamında → uygun.

## FOLLOW-UP (kullanıcı kararı / risk → sabah)
- Abort (in-flight iptal): kalıcı aborting sinyali + tick ctx-honor (ADR-0020 follow-up).
- StateStore'a toplamsal UpdateProject (frozen'ın kontrollü gevşetilmesi) → pause'u Project-durumuna taşı (ADR-0020 önerisi).
- Builder tick-bug kök-çözümü (temiz ortam; gece-otonom RİSKLİ → bilinçli ertelendi).
- **Opsiyonel:** builder tick-bug kök-çözümü (temiz ortam) → çözülürse dogfood.
