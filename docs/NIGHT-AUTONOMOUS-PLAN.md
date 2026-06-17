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
