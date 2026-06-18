# Faz-2 Adversarial Review — Bulgular (2026-06-18)

4 paralel salt-okunur review-agent (correctness · security · concurrency/multi-host · invariants/test-integrity) +
orchestrator'ın kod-satırı bağımsız doğrulaması (Rule#9). Mimari invariant'lar SAĞLAM; bulgular çok-host
koordinasyonu + güvenlik env-sızıntısı + trust-gap'te.

## ✅ Temiz (gerçek inceleme sonrası — bug yok)
engine.go Faz-2 boyunca byte-frozen; statestore additive-only; **merge yetkisi yalnız deterministik gate** (LLM/sentinel/advisor asla merge edemez); sahte-yeşil/sessiz-skip yok; **holdout izolasyonu (ADR-0018) sağlam** (performer holdout/referansı göremez); path-traversal (3 backing + verify) kapalı; sentinel 3-katman precedence + boundary doğru; **abort⊗sentinel birleşik watcher race-free** (-race temiz, goroutine join'li); AcquireLease atomik (PK+ON CONFLICT, 32-goroutine testi); push-fail sınıflandırması doğru; governor sayımı; pgx pool yaşam döngüsü; reconcile internals; imagediff/scaffolder/recipe. Yeşil testler içi-boş DEĞİL (negatif-assert'li, gerçek-binary/git kanıtlı).

## 🔴 KRİTİK
### C-1 — Uzun develop host-heartbeat'i aç bırakır → canlı host lease'i FALSE-reap → iki host tek repo
İki bağımsız agent + iki repro ile doğrulandı; orchestrator kaynakta teyit etti.
- `cmd/conductor/main.go:1371` — `HostHeartbeat` yalnız `Tick` döndükten SONRA (tick-içi); background goroutine YOK.
- develop `-timeout` 30dk'ya kadar; reconcile CronJob her 1dk; `CONDUCTOR_HOST_STALE`=2dk.
- `reconcile.go:102 isStale` OR-mantığı: `OwnerLive` false → reap, **TTL'den (30dk) bağımsız**.
- → develop > ~2dk olan her görevde canlı host "ölü" sanılır, aktif lease'i reap edilir; başka host aynı repoyu alır → repo-per-1 (ADR-0008) ihlali, çift/çakışan merge.
- **FIX:** host-heartbeat'i tick'ten ayır — bağımsız ticker goroutine (her `interval`) VEYA mevcut watcher'dan vur; ek olarak default `hostStale >> timeout`.

## 🟠 YÜKSEK
### C-2 — ReleaseLease owner-kör → eski sahip yeni sahibin lease'ini siler
`postgres.go:320` / `memory.go:207` `DELETE ... WHERE project_id` (host/task fencing yok). C-1 reap→re-acquire sonrası A bitince B'nin satırını siler.
- **FIX:** owner-scoped release — additive `ReleaseLeaseOwned(ctx, projectID, hostID, taskID)` (frozen `ReleaseLease(ctx,projectID)` imzası korunur; conductor owner-scoped olanı çağırır).

### S-1 — Performer subprocess'i tam (sanitize edilmemiş) env miras alıyor → GH_TOKEN + DSN sızıntısı
`command_engine.go execRunner`: `cmd.Env = append(os.Environ(), env...)`. Faz-2'de `GH_TOKEN` (repo-write) + `CONDUCTOR_DSN` (PG şifre) daemon env'inde → `claude -p --dangerously-skip-permissions` (saldırgan-etkili repo) okuyabilir/exfiltrate edebilir.
- **FIX:** execRunner env'ini sanitize et (GH_TOKEN/CONDUCTOR_*/secret-shaped strip; tercihen toolchain allowlist). Performer'ın token'a ihtiyacı yok (git=helper, merge/push=daemon-tarafı).

### S-2 — verify.sanitizedEnv yalnız CONDUCTOR_* siliyor, GH_TOKEN'ı değil → gate+holdout sızıntısı
`verify.go:265`. Her gate (`go test`/`npm test`/…) + holdout, saldırgan-etkili commit'i GH_TOKEN'lı env'de koşar.
- **FIX:** sanitizedEnv GH_TOKEN'ı (+ bilinen secret anahtarları) da strip etsin; tercihen allowlist.

## 🟡 ORTA
### S-3 — Saldırgan-kontrollü .conductor/config.yaml → host'ta keyfi argv
`scaffolder.LoadRecipe`→`resolveRecipe` develop+gate argv'yi repo YAML'ından alıp `exec` ediyor (gate argv'de guard yok; develop'ta yalnız placeholder-check). Operatör `-recipe-dir`'i klonlanan (saldırgan-etkili) ürün repo'suna gösterirse (doğal per-project kullanım) RCE. Shell yok ama argv[0] saldırgan-seçimli.
- **FIX:** trust-boundary'yi açık dokümante et + (opsiyonel) recipe argv allowlist / sandbox; S-1/S-2 fix'i blast-radius'u (secret) daraltır.
### M1 — Approve re-verify "base drift" iddiası semantik drift'i yakalamıyor
`mergeApproved`/`WorkspaceForBranch` korunmuş branch'i olduğu-tip'te checkout ediyor; ilerlemiş base worktree'ye getirilmiyor → semantik (metinsel-olmayan) drift re-verify'ı geçer (metinsel çakışma merge'de yakalanır).
- **FIX:** re-attach'tan sonra base'i branch'e merge/rebase edip re-verify; VEYA yorumları "yalnız korunmuş-tip'te gate, çakışma merge'de" diye yumuşat.

## 🟢 DÜŞÜK
- **S-4** `private:` `file://`/mutlak/relative kabul ediyor (sınırlı SSRF; ref operatör-authored) → https-allowlist.
- **S-5** eskimiş "secret yok / GH_TOKEN okunmaz" yorumları (main.go:14-17, secret.yaml:32) artık yanlış → güncelle.
- **L1** prod progress-probe `Alive:true` hardcoded → sentinel Layer-1 "clearly dead→Kill" prod'da inert (backstop yine bağlar). Signal=="idle" eşle veya iddiayı kaldır.
- **L2** capability routing `Task.Requires`'ı normalize etmiyor (whitespace/boş → sessiz skip) → host-tarafı gibi trim/dedup.
- **L3/L4** privatestore cacheKey çakışması; pgstore id TrimSpace yok (loud-fail, hardening).

## Önerilen düzeltme grupları
- **R-1 (multi-host lease güvenliği):** C-1 (background heartbeat) + C-2 (owner-scoped release). KRİTİK+YÜKSEK.
- **R-2 (secret containment):** S-1 + S-2 (performer + gate env sanitize, allowlist). YÜKSEK.
- **R-3 (sertleştirme):** S-3 (trust-doc + argv guard) + M1 (base-drift) + S-4/S-5/L1/L2/L3/L4.
