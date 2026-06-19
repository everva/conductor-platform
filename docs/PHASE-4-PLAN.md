# Faz-4 Planı — Editör Fork (Code-OSS fork + webview extension) [compact-proof çıpa]

> Faz-1 + Faz-2 + Faz-3 TAMAM (gateway + web cockpit + uçtan-uca; reviewed+hardened, CI org-self-hosted runner'da
> yeşil). Bu plan Faz-4'ü tanımlar. Karar: **ADR-0027** (Code-OSS fork + built-in extension/webview, 3B reuse,
> gateway reuse, token SecretStorage). develop @ (sweep'ten önce) ~18175ee.

## Kullanıcı kararları (2026-06-18)
- **Fork yapısı:** Code-OSS (MIT) fork + agent UI = built-in extension/webview (3B React reuse). Cursor/Windsurf modeli.
- **Repo düzeni:** extension + paylaşılan UI `conductor-platform/editor/`; fork ayrı repo `everva/conductor-editor`
  (Code-OSS + extension bundle + rebrand). Gateway yerinde.
- **Sıra:** ÖNCE minör follow-up sweep → sonra Faz-4 (4A→4E).

## Mekanizma (Faz-1/2/3 disiplini — DEĞİŞMEZ)
Her iş: orchestrator spec → **Agent kodlar** (TDD, gate yeşil, frozen kontratlara ADDITIVE, sahte-yeşil ASLA) →
orchestrator **BAĞIMSIZ gate + gerçek-koşu doğrular (Rule#9)** → squash-merge develop → push. Mimari karar → önce
**ADR**. engine.go + statestore + EventBus imzaları frozen (additive, ADR-0021). Secret-leak yok. Canlı optiway
(`~/optiway-conductor`)/xirigo'ya DOKUNMA. Yalnız subscription `claude -p`. Yalnız `develop`'a merge+push (fork repo
ayrı; oluşunca kendi akışı). Her dalga sonu bağımsız doğrulama + İLERLEME KAYDI.

---

## FAZ-3.5 — Minör follow-up sweep (ÖNCE; hepsi blocker değildi, temiz Faz-4 girişi için)
Sıra (ucuzdan der ine):
- **S-1 distill JSON snake_case:** distill DTO scenario alanları PascalCase çıkıyor (intake.Scenario json-tag'siz);
  gateway DTO'ya snake_case json-tag'li bir scenario-shape ekle (web tipi de güncellenir). Küçük, gateway+web.
- **S-2 CI Playwright hard-gate kararı:** `org-runner-1`'de Playwright zaten geçiyor (libs kurulu) → `continue-on-error`
  kaldır (strict) VEYA best-effort bırak. (Kullanıcı kararı; geçtiği kanıtlı → strict öneri.)
- **S-3 Gerçek-claude advisor smoke:** sentinel gri-bölge advisor runner'ı için canlı claude -p smoke (kapsam boşluğu).
- **S-4 private-repo auth-fail testi:** holdout private: şeması auth-fail yolu için deterministik test.
- **S-5 iOS/maestro CANLI koşum:** `org-macos-1` self-hosted runner HAZIR (review sırasında doğrulandı) → iOS-lane
  reçete+routing'i gerçek Mac-host'ta canlı koş (Dalga-2A'dan beri infra bekliyordu).
- **S-6 sentinel Layer-1 zengin probe:** process-liveness probe'u zenginleştir (şu an signal-wire + backstop;
  "clearly-dead→Kill" prod'da inert).
- **S-7 recipe argv tam sandbox:** S-3 trust-gap (şu an doküman + shell-argv uyarısı + R-2 secret-containment).
Sweep her madde: spec→(gerekirse Agent)→bağımsız gate doğrula→commit→push. Bitince Faz-4'e geç.

---

## DALGA 4A — Köprü hazırlığı (conductor-platform/web; fork'tan ÖNCE, Go-free)
3B bileşenlerini hem web (fetch/WS) hem fork-webview (postMessage) transport'unda çalışır hale getir.
- **4A-0 — KARAR ADR-0027** (✅ yazıldı).
- **4A-1 — Transport seam:** `ApiClient` + `useEventStream` arkasına bir transport arayüzü (REST-call + event-subscribe
  soyutlaması). Web impl = mevcut fetch/WebSocket (davranış değişmez). Fork impl (4B) = postMessage-köprüsü. Bileşenler
  transport'u inject alır. Frontend gate (tsc/eslint/vitest) yeşil; mevcut web testleri değişmeden geçer.
- **4A-2 — Paylaşılabilir UI:** fleet/events/interventions/intake-chat bileşenlerini `editor/` ve `web/`'in ikisinin de
  tüketebileceği forma getir (paylaşılan src veya workspace paketi). events.gen.ts tek-kaynak korunur.

## DALGA 4B — VS Code extension (conductor-platform/`editor/`, built-in)
- **4B-0 — Extension iskele:** TS extension (esbuild/bundle), `editor/extension/`; activity-bar "Conductor" view
  container + komut iskeleti; gate = tsc/eslint/vitest + `@vscode/test-electron` smoke.
- **4B-1 — Bağlantı + auth:** gateway URL config + token **SecretStorage**'da; `/readyz` health; bağlan/çöz akışı.
  Token asla log'a/webview'e girmez.
- **4B-2 — Webview host + postMessage köprü:** extension-host gateway client'ı (REST+WS) → webview'e tipli postMessage
  transport (4A-1 fork impl'i). Webview CSP sıkı; veri yalnız host'tan.
- **4B-3 — Cockpit panelleri:** fleet dashboard + event-akış + müdahale + intake-chat webview'leri = **3B reuse**
  (4A-2 paylaşılan bileşenler). Kabul: extension VS Code'da yüklenir, gerçek gateway'e bağlanır, paneller canlı veri gösterir.

## DALGA 4C — Editör-native deneyim
- **4C-0 — KARAR: diff kaynağı.** Gateway store/bus dışında repo görmüyor; task branch diff'i nereden? Seçenekler:
  (a) KindDiff event'lerini render; (b) daemon-tarafı additive diff endpoint (gateway proxy); (c) editör daemon ile
  aynı host'taysa yerel clone. Karar notu → sonra 4C-1.
- **4C-1 — Native diff:** task branch diff'i VS Code native diff editor'de göster.
- **4C-2 — Inline komutlar:** approve/abort/pause/resume command-palette + context-menu (control API).
- **4C-3 — Bildirim/status:** intervention-needed → native notification + status-bar canlı durum; task'a atla.

## DALGA 4D — Fork + paketleme (`everva/conductor-editor` ayrı repo) — AĞIR ALTYAPI
- **4D-0 — Code-OSS fork + build:** Code-OSS fork, build zinciri (mac/linux/win); reproducible build doğrula.
- **4D-1 — Extension'ı built-in bundle:** conductor extension'ı fork'a built-in göm.
- **4D-2 — Rebrand:** product.json (ad/ikon/ürün), hafif tema; marketplace/telemetri Code-OSS-temiz kalır.
- **4D-3 — CI/release:** fork CI + imzalı/paketli artifact (en az bir platform).

## DALGA 4E — Uçtan-uca + sertleştirme
- **4E-1 — Editör e2e:** forked editör'den gerçek task sür (intake-chat→senaryo→Kontaktör develop/verify→**native diff
  review**→approve→merge), tamamı editör içinde; auth sertleştirme; editör'ün kendi CI gate'i. Kabul: uçtan-uca canlı,
  bağımsız doğrulanır (köprü kanıtı: gateway + 3B bileşenleri reuse edildi).

## Sıra / bağımlılık
Sweep (3.5) → 4A (köprü, web) → 4B (extension) → 4C (native) → 4D (fork, ayrı repo) → 4E. 4A-0/4C-0 kararları kendi
dalga başlarında.

## İLERLEME KAYDI (her iş bitince güncelle)
- Faz-4 planı + ADR-0027 oluşturuldu (2026-06-18). Kullanıcı: Code-OSS fork + webview extension; editor/ + ayrı fork
  repo; önce sweep.
- ✅ **S-1 distill snake_case** (`ab95f4e` + `be91bfe`): gateway `scenarioDTO` (snake_case json) + web `Scenario` tipi +
  vitest mock'ları + Playwright e2e distill mock'u hizalandı; snake_case wire-contract testi. (e2e mock'u ilk commit'te
  atlanmıştı; S-2 strict gate yakaladı.)
- ✅ **S-2 CI Playwright hard-gate** (`6e1547f` + stabilize `be91bfe`): `continue-on-error` kaldırıldı → e2e bloklayıcı;
  flaky'di → `routeWebSocket` temiz WS mock (offline-churn öldürüldü) + Distill click'inde `toPass` retry → yerelde 5/5,
  CI'da yeşil, güvenilir hard-gate.
- ✅ **CI self-hosted sertleştirme** (yolda yakalanan 3 latent bug): e2e committer-identity (`a5ef5ba` provisioner.gitEnv
  pin), golangci-lint-action v6→v7 (`18175ee`, v2 uyumu), setup-go `cache:false` (`25ce0c8`, persistent GOMODCACHE tar
  çakışması). CI org-self-hosted runner'da tam yeşil (gate 1m13s).
- ✅ **S-4 private-repo auth-fail testi** (`16c2dba`): auth-configured private clone-fail → HATA (asla fake-green boş
  holdout) + token redacted; deterministik `.invalid` host (offline).
- ✅ **S-3 advisor canlı smoke + env-fix** (`3d46479`): **bulgu** — sentinel advisor `realClaudeRunner` sanitize'siz
  `os.Environ()` veriyordu (F7/R-2 asimetrisi) → `envsafe.Sanitize` eklendi (GH_TOKEN/CONDUCTOR_* strip, claude-auth
  korunur). + tag-gated real-claude advisor smoke (canlı doğrulandı: `advice=progressing`, mantıklı gerekçe).
- ⏸️ **ERTELENDİ (düşük-değer/derin/infra — gerekçeli):**
  - **S-6** sentinel L1 process-liveness probe: gerçek probe için engine'in performer process'ini session başına
    izleyip Health'e additive `ProcessUp` eklemesi gerekir (derin değişiklik). Değer DÜŞÜK — Layer-3 backstop ölü
    process'i zaten bağlıyor; sentinel docs bu inert-path'i açıkça kabul ediyor. Maliyet >> fayda → ertelendi.
  - **S-7** recipe argv tam sandbox: mevcut durum doküman + shell-argv uyarısı + R-2 secret-containment ile riski
    yönetiyor; tam sandbox (seccomp/namespace) derin + platform-bağımlı. Ertelendi.
  - **S-5** iOS/maestro CANLI: gerçek maestro + iOS-simulator kurulumu `org-macos-1`'de gerekiyor (infra); reçete+
    routing zaten hazır+test'li. Mac-host maestro provision edilince koşulur. Ertelendi (infra-bound).
- **Sweep sonucu:** yüksek-değer/tractable maddeler (S-1/S-2/S-3/S-4) ✅ + yolda 4 latent bug (e2e-identity,
  golangci-v7, setup-go-cache, advisor-env-leak) düzeltildi. CI self-hosted'da tam yeşil.
- ✅ **4A-1 transport seam** (`cdd904a`): `ApiClient` arkasına `HttpTransport`/`FetchTransport`, `useEventStream`
  arkasına `EventTransport`/`WebSocketTransport` (ADDITIVE — mevcut `new ApiClient({token})` + `useEventStream({token})`
  yolları byte-for-byte aynı; mevcut client/useEventStream testleri DEĞİŞMEDEN geçti). **Auth artık transport'un
  sorumluluğu** → injected-transport yolunda ApiClient + hook **token-agnostik** (ADR-0027: fork webview token tutmaz,
  host forward'da auth ekler). Yeni seam testleri: token-agnostik delege + hata haritalama (ApiError/
  DistillNoScenariosError) + event callbacks/close-on-unmount. Bağımsız doğrulama (Rule#9): yerel gate yeşil
  (tsc -b + eslint + 84 vitest) + CI self-hosted'da TAM YEŞİL (Go job 50s + web job 1m18s, **Playwright hard-gate dahil**
  — gerçek-tarayıcı default-transport davranış-eşitliği kanıtı). Yolda 2 gate-hatası yakalandı: exactOptionalPropertyTypes
  (`req.contentType` guard) + kullanılmayan `_url` param.
- ✅ **4A-2 paylaşılabilir UI** (ADR-0028): (a) transport injection bileşen ağacının YUKARISINA taşındı — `eventTransport?:
  EventTransport` `useFleet`/`useEventFeed`/`EventStreamView`/`FleetDashboard`'a threaded (additive, conditional-spread);
  (b) **readiness token'dan ayrıştı** — `enabled` default'u `opts.enabled ?? (token.length>0)`, iç kapı `!enabled`
  (DAVRANIŞ KORUNUR: her mevcut çağrı byte-for-byte; fork `enabled:true`+boş-token ile token'sız mount); (c) açık
  **barrel `web/src/cockpit.ts`** (mount yüzeyi + iki transport seam + domain tipleri; iki mount modu dokümante;
  `events.gen.ts` tek-kaynak), App.tsx barrel'ı tüketir (web = ilk tüketici); (d) ESLint sınır kilidi (paylaşılan UI
  `src/{fleet,events,intake,api}` ⊥ `auth`/`main`; negatif-test edildi). Fiziksel workspace paketi ERTELENDİ (4B,
  `editor/` gelince). Bağımsız doğrulama (Rule#9): yerel gate yeşil (tsc -b + eslint + **86 vitest**, +1 dosya
  `cockpit.test.tsx` = token'sız fork-mount kanıtı: injected REST+EventTransport, gerçek socket YOK), mevcut 84 test
  DEĞİŞMEDEN geçti. **4A DALGASI TAMAM.**
- ✅ **4B-0 extension iskele** (`editor/`): yeni izole araç zinciri — VS Code extension iskeleti. `editor/go.mod`
  carve-out (root `go build ./...` editor/'a inmez, doğrulandı), extension manifest (activity-bar "Conductor" view
  container + `conductor.fleet` webview view + `conductor.connect` komut iskeleti), thin `activate`→testable
  `registerConductor(VscodeApi)`, pure `placeholderHtml` (sıkı CSP `default-src 'none'`, nonce'lu style, script YOK),
  `FleetViewProvider` (enableScripts:false placeholder). Araç zinciri `web/`'i aynalar (strict TS, flat eslint,
  vitest+vscode-mock, esbuild cjs bundle). `@vscode/test-electron` smoke OPT-IN (`CP_VSCODE_SMOKE=1`, CI'da DEĞİL —
  headless runner'da display/xvfb garanti değil; CP_REAL_CLAUDE desenini aynalar). CI: yeni `editor` job (node-only,
  self-hosted, tsc/eslint/vitest/esbuild). Bağımsız doğrulama (Rule#9): yerel gate yeşil (tsc + eslint + **13 vitest**
  + esbuild 3.9kb) + root go-carve-out doğrulandı + CI'da `editor` job TAM YEŞİL.
- ✅ **4B-1 bağlantı + auth** (`editor/`): gateway connect/disconnect/restore akışı + token **SecretStorage**'da.
  `gateway.ts` (vscode-FREE, global fetch): `pingReadyz` (unauth `/readyz` — gateway ayakta mı), `validateToken`
  (authed `/status`, Bearer → 200 valid / 401 unauthorized / diğer unreachable), `normalizeBaseUrl`/`deriveWsUrl`.
  `connection.ts` `ConnectionManager` (injectable `SecretStore`+`GatewayProbe`): connect token'ı YALNIZ (readyz ok +
  valid) ise saklar; restore politikası = geçici kesinti token'ı KORUR, kesin 401 token'ı SİLER; token instance'ta
  cache'lenmez (kaynak = SecretStorage), getter YOK. `extension.ts`: `conductor.gatewayUrl` ayarı + connect/disconnect
  komutları + status-bar state aynası + restore-on-activate. **Token disiplini (HARD): token YALNIZ SecretStorage +
  host fetch Authorization header'ında; mesaj/log/webview'e ASLA girmez** — `connection.test.ts` token-leak guard
  (sentinel token hiçbir state/result/mesajda görünmez) + grep ile doğrulandı. Bağımsız doğrulama (Rule#9): yerel gate
  yeşil (tsc + eslint + **36 vitest**: connection 10 + gateway 9 + extension 17 + esbuild 11kb). Not: 2 modülü Agent
  yazdı (529 overload'da kesildi), kalanı (extension wiring + 3 test dosyası + mock genişletme) orchestrator tamamladı;
  3 strict-gate hatası yakalanıp düzeltildi (kullanılmayan `#token` field, DOM `RequestInfo` tipi, ölü `_gatewayUrl`
  param). **4B-2 TAMAM (aşağıda).**
- ✅ **4B-2 webview host + postMessage köprü** (`editor/src/bridge/`): 4A-1 transport seam'inin FORK IMPL'i.
  `protocol.ts` (tipli `WebviewRequest`/`HostMessage` discriminated union'lar + defansif `isWebviewRequest`/
  `isHostMessage` guard'ları). `hostBridge.ts` `HostBridge` (token-sahibi proxy; injectable `WebviewLike`/
  `TokenProvider`/`WsConnector`/`fetchImpl` seam'leri): webview YALNIZ method+path verir, host `baseUrl+path` birleştirir;
  REST `Authorization: Bearer` + WS `?token=` host-tarafı eklenir. `webviewTransport.ts` (webview tarafı `HttpTransport`+
  `EventTransport`, postMessage üstünden, 4A-1 ile YAPISAL eşleşir, web/ import YOK; monotonik id, pending/subscription
  map). `wsConnector.ts` (ince `ws` adaptörü — VS Code Node host'ta global WebSocket yok). `extension.ts` FleetViewProvider
  artık `enableScripts:true` + sıkı CSP (`script-src 'nonce'`, `connect-src 'none'` — webview KENDİ ağını yapamaz, veri
  yalnız host köprüsünden) + HostBridge attach/dispose. **GÜVENLİK (HARD, test edildi):** (1) **SSRF path-guard** — path
  tek `/` ile başlamazsa (`//evil`, `http://evil`, slash'sız) rest-error + fetch YOK (webview authed isteği başka origin'e
  yönlendiremez); (2) **token izolasyonu** — token YALNIZ host fetch header'ı + WS URL'inde, webview'e gönderilen hiçbir
  mesajda YOK (fetch-throw'da bile generic mesaj, ham hata/URL değil). `ws ^8.21.0` + `@types/ws` eklendi; esbuild ws'i
  bundle eder (opsiyonel native dep'ler `bufferutil`/`utf-8-validate` external). Bağımsız doğrulama (Rule#9): yerel gate
  yeşil (tsc + eslint + **68 vitest**: bridge protocol 6 / hostBridge 15 / webviewTransport 10 + önceki 37 + esbuild
  142kb ws-bundled) + root go-carve-out hâlâ geçiyor + hostBridge güvenlik incelemesi. SSRF-guard testi + token-leak guard
  testi (her postMessage argümanı toplanır, sentinel hiçbirinde yok — fetch-throw dahil). **Sıradaki: 4B-3 (cockpit
  panelleri: webview React bundle = 3B bileşenleri 4A-2 barrel reuse, bridge transport'ları inject; gerçek gateway'e
  bağlanıp canlı veri). NOT: 4B-3 webview build'i web/src/cockpit.ts'i tüketecek → fiziksel paylaşım (ADR-0028 ertelenen
  workspace) kararı burada netleşir.**
