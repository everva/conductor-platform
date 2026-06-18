# Faz-2 Adversarial Review — Bulgular (2026-06-18)

4 paralel salt-okunur review-agent (correctness · security · concurrency/multi-host · invariants/test-integrity) +
orchestrator'ın kod-satırı bağımsız doğrulaması (Rule#9). Mimari invariant'lar SAĞLAM; bulgular çok-host
koordinasyonu + güvenlik env-sızıntısı + trust-gap'te.

## ✅ Temiz (gerçek inceleme sonrası — bug yok)
engine.go Faz-2 boyunca byte-frozen; statestore additive-only; **merge yetkisi yalnız deterministik gate** (LLM/sentinel/advisor asla merge edemez); sahte-yeşil/sessiz-skip yok; **holdout izolasyonu (ADR-0018) sağlam** (performer holdout/referansı göremez); path-traversal (3 backing + verify) kapalı; sentinel 3-katman precedence + boundary doğru; **abort⊗sentinel birleşik watcher race-free** (-race temiz, goroutine join'li); AcquireLease atomik (PK+ON CONFLICT, 32-goroutine testi); push-fail sınıflandırması doğru; governor sayımı; pgx pool yaşam döngüsü; reconcile internals; imagediff/scaffolder/recipe. Yeşil testler içi-boş DEĞİL (negatif-assert'li, gerçek-binary/git kanıtlı).

## 🔴 KRİTİK
### C-1 — Uzun develop host-heartbeat'i aç bırakır → canlı host lease'i FALSE-reap → iki host tek repo — ✅ FIXED (R-1, 2026-06-18)
**Düzeltme:** host-heartbeat tick'ten ayrıldı; daemon `Run` (loop) ADANMIŞ arka-plan goroutine'i (`runHostHeartbeat`, cmd/conductor/main.go) sabit cadence (default 30s, `defaultHeartbeatInterval`) ile develop süresinden BAĞIMSIZ `store.HostHeartbeat` vurur, ctx-cancel'da join'li durur. Tick-içi heartbeat kaldırıldı; `-once` tek heartbeat. 30dk develop'ta canlı host ~30s'de bir taze → host-stale 2dk reaper false-reap etmez. Test: cmd/conductor/heartbeat_c1_test.go (enjekte clock + bloklayan develop; 20dk simüle boyunca CANLI; goroutine'siz ölü host hâlâ reap edilir). engine.go untouched; gate+e2e+race yeşil.
İki bağımsız agent + iki repro ile doğrulandı; orchestrator kaynakta teyit etti.
- `cmd/conductor/main.go:1371` — `HostHeartbeat` yalnız `Tick` döndükten SONRA (tick-içi); background goroutine YOK.
- develop `-timeout` 30dk'ya kadar; reconcile CronJob her 1dk; `CONDUCTOR_HOST_STALE`=2dk.
- `reconcile.go:102 isStale` OR-mantığı: `OwnerLive` false → reap, **TTL'den (30dk) bağımsız**.
- → develop > ~2dk olan her görevde canlı host "ölü" sanılır, aktif lease'i reap edilir; başka host aynı repoyu alır → repo-per-1 (ADR-0008) ihlali, çift/çakışan merge.
- **FIX:** host-heartbeat'i tick'ten ayır — bağımsız ticker goroutine (her `interval`) VEYA mevcut watcher'dan vur; ek olarak default `hostStale >> timeout`.

## 🟠 YÜKSEK
### C-2 — ReleaseLease owner-kör → eski sahip yeni sahibin lease'ini siler — ✅ FIXED (R-1, 2026-06-18)
`postgres.go` / `memory.go` `DELETE ... WHERE project_id` (host/task fencing yok). C-1 reap→re-acquire sonrası A bitince B'nin satırını siler.
- **FIX (uygulandı):** ADDITIVE `ReleaseLeaseOwned(ctx, projectID, hostID, taskID)` StateStore'a eklendi (memory + PG: `WHERE project_id AND host_id AND task_id`; owner değilse no-op idempotent). Frozen `ReleaseLease(ctx,projectID)` DEĞİŞMEDİ (reconcile reaper kullanır). Conductor post-tick release defer'i (normal + approve-merge) edindiği lease ile `ReleaseLeaseOwned` çağırır → eski sahip yeni sahibin lease'ini silemez. Picker arayüzü + registry additive güncellendi. Test: conformance `ReleaseLeaseOwnedFencesByOwner` (memory + skip-gated PG) + conductor `release_owned_c2_test.go` (owner-scoped çağrı + mid-tick reap+B re-acquire sonrası B lease'i hayatta). engine.go + EXISTING imzalar untouched (ADR-0021); gate+e2e+race yeşil.

