# Faz-3 Adversarial Review — Bulgular ve Düzeltmeler (2026-06-18)

> 4 paralel salt-okunur review-agent (security / correctness / concurrency-resource / invariants-architecture)
> + orchestrator'ın her bulguyu BAĞIMSIZ doğrulaması (Rule#9 — review-agent'a da güvenme). Kapsam: Faz-3 yeni
> yüzeyi (cmd/conductor-api gateway + internal/events EventReader seam + web/ cockpit + .github CI + deploy/k8s + Dockerfile).
> Sonuç: 0 critical, 1 high, 2 medium, 5 low. **Hepsi düzeltildi + bağımsız doğrulandı (gate + real-PG).**

## ✅ Temiz (review'ın doğruladığı invariant'lar)
- **Frozen kontratlar HELD:** `engine.go` byte-identical (`git diff e46e07d..HEAD` boş); `statestore`/`EventBus`/`Event`
  yalnız ADDITIVE büyüdü; `EventReader` ayrı arayüz (EventBus değişmedi).
- **Deterministik-gate-only merge authority HELD:** distill = insan-onaylı taslak (persist yok), kalite kapısı değil;
  merge yetkisi hâlâ daemon'ın deterministik gate'inde. LLM yalnız danışman.
- **ADR-0025 store-yansıma HELD:** gateway daemon'a RPC/ssh/sinyal göndermiyor; yalnız paylaşılan store/bus.
- **Tek doğruluk kaynağı HELD:** gateway state/cache tutmuyor.
- **web/ izolasyon sağlam:** web/go.mod kökten ayırıyor; `go build ./...` web'i görmüyor; CI codegen-sync doğru.
- **Auth temiz:** tüm endpoint'ler gated, constant-time, token leak yok, empty/placeholder/<16 reddediliyor.
- **Injection temiz:** distill claude argv (shell yok), events SQL parameterized, body io.LimitReader(1MB), limit clamp.
- **XSS temiz:** token sessionStorage + asla DOM/log; event payload React text-escape; no dangerouslySetInnerHTML.

## Düzeltilen bulgular

| ID | Sev | Bulgu | Düzeltme | Doğrulama |
|----|-----|-------|----------|-----------|
| **F1** | HIGH | `/events` backfill EN ESKİ N'i döndürüyordu (`ORDER BY ts ASC LIMIT`) ama cockpit history view EN YENİ N bekliyor → Postgres'te kırık (Memory/dev'de maskeli) | `ListEvents` her iki backend'de **most-recent N** (PG `DESC LIMIT`→reverse; Memory `matched[len-limit:]`), yine ascending döner; interface doc + testler güncellendi | reader_test + gateway events_test güncel; **real-PG `limitMostRecent` PASS** |
| **F2** | MED | `approve` bilinmeyen projede 409 ("no task awaiting") dönüyordu (doc'ta 404) — auto-resolve ListTasks boş→409 | `handleApprove`'a `GetProject` varlık-kontrolü → unknown→404 | `TestApproveUnknownProject404` |
| **F3** | MED | Graceful shutdown canlı `/ws` bağlantılarını drain/close etmiyordu (hijacked conn'lar Shutdown'ın dışında; bus.Close trailing-defer) | `apiServer.baseCtx` (sinyal ctx) → `/ws` handler ctx'i baseCtx'e bağlı (`context.AfterFunc`); SIGTERM'de WS temiz kapanır | build + race temiz |
| **F4** | LOW | onboard `repo` validasyonsuz daemon'ın `git clone`'una gidiyordu; `-` ile başlayan repo git'e flag-injection olabilir (provisioner `--` guard'sız) | `handleOnboard`: `-` ile başlayan veya whitespace/control-char içeren repo → 400 (yerel path/github korunur) | `TestOnboardInvalidRepo400` |
| **F5** | LOW | `/ws ?token=` nginx default access-log'da (query string) → token proxy log'una düşer | api-ingress.yaml: log-format'tan query-string strip / `access_log off` notu (prod öncesi) | doküman |
| **F6** | LOW | api pod `readOnlyRootFilesystem:true` + yazılabilir mount yok; distill'in `claude -p`'si HOME/temp ister → prod'da /distill fail | api-deployment: yazılabilir `home`/`tmp` emptyDir mount + `HOME`/`TMPDIR` env (rootfs kilitli kalır) | kustomize build + kubectl dry-run valid |
| **F7** | LOW | distiller `claude -p`'ye sanitize'siz `os.Environ()` veriyordu (gateway env'inde CONDUCTOR_API_TOKEN+DSN) — execRunner ise envsafe kullanıyor (asimetri) | `distiller_real.go`: `cmd.Env = envsafe.Sanitize(os.Environ())` (claude-auth korunur, token/DSN strip) | build + intake test yeşil |
| **F8** | LOW | distill endpoint ADR-0025 yüzey listesinde yoktu (3B-4a'da eklendi, PHASE-3-PLAN'da var ama ADR'de yok) | ADR-0025'e `POST /distill` eklendi: taslak-yardımcısı/persist-yok/422-never-fabricate + F6/F7 notları | doküman |

## Doğrulama (orchestrator, bağımsız)
- Tam gate: `go build ./...` + `go test ./...` (regresyon) + `go vet` + `golangci-lint` (0 issue) + `go test -race` — hepsi yeşil.
- F1: **real-PG** (`TestPostgresListEvents/limitMostRecent` + tüm alt-testler PASS) — bug Postgres'te ortaya çıkıyordu, gerçek PG'de düzeltme kanıtlandı.
- F6: `kustomize build` + `kubectl apply --dry-run=client` valid (env/volumeMounts/volumes + readOnlyRootFS=true).
- Frozen kontratlar hâlâ dokunulmamış (engine.go/statestore imzaları); değişiklikler additive + test-kapsamlı.
