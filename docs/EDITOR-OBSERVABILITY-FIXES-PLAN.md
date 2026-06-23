# Editör gözlemlenebilirlik + e2e disiplini — plan (kullanıcı 2026-06-23)

Kullanıcı 3 madde + 1 disiplin koşulu koydu. Bu plan onları aynı standart/disiplinde (Faz-G/L/
provisioner-fix ile AYNI) çözer. **İMPLEMENT COMPACT SONRASI.**

> ⚠️ DİSİPLİN (DEĞİŞMEZ): UYDURMA YOK · FAKE-GREEN YOK · frozen-additive (ADR-0021) · HER task
> Go gate (build+vet+golangci-0+`-race`) + GERÇEK-PG (gerekirse) + editör gate (tsc×2+eslint-0+
> vitest+esbuild) + GERÇEK fork electron smoke + **PLAYWRIGHT e2e (web cockpit)** + CI 3-job yeşil
> (`gh run view`) + ledger/memory. **TOKEN DONMUŞ.** Canlı optiway `main`'e DOKUNMA. worktree YOK.
> **Self-verify: KENDİM Playwright/curl ile doğrula, kullanıcıya manuel test ettirme.**

## §0 — GERÇEK DURUM (compact anı)
- develop @ `37d8230`. Bu oturumun zinciri (hepsi CI-yeşil; son 1-2 commit CI doğrulanacak):
  L1 `de3b5c4` · L2 `da0a8ea` · docs `d4e5b05` · L3a `8f4de59` · L3b `9588a3e` · L3c `68a32c0` ·
  L3d `1509e17` · L3e `57f9e44` · diagnostics `0fd6ac8` · branch-D/F-fix `6b5d8d7` · pulse
  `d49fdd5` · Now/Activity `327f990` · scenario-brief `56e2168` · pulse-phase-fix `37d8230`.
- **PROD CANLI:** gateway k8s'te (`conductor-api`, L3 credential store açık, `CONDUCTOR_CREDENTIAL_KEY`
  k8s secret'te). davinci performer **systemd'de aktif** (claude+node+pnpm kurulu; binary `56e2168`
  — brief var ama pulse-phase fix'i YOK). Editör app (`conductor-editor` fork) Now/Activity +
  Diagnostics inject'li (ama `37d8230` step-granularity editör fix'i HENÜZ inject DEĞİL).
- **VARDIYE-3 GERÇEKTEN ÇALIŞIYOR:** claude `apps/web/src/components/shifts/shift-assignment-dialog.tsx`
  + 5 i18n dosyasını düzenledi (mesafe-aralığı filtresi). Bitince held-for-review → kullanıcı onayı.
  **VARDIYE-3 bitene kadar davinci agent'ı RESTART ETME** (develop'u keser).
- **BEKLEYEN DEPLOY (VARDIYE-3 terminal olunca):** agent binary `37d8230` cross-compile→davinci
  (stop→scp→start) + editör re-inject (`build/inject-extension.sh` + `codesign --force --deep --sign -`).

## §0.1 — DOĞRULANACAK (impl ilk adımı; uydurma yok)
- web cockpit Playwright e2e harness'i NASIL ayağa kalkıyor: `web/playwright.config.ts` `webServer`
  → conductor-api'yi hangi STORE ile başlatıyor (memory mi, throwaway PG mi) + seed verisi nereden
  (fixture/intake). `web/e2e/*.spec.ts` (board/dashboard/intake/palette/session/smoke) mevcut.
  **Cevap (madde 3): konser/editör e2e'si EFEMER bir test-gateway'e karşı koşar (PROD DEĞİL) —
  bunu §0.1'de teyit edip belgele; gerekiyorsa deterministik seed fixture'ı ekle/sağlamlaştır.**

## §1 — MADDE 1: status tutarsızlığı (tree=todo vs board=running)
**KÖK NEDEN (kanıtlı):** board `columnFor` AKTİF LEASE'i canlı-running sinyali olarak kullanır
(`web/src/fleet/board.ts`: lease varsa running) → VARDIYE-2 lease'li → board "running". sessions-tree
ise `task.Status`'u gösterir; **gateway `handleAgentLease`'te task.Status'u todo→running FLİP ETMİYOR**
→ `/tasks` "todo" → tree "todo". (VARDIYE-1 blocked vs needs-review = gerçek mismatch DEĞİL; board
blocked'ı "needs-review" aksiyon-kolonunda gruplar.)
- **FIX (gateway, additive):** `handleAgentLease` lease başarılı olunca leased task.Status="running"
  (+ HostID) yazsın (`updateTask`). Held/blocked/done G2'de zaten güncelleniyor. Lease serbest
  kalınca (release) running→todo geri (re-runnable) — dikkat: blocked/awaiting'i EZME (yalnız hâlâ
  running ise geri al). Go gate + GERÇEK-PG + `agent_test.go` (lease→status running; release→todo).
