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
- **N-2 (CI GitHub Actions):** 🔄 Agent (worktree). | **N-3 (README):** 🔄 Agent (worktree). Paralel, disjoint dosya.