### S-1 — Performer subprocess'i tam (sanitize edilmemiş) env miras alıyor → GH_TOKEN + DSN sızıntısı — ✅ FIXED (R-2, 2026-06-18)
`command_engine.go execRunner`: eski `cmd.Env = append(os.Environ(), env...)`. Faz-2'de `GH_TOKEN` (repo-write) + `CONDUCTOR_DSN` (PG şifre) daemon env'inde → `claude -p --dangerously-skip-permissions` (saldırgan-etkili repo) okuyabilir/exfiltrate edebilir.
- **FIX (uygulandı):** `cmd.Env = append(envsafe.Sanitize(os.Environ()), env...)`. Yeni leaf paket `internal/envsafe` tek paylaşımlı **DENYLIST** sanitizer'ı tutar (import-cycle yok; engine.go FROZEN, additive ADR-0021). Denylist daemon'ın KENDİ sırlarını siler — `GH_TOKEN`/`GITHUB_TOKEN` (exact) + her `CONDUCTOR_*` (prefix, DSN dahil) — ve KEEP eder: PATH/HOME/GO*/`CLAUDE_*` (OAuth token)/locale. Aggressive `*_TOKEN/*_SECRET` strip DEĞİL: o Claude OAuth token'ı öldürür ve performer'ı bozardı. Performer'ın GH_TOKEN/DSN'e ihtiyacı yok (git=credential-helper .git/config'te, provisioner daemon-tarafı; push/merge daemon-tarafı). Recipe/operatör `env` arg'ı sanitize SONRASI eklenir (operatör-authored config, sırla doldurulmamalı). Test: `engine.TestExecRunner_StripsSecretEnvFromPerformer` (gerçek execRunner; `sh -c 'env'` performer'ı GH_TOKEN/DSN içermez, CLAUDE_CODE_OAUTH_TOKEN+PATH içerir) + `envsafe` birim testleri.

### S-2 — verify.sanitizedEnv yalnız CONDUCTOR_* siliyor, GH_TOKEN'ı değil → gate+holdout sızıntısı — ✅ FIXED (R-2, 2026-06-18)
`verify.go` eski `sanitizedEnv` yalnız `CONDUCTOR_*` siliyordu. Her gate (`go test`/`npm test`/…) + holdout, saldırgan-etkili commit'i GH_TOKEN'lı env'de koşuyordu.
- **FIX (uygulandı):** `sanitizedEnv()` artık aynı paylaşımlı `envsafe.Sanitize(os.Environ())` denylist'ini kullanıyor (GH_TOKEN/GITHUB_TOKEN + CONDUCTOR_*). Gate + holdout subprocess'leri daemon sırlarını görmez; PATH/toolchain korunur (DRY, S-1 ile tek kaynak). Test: mevcut `TestRunGate_StripsConductorEnv` genişletildi — parent'taki GH_TOKEN gate'e görünmez, PATH görünür.

## 🟡 ORTA
### S-3 — Saldırgan-kontrollü .conductor/config.yaml → host'ta keyfi argv — ✅ FIXED (R-3, 2026-06-18)
`scaffolder.LoadRecipe`→`resolveRecipe` develop+gate argv'yi repo YAML'ından alıp `exec` ediyor (gate argv'de guard yok; develop'ta yalnız placeholder-check). Operatör `-recipe-dir`'i klonlanan (saldırgan-etkili) ürün repo'suna gösterirse (doğal per-project kullanım) RCE. Shell yok ama argv[0] saldırgan-seçimli.
- **FIX (uygulandı):** trust-boundary `resolveRecipe` doc + DEPLOY.md §7'de açıkça yazıldı (`-recipe-dir` repo'sunun `.conductor/config.yaml` argv'si TRUSTED/host-executed; sandbox'sız saldırgan-etkili repo'ya YÖNLENDİRME). Konservatif tripwire `warnIfShellArgv`: develop/gate argv[0] shell (`sh`/`bash`/`zsh`/…, base-name ile) ise veya argv `-c` içeriyorsa WARN (hard-fail DEĞİL — legit recipe gerekebilir). S-1/S-2 zaten secret blast-radius'u kapatıyor. Test: `cmd/conductor` recipe testleri (shell argv0 + `-c` WARN, tool argv WARN'suz).
### M1 — Approve re-verify "base drift" iddiası semantik drift'i yakalamıyor — ✅ FIXED (R-3, 2026-06-18)
`mergeApproved`/`WorkspaceForBranch` korunmuş branch'i olduğu-tip'te checkout ediyor; ilerlemiş base worktree'ye getirilmiyor → semantik (metinsel-olmayan) drift re-verify'ı geçer (metinsel çakışma merge'de yakalanır).
- **FIX (uygulandı):** re-verify'dan ÖNCE base branch'e merge edilir. `provisioner.MergeBaseIntoWorktree` (`git merge --no-edit --no-ff <base>` worktree'de; çakışırsa `merge --abort` + `ErrBaseMergeConflict`; `IsBaseMergeConflict` sınıflandırıcı). Conductor opsiyonel `BaseMerger` seam'iyle (type-assert; provisioner satisfy eder, fake'ler skip → backward-compatible) çağırır: temiz merge → re-verify DRIFTED base'e karşı (gerçek drift guard); çakışma VEYA re-verify-fail → `OutcomeApprovedRejected` (blocked, approval temizlenir, trailer YOK, fake-green değil; abort sonrası worktree temiz). "develop hiç re-run etmez" korunur. engine.go(interface) + statestore imzaları FROZEN (additive ADR-0021). Test (e2e, gerçek git): `TestE2E_ApproveReVerify_SemanticBaseDrift_BlocksMerge` — held branch `Greeting()` ekler, base TUTULURKEN farklı dosyada ikinci `Greeting()` ekler (metinsel çakışma YOK, ama birleşik ağaç `go build`'i kırar) → approve → base merge edilir → re-verify FAIL → approved-rejected, trailer yok, blocked; + `..._TextualBaseConflict_...` (aynı satır → conflict → abort → rejected).

## 🟢 DÜŞÜK — tümü ✅ FIXED (R-3, 2026-06-18)
- **S-4** ✅ `private:` repo artık https/ssh-only allowlist (`validatePrivateRemote`, Fetch'te); `file://`/mutlak/`..` reddedilir (test-only `allowLocalRemote` in-package read-path için). `intake.validateHoldoutRef` de private repo `file://`/mutlak/`..` reddeder. Test: reddedilen+kabul (holdout + intake).
- **S-5** ✅ `cmd/conductor/main.go` header + `deploy/k8s/secret.yaml` doğru: daemon GH_TOKEN/CONDUCTOR_DSN'i env'den OKUR (provisioner credential-helper + merge-push + DSN) ve performer/gate subprocess env'inden STRIP edilir (R-2).
- **L1** ✅ `engineProgressProbe` `Health.Signal`'ı `aliveFromSignal` ile Alive'a WIRE eder (hardcoded değil). Mapping konservatif (output-türevli liveness süreç-ölümü kanıtlayamaz → "idle" bile alive → gray-zone+backstop'a yönlendirilir, Layer-1 false-kill yok); bu yüzden Layer-1 "clearly dead→Kill" iddiası sentinel doc'unda yumuşatıldı (richer probe gerektirir; backstop default'ta yetkili). Test: `TestEngineProgressProbe_WiresSignalToAlive`.
- **L2** ✅ `capabilitiesSatisfy` `Task.Requires`'ı TrimSpace+boş-drop eder (host-tarafı `WithCapabilities` ile aynı) → `"docker "`/`""` sessiz never-match olmaz. Test: stray-whitespace + blank doğru route.
- **L3** ✅ `cacheKey` full remote'u sha256 hex'le hash'ler (slug prefix + digest); farklı remote → farklı dir. Test: lossy-slug çiftleri ayrışır.
- **L4** ✅ `pgHoldoutID` id'yi TrimSpace+boş-validate eder → boşluk-only id net hata ("no holdout id"), kafa-karıştırıcı "no rows" değil. Test: padded trim, whitespace-only reddedilir.

## Önerilen düzeltme grupları
- **R-1 (multi-host lease güvenliği):** C-1 (background heartbeat) + C-2 (owner-scoped release). KRİTİK+YÜKSEK. — ✅ DONE (2026-06-18).
- **R-2 (secret containment):** S-1 + S-2 (performer + gate env sanitize). YÜKSEK. — ✅ DONE (2026-06-18): paylaşımlı `internal/envsafe` denylist (GH_TOKEN/GITHUB_TOKEN + CONDUCTOR_*) execRunner + verify.sanitizedEnv'de; gate+e2e+race yeşil.
- **R-3 (sertleştirme):** S-3 (trust-doc + argv guard) + M1 (base-drift) + S-4/S-5/L1/L2/L3/L4. — ✅ DONE (2026-06-18): M1 base-merge-before-reverify (`BaseMerger` seam + e2e semantik-drift+conflict testleri); S-3 trust-doc (DEPLOY.md §7) + shell-argv tripwire; S-4 https/ssh allowlist; S-5 doğru secret-hijyen yorumları; L1 Signal→Alive wiring (+doc softening); L2 Requires normalize; L3 sha256 cacheKey; L4 pgstore id trim/validate. Gate+e2e+race yeşil; engine.go+statestore frozen.