- **DOĞRULAMA (Playwright):** seeded gateway'de bir task'a lease ver → board RUNNING + tree running
  AYNI → `web/e2e` (veya editör sessionsTree vitest) tutarlılık testi.

## §2 — MADDE 2: gateway kopunca otomatik yeniden bağlanma
**SORUN:** bağlantı/WS kopunca editör otomatik reconnect ETMİYOR (yalnız manuel Reload). Diagnostics
"Connection: disconnected" ama /healthz ok (kanıt: ekran görüntüsü).
- **FIX (editör, additive):** dayanıklı reconnect:
  - `ConnectionManager`: periyodik `restore()` (backoff) — "unreachable"da token'ı TUTUYOR zaten;
    bir reconnect-loop/health-poll ekle (ör. 10-15sn'de bir, connected değilken) → tekrar bağlanınca
    state "connected" + EventsWatcher.start() + sessionsTree/diagnostics/activity refresh.
  - `EventsWatcher` / `wsConnector`: WS düşünce otomatik re-subscribe (backoff). Şu an start/stop
    bağlantı-state'ine bağlı; "drop → otomatik yeniden start" ekle.
  - HostBridge (webview): WS düşünce reconnect (cockpit canlı kalsın).
  - Reconnect'te status snapshot'ları tazele (madde 1 mismatch'i de giderir).
- **DOĞRULAMA:** vitest (reconnect-loop + backoff, fake clock/connector) + GERÇEK fork electron
  smoke + **Playwright** (cockpit WS-drop→auto-recover senaryosu, mümkünse).

## §3 — MADDE 3: e2e test altyapısı (KATI KURAL — DB/veri)
Kullanıcı: "yapılan işler kesinlikle Playwright ile kontrol edilmeli; hangi DB hangi veri?
bu altyapıyı kurmadan ilerleme."
- **MEVCUT:** `web/e2e/*.spec.ts` (6 spec) + `web/playwright.config.ts` ZATEN var; cockpit'i seeded
  bir gateway'e karşı test ediyor. Bunu §0.1'de teyit et: hangi store + seed.
- **KUR/SAĞLAMLAŞTIR:**
  1. **EFEMER test-gateway** (PROD DEĞİL): conductor-api'yi `CONDUCTOR_DSN`'siz (MEMORY store) VEYA
     throwaway PG (docker `conductor-pg :5433` / auto-drop şema) ile başlat; `CONDUCTOR_API_TOKEN`=
     test-token. **PROD `conductor/optiway` ya da canlı PG'ye ASLA dokunma.**
  2. **Deterministik seed:** intake ile bilinen projeler/scenariolar/tasklar/host/lease yükle (bir
     `seed` fixture/script). Madde 1 (lease→running) + madde 2 (reconnect) + needs-review/board için
     yeterli durum çeşitliliği.
  3. **Playwright e2e** (KENDİM koşar, kullanıcıya değil): board status tutarlılığı, needs-review,
     reconnect-recover, Now/Activity (webview kısmı). Editör-NATIVE yüzeyler (Activity tree, Now
     status-bar, sessions-tree) Playwright'a görünmez → onlar vitest + electron smoke ile; **board/
     cockpit (webview) Playwright ile.**
  4. **Agent/pipeline e2e:** Go tarafında `multihost_e2e_test.go` / `twohost_test.go` deseni
     (GERÇEK-PG, TEST_DATABASE_URL) lease→develop(fake performer)→verify→merge zincirini kapsar;
     madde 1'in lease→running'ini buraya da ekle. **Gerçek claude e2e** opt-in (`CP_REAL_CLAUDE`).
- **Dürüst sınır:** canlı davinci+gerçek-claude uçtan uca, deterministik CI e2e DEĞİL (claude
  non-deterministik + ağ); o "canlı doğrulama" (VARDIYE-3 gibi). CI e2e = fake-performer + seeded
  gateway. İkisi de gerekli; karıştırma.

## §4 — SIRA + her faz CI-yeşil
1. **§0.1 grounding** (playwright.config + board.ts + handleAgentLease oku).
2. **M1 status flip** (gateway lease→running + release→todo; agent_test + GERÇEK-PG) → commit → CI.
3. **M3 e2e harness** (efemer test-gateway + seed + ilk Playwright status-tutarlılık testi —
   KENDİM koştur) → commit → CI. (M1'i bu harness'la da doğrula.)
4. **M2 auto-reconnect** (ConnectionManager loop + EventsWatcher/bridge re-subscribe + vitest +
   electron smoke + Playwright recover) → commit → CI.
5. **Bekleyen deploy** (VARDIYE-3 terminal sonrası): agent `37d8230`+ → davinci, editör re-inject.
6. ADR + memory güncelle.

## DURUM
- Plan yazıldı. **VARDIYE-3 canlı geliştiriyor (kesme).** Compact sonrası §0.1 → M1 → M3 → M2.
- 3 madde: status-flip (gateway), auto-reconnect (editör), e2e-harness (efemer test-gateway+seed+
  Playwright, KENDİM). Hepsi additive + CI-yeşil + Playwright-doğrulamalı.
