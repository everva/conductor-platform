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
  testi (her postMessage argümanı toplanır, sentinel hiçbirinde yok — fetch-throw dahil). **4B-2 TAMAM.**
- ⚙️ **CI-stabilize** (`6ac59fd`): web vitest paylaşılan self-hosted runner'da CPU-contention'da flake etti (6 etkileşim
  testi 5000ms default timeout'u aştı; AYNI suite yerelde + önceki 4A-2/4B-1 koşularında geçmişti; 4B-2 editor-only =
  regresyon değil). Düzeltme: `vitest.config.ts` testTimeout/hookTimeout=20000 (yüklü runner'a pay; assertion'lar
  değişmedi — sahte-yeşil değil) + `userEvent.setup({delay:null})` (ağır YAML/conversation fill'lerinde gerçek hız).
- ✅ **4B-3 cockpit panelleri** (`editor/webview/`, ADR-0029): fork webview React bundle = **3B bileşenleri reuse**.
  Paylaşım = **cross-dir source alias** (kullanıcı kararı, ADR-0029): editor webview build'i `web/src/cockpit.ts`'i
  `@cockpit` alias ile KAYNAK import eder; **web/ byte-byte DOKUNULMADI** (git status web/ boş). `webview/main.tsx`
  FORK modu mount: `token=""`, üç `make*Client = () => new ApiClient({transport: http})` (4B-2 köprü HTTP transport'u),
  `eventTransport = events` (4A-2: eventTransport varsa enabled, token'sız çalışır), `onUnauthorized` token-free.
  `tsconfig.webview.json` (DOM+react-jsx, Bundler res + allowImportingTsExtensions, `@cockpit` alias) → webview typecheck
  alias'ı çözüp **FleetDashboard prop'larını köprü transport'larıyla TİP-UYUMLU** doğrular (ADR-0029 tip-kanıtı). esbuild
  2. bundle (browser/iife, React+CSS bundle, jsx automatic). `extension.ts` FleetViewProvider artık bundle'ı yükler:
  `localResourceRoots=dist/webview`, sıkı CSP (`default-src 'none'`, `script-src 'nonce'`, **`connect-src 'none'`** —
  webview KENDİ ağını yapamaz, veri yalnız host köprüsünden → token webview'e GİRMEZ), `asWebviewUri`+nonce'lu
  script/style. Bağımsız doğrulama (Rule#9): yerel gate yeşil (host tsc + **webview tsc alias-çözümlü** + eslint +
  **74 vitest** + esbuild İKİ bundle: host 143kb + webview 351kb = React+3B-cockpit+CSS `web/src`'ten — cross-dir reuse
  build kanıtı) + web/ dokunulmadı + go build geçiyor + webviewHtml CSP testi + main.tsx/CSP incelemesi. CANLI render
  (extension yüklenir→gerçek gateway→canlı paneller) = opt-in/manuel kabul (ADR-0029; electron+gateway gerekir, headless
  CI'da değil). **4B DALGASI (0..3) DETERMİNİSTİK TAMAM.**
- ⚙️ **CI-fix + stabilize (4B-3 sonrası)**: (1) `3babf3f` editor webview React'i editor/node_modules'tan çözer (tsc
  paths→@types + esbuild nodePaths; editor CI job'unda web/node_modules YOK → TS2307/TS2875'ti; CI koşulu yerelde
  web/node_modules gizlenerek birebir tekrar üretilip doğrulandı). (2) `3e0fbb9` Playwright e2e contended-runner sertleştirme
  (workers:1 CI + retries:2 + timeout 60s/expect 15s; paralel browser context'leri yükte birbirini açlığa düşürüyordu;
  intake spec gerçekten 45s sürüyor → eski 30s default'u aşıyordu). CI 3-job TAM YEŞİL.
- ✅ **4C-0 diff-kaynağı KARARI** (ADR-0030): gateway repo görmüyor + `KindDiff` rezerve-ama-emit-edilmiyor + diff
  host-git-local. KARAR: **conductor** (frozen DEĞİL; lifecycle event'leri zaten emit eder) review/merge'de task-branch
  diff'ini hesaplayıp **bounded `KindDiff`** (diff-stat + capped patch + truncated; PG NOTIFY ~8KB) emit eder → mevcut
  bus→gateway→köprü→webview pipe reuse, yeni endpoint YOK, engine.go dokunulmaz. 4C-2/4C-3 bu karardan BAĞIMSIZ.
- ✅ **4C-2 inline kontrol komutları** (`editor/src/controlClient.ts` + extension): `conductor.pause/resume/abort/approve`
  komut paleti — host-tarafı authed control POST (token SecretStorage'dan, YALNIZ Authorization header'ında, hiçbir
  ControlResult/mesaj/log'a girmez — leak-guard test'li). `ControlClient` (listProjects + 4 POST; no-token→not-connected,
  2xx→ok, 401→unauthorized, 409→conflict, diğer→unreachable; projectId encodeURIComponent; approve {task_id} sadece
  verilirse). `runControl`: proje quick-pick → abort/approve modal-confirm → action-specific token-free mesaj. Bağımsız
  doğrulama (Rule#9): yerel gate yeşil (host+webview tsc + eslint + **105 vitest** + iki bundle) + controlClient güvenlik
  incelemesi + web/ dokunulmadı.
- ✅ **Faz-4 ADVERSARIAL REVIEW** (4 mercek + orchestrator, docs/REVIEW-FAZ4-FINDINGS.md): invariant'lar TEMİZ (0 Go-değişikliği
  / web-editör-dokunmaz / events.gen-tek-kaynak / sahte-yeşil-yok / ADR-uyumu / CI-webview-gate). 0 crit/high. Düzeltildi:
  **1 HIGH** hostBridge dup-id WS-handle-sızıntısı (close-on-overwrite + onClose delete-by-identity), **2 MED**
  FleetViewProvider re-resolve-bridge-sızıntısı (predecessor dispose) & webviewTransport unsubscribe-thunk-atılıyordu
  (dispose() + main.tsx seam reuse), **4 LOW** (attach re-entrancy, bayat yorum, makeNonce→node:crypto, eventTransport
  stability dokümanı). 3 regresyon testi. 4 LOW gerekçeyle ERTELENDİ. (`4f9742d`)
- ✅ **4C-3 intervention bildirim + status** (`editor/src/notifier.ts` + extension): host-tarafı bağımsız WS aboneliği
  (`?kind=intervention-needed`, token YALNIZ URL'de — leak-guard'lı), intervention-needed → native `showWarningMessage`
  ("Open Conductor" → `workbench.view.extension.conductor` reveal) + `$(bell)` status-bar sayacı. Notifier yaşam döngüsü
  mevcut `onStateChange`'e bağlı (connected→start, değilse→stop); restart'ta önceki handle kapatılır (review dersi: WS
  sızıntısı yok). Bağımsız doğrulama (Rule#9): yerel gate yeşil (host+webview tsc + eslint + **128 vitest** + iki bundle)
  + notifier token-izolasyonu grep+test.
- ✅ **4C-1a conductor bounded-KindDiff emit** (`15f6ae3` + e2e-tag fix `2d1ccb7`, ADR-0030 — **Faz-4'ün İLK additive Go
  dokunuşu**, kullanıcı onayıyla): yeşil gate'ten SONRA (auto-merge VE held task'lar için, `runTask`'ta policy/merge
  dalından ÖNCE, `PhaseReview`'de) conductor BOUNDED `KindDiff` emit eder. Yeni opsiyonel `Differ` seam (`Emitter` desenini
  birebir aynalar; nil=no-op, geriye-uyumlu) + `GitDiffer` (üç-nokta `base...HEAD`, `git diff --numstat`+`--name-status`
  birleşimi + capped patch; ÜÇLÜ sınır maxFiles=40 / maxPatchBytes=4096 / maxPayloadBytes=7000 toplam-bütçe guard'ı →
  PG NOTIFY ~8KB garantisi) + yeni `events.DiffSummary` tipi (additive; `task` payload'a KONULMADI — envelope zaten taşıyor).
  **FROZEN DOKUNULMADI** (engine.go + statestore + EventBus + Event envelope byte-untouched; `git diff` boş — kanıtlandı).
  Bağımsız doğrulama (Rule#9): `build`/`vet`/`test -race -count=1` (4 tick-level + 4 gerçek-git GitDiffer + payload
  round-trip; bağımsız koşturulup tanık olundu) + golangci 0 issue + **GERÇEK-PG cross-process** (`make db-up`,
  `TestPostgresKindDiffRoundTrip` PASS — KindDiff DiffSummary payload'u gerçek PostgresBus JSON-column + LISTEN/NOTIFY
  hop'undan ayrı bir `Subscribe`'a sağlam ulaştı; gateway→bridge→webview ile AYNI pipe). CI 3-job TAM YEŞİL. **Yolda
  yakalandı:** CI `-tags e2e` adımı `writeFile` test-helper çakışmasını yakaladı (agent + ben `go test ./...` etiketsiz
  koştuğumuz için kaçmıştı) → `writeFileIn`'e yeniden adlandırıldı; ARTIK her iki etiket modu (default + e2e) bağımsız
  doğrulandı (ders: Go işinde Rule#9 = HER iki etiket modu).
- ✅ **4C-1b editör native diff render** (`35f8962`, ADR-0030; **editor/ only — Go/web DOKUNULMADI**): host-tarafı
  `DiffObserver` (`editor/src/diffObserver.ts`, 4C-3 notifier desenini birebir aynalar — `?kind=diff` WS, token YALNIZ
  URL, tek restart-safe handle, defansif parse → token-FREE `TaskDiff`). Render = **salt-okunur virtual doc**
  (`conductor-diff:` scheme + `.diff` URI → otomatik `diff` dili; immutable per-URI → EventEmitter yok): pure
  `renderDiffDocument` (header + per-file stat + unified patch) + bounded `DiffStore` (cap 20, eski-tahliye,
  evict→placeholder). Tetik QUIET (4C-3 bell gibi): ayrı `$(git-compare) Conductor: N diff(s)` status-bar +
  `conductor.showDiff` komutu (>1→quick-pick); toast YOK (gürültü-önleme). Ayrı `DiffVscodeApi` arayüzü (mevcut 26
  call-site kırılmadan; gerçek vscode + headless mock ikisi de sağlar). **Token disiplini**: token yalnız WS URL'inde;
  render/store/status/mesaj token-FREE — diffObserver + extension leak-guard test'li. Bağımsız doğrulama (Rule#9): yerel
  editör gate yeşil (host+webview tsc + eslint --max-warnings 0 + **165 vitest** [diffObserver 17 + extension 64] +
  esbuild iki bundle) + izolasyon kanıtı (sadece editor/ değişti; web/ + Go boş) + kod incelemesi (token-discipline +
  güvenli notifier-mirror). CI 3-job TAM YEŞİL.
  **→ 4C-1 (a+b) TAMAM.**
- ✅ **Faz-4 CANLI DOĞRULAMA** (kullanıcı kararı: 4D'den ÖNCE; `1058a2e`): forked-editör render'ı GERÇEK VS Code'da
  doğrulandı. (1) **Real-electron smoke** (`@vscode/test-electron`, opt-in) GENİŞLETİLDİ + koşuldu: extension GERÇEK VS
  Code host'ta aktive olur, `conductor.connect`+`conductor.showDiff` kayıtlı, ve **native diff render çalışır** —
  `conductor-diff:` URI extension'ın content-provider'ına yönlenir + `.diff` → `diff` dili (unit testlerin yalnız
  mock'layabildiği gerçek `registerTextDocumentContentProvider`/`openTextDocument`/dil-eşleme yolu). PASSED (exit 0).
  Yol-fix: `ELECTRON_RUN_AS_NODE=1` (ortamda set) VS Code binary'sini Node gibi çalıştırıp tüm GUI flag'lerini
  reddediyordu (`bad option: --no-sandbox`, exit 9) → `env -u ELECTRON_RUN_AS_NODE` (README'ye not) + eslint `.vscode-test`
  (258MB indirilen VS Code) ignore (smoke sonrası yerel gate OOM'unu önler). (2) **Gateway backend GERÇEK koşuldu**:
  `/readyz` 200, authed `/status` 200, token'sız `/status` 401, onboard `POST /projects`→201 + `GET /projects` editör Fleet
  panelinin render edeceği veriyi döndürür.
- ✅ **Faz-4 CANLI COCKPIT DOĞRULAMA + 4 GERÇEK BUG** (orchestrator **Playwright ile KENDİ koştu** — kullanıcı kuralı:
  testi BEN yaparım, kullanıcıya ettirmem): forked-editör webview'i gerçek tarayıcı + VS Code'da sürülünce 4 gerçek bug
  çıktı, hepsi düzeltildi + Playwright/gerçek-koşumla doğrulandı. **(1) REST fırtınası** (`2b884ef`): `useFleet` default
  `makeClient` inline-arrow → her render yeni client → poll effect sonsuz döngü → `ERR_INSUFFICIENT_RESOURCES`/flicker;
  fix = module-scope default + regresyon testi. 3B-1'den latent; hızlı yerel gateway açığa çıkardı (uzak-PG maskelemişti).
  **(2) WS 403** (`46fb068`): Vite dev-proxy `/ws` `changeOrigin:true` Host'u :8080 yapıp Origin :5173 bırakıyordu →
  gateway same-origin WS Accept reddediyordu; fix = `/ws` `changeOrigin:false` (DEV-only; prod ingress same-origin).
  **(3) BOŞ fork webview** (`0f44cc7` fix + `53159ec` CI-guard): webview bundle'ı İKİ React kopyası içeriyordu (ADR-0029
  nodePaths yalnız FALLBACK; web/node_modules kuruluyken web/src'in react'i oraya, editör editor/node_modules'a → 2 kopya
  → null hooks dispatcher → blank panel); fix = esbuild **`alias`** react/react-dom tek-kopya; guard = metafile single-React
  testi (editor CI). Gate kaçırmıştı (build başarılı + mount mock'lu); CI no-web/ olduğu için ŞANSLICA dedupe oluyordu →
  yalnız YEREL build bozuktu (extension onu yükler). **(4) Events-tab backfill köprülenmemiş** (`38cb944`):
  `EventStreamView`'in REST `/events` backfill'i fork'ta direct ApiClient'a düşüyordu (CSP `connect-src none` blokluyor) →
  "Could not reach the gateway"; fix = `makeHistory` factory'sini FleetDashboard→EventStreamView'e thread'le (fork bridge
  ApiClient verir). **Doğrulama deseni (DURABLE):** gerçek dist bundle'ı Playwright + stubbed `acquireVsCodeApi`
  (hostFetch Node-side → canlı gateway) ile yükle → #root mount + 3 proje + PAY-1 + Events `/events` köprü-backfill + 0
  konsol hatası (`env -u ELECTRON_RUN_AS_NODE` gerek). **SONUÇ: fork cockpit'in 3 sekmesi de (Fleet/Events/Intake)
  render olup gateway'e KÖPRÜDEN bağlanıyor.** KALAN (daemon/claude gerektirir; render+transport KANITLI): canlı event
  akışı + native diff'in pencerede açılması + intake distill — bir daemon gerçek task işleyince görülür.
- ✅ **4E UÇTAN-UCA (kullanıcı kararı: 4D'den ÖNCE) — editör GERÇEK otonom döngüyü gözlemliyor/kontrol ediyor**
  (orchestrator KENDİ koştu + Rule#9 bağımsız doğruladı; canlı optiway/xirigo'ya DOKUNULMADI — throwaway repo + throwaway PG):
  - ✅ **4E-1 real-claude FULL-LOOP** (`19c528c`, CI 3-job YEŞİL): yeni `internal/conductor/e2e_realclaude_test.go`
    (`//go:build e2e && realclaude` + `CP_REAL_CLAUDE=1` skip-guard; e2e_test.go helper'larını REUSE — newProductRepo/
    gitT/mustCreate*/recordingEmitter — + GitDiffer'lı yerel builder). RULE#9: `CP_REAL_CLAUDE=1 -tags "e2e realclaude"`
    BEN koştum (28s) → GERÇEK `claude -p` greeting.go+test yazdı → bağımsız gate (go build/test) GEÇTİ → squash-merge
    `[task:T-rc-green]` → task done → 4C-1 KindDiff (files=2, patch=518B) emit. Normal gate + CI'nın `-tags e2e`'si
    ETKİLENMEZ (dual-tag dışlar). **Brain artık stub değil — otonom döngü gerçek LLM ile çalışıyor.**
  - ✅ **4E-2 CANLI WIRE** (PRODÜKSİYON topolojisi: daemon + gateway aynı `-dsn` Postgres bus; FS holdout `store://`):
    **G1 native diff** — yeni env-gated `editor/src/diffObserver.live.test.ts` GERÇEK DiffObserver + GERÇEK `ws`
    connector'la canlı gateway'e bağlandı; daemon yeşil-gate KindDiff'i → PG NOTIFY → gateway `/ws` (LIVE) → `onDiff`
    → token-free TaskDiff (greeting.go + gerçek patch; **token sızmaz**) — **PASSED**. **G2 live events** — authed
    `GET /events?kind=diff` backfill gerçek diff'i döndürür (cockpit Events tab kaynağı). **G3 intake distill** —
    `POST /projects/{id}/distill` GERÇEK claude ile konuşmayı önerilen senaryoya çevirdi (A-1/T1, ~8s). **G4
    approve→merge** — T3 task governance ile HELD (`awaiting-approval`) → `POST /approve` (editörün kontrol yolu,
    HTTP 200 `approved_task`) → daemon `outcome=approved-merged` (preserved verified branch'i RE-DEVELOP'SUZ merge) →
    done. **DOĞRULAMA SEVİYESİ (dürüstlük — closing review Lens-3): G1 = COMMITTED env-gated OTOMATİK test
    (`diffObserver.live.test.ts`; diff gelmezse timeout→FAIL, sahte-yeşil yok). G2/G3/G4 = orchestrator tarafından CANLI
    ELDEN doğrulandı (curl `/events` + `/distill` + daemon approve→merge; runbook `editor/README` "Live e2e check (4E)") —
    committed otomatik test/captured-artifact DEĞİL (manuel Rule#9 koşumu).** Editör live test GATE-SAFE (env yoksa skip;
    editor gate typecheck/lint/vitest/esbuild YEŞİL). Native-render-of-real-content zaten katmanlı kanıtlı: unit
    (`renderDiffDocument` patch + content-provider real body) + electron smoke (gerçek VS Code `conductor-diff` doc + `diff`
    dili). Runbook: `editor/README` "Live e2e check (4E)".
  **→ 4E TAMAM. Editör GERÇEK uçtan-uca döngüyü (intake-distill → develop → verify → native-diff → approve → merge)
  gözlemleyip kontrol ediyor; KALAN (önceki kayıttaki "daemon gerçek task işleyince görülür") artık KANITLANDI.**
  **SIRADAKİ: 4D fork-repo paketleme — `everva/conductor-editor` private repo REZERVE edildi (kullanıcı onayı + platform
  kararı alındı: macOS Apple Silicon önce). Code-OSS (MIT) fork + extension built-in bundle + rebrand + CI/release
  (AĞIR ALTYAPI, çok-GB build, dışa-dönük). Dalgalar: 4D-0 fork+yerel-mac-build → 4D-1 göm → 4D-2 rebrand → 4D-3 CI/release.**
- 🟡 **4D BAŞLADI — fork paketleme, VSCodium-tarzı overlay** (otonom oturum; kullanıcı: "4 saat otonom + **reviewli** ilerle"):
  - ✅ **ADR-0031** (`d4cee1c`): `everva/conductor-editor` ince orkestrasyon reposu vscode kaynağını VENDOR'LAMAZ; build-time'da pinned tag clone + küçük patch (product.json rebrand) + built-in extension inject + gulp build. Hard-fork DEĞİL → upstream-merge ucuz.
  - ✅ **4D-0 build spike** (toolchain de-risk): Code-OSS **1.122.1** @ commit `8761a556` + **Node 22.22.1** (nvm). git-lfs eksikti → brew ile kuruldu; broken clone'u skip-smudge+lfs-pull ile temizledim. `build/build.sh compile` (`npm ci`+`npm run compile`) **PASSED** (compile 1.28dk).
  - ✅ **4D-0 scaffold** (`everva/conductor-editor` main `c4fb599`): upstream.json (tag+commit+node+vsce **pin**) + .gitignore (vscode/ ignore) + .nvmrc + build/{get-vscode,prepare,build,inject-extension,all}.sh + .github/workflows/build.yml (manuel macOS-arm64) + README. get-vscode fast-path (marker `.build/upstream-pin` checkout-DIŞI → reset+clean pristine, node_modules korunur) doğrulandı.
  - ✅ **ADVERSARIAL REVIEW** (agent GERÇEK Code-OSS build script'lerini okudu): **C1 CRITICAL blocker** — inject prepare.sh'te build'DEN ÖNCE çalışıyordu → gulp package build `vscode/extensions/`'ı siler/yeniden-işler, doğru post-build app-inject yolu HİÇ çalışmıyordu. **FIX:** inject POST-build (üretilen `.app`'in `Contents/Resources/app/extensions/`'ına; review bu yolu DOĞRU diye doğruladı); prepare=yalnız-patch; all.sh package'te build SONRASI inject. + **H1** inject npm ci+hata-yüzeye-çıkar · **H2/H3** build.sh node-pin assert (sessiz yanlış-node yok) · **H4** CI brew-`||true` kaldırıldı · **M1/M2** marker-dışarı + patch-fail→pin-invalidate · **M4** VSCode-${PLATFORM} · güvenlik commit-SHA+vsce pin. Hepsi düzeltildi (`c4fb599`); bash -n + fast-path + compile doğrulandı.
  - ✅ **4D-2 rebrand** (`2766581`): `patches/0001-rebrand-product.patch` (12 alan: nameShort=Conductor, nameLong=Conductor Editor, applicationName=conductor, data/server/tunnel-folder, darwinBundleIdentifier=ai.everva.conductor-editor, urlProtocol, linuxIconName, reportIssueUrl). prepare.sh temiz+idempotent uygular; get-vscode reset pristine'e döndürür — overlay döngüsü uçtan-uca doğrulandı. (App ikonu .icns = follow-up.)
  - ✅ **4D-1 inject script** + clean package: `editor/.vscodeignore` (`9597ead`) webview/tsx/tsconfig.webview/harness'ı dışlar → **8 dosya, 114KB .vsix** (doğrulandı). `build/inject-extension.sh` post-build app'e enjekte eder.
  - ✅ **CAPSTONE DOĞRULANDI** (fork main `4cf5b3e`): `build/all.sh package` (vscode-darwin-arm64-min) → **`Conductor
    Editor.app`** (1.3G): `CFBundleIdentifier=ai.everva.conductor-editor`, `CFBundleName=Conductor`, executable `Conductor`,
    `bin/code --version`→`1.122.1`/`8761a556`/arm64 (binary GERÇEKTEN koşuyor) + built-in `everva.conductor-editor`
    (`dist/extension.js` mevcut) inject edildi. `build/verify-app.sh` PASS (branding + built-in deterministik gate;
    all.sh package + CI'a bağlı — sahte-yeşil yok).
  - ✅ **RUNTIME DOĞRULANDI** (fork main `9cdf0bb`; `build/verify-runtime.sh` PASS): packaged app'i direct-exec ile boot
    ettim → exthost log'unda `_doActivateExtension everva.conductor-editor (onStartupFinished)` — **built-in conductor
    extension GERÇEK fork editörde AKTİVE oluyor** (vscode.git/github yanında); ayrıca rebrand RUNTIME'da canlı
    (`~/.conductor-editor-shared` kullanılıyor). Reproducible (verify-runtime.sh). NOT: ad-hoc-signed/unsigned olduğu için
    `open` LaunchServices'e takılır → direct-exec veya sağ-tık→Aç gerekir (4D-3 imzalamaya kadar). **→ 4D-0 + 4D-1 + 4D-2
    TAMAM, GERÇEK branded app'te STATİK + RUNTIME uçtan-uca doğrulandı (binary koşuyor + branded + built-in AKTİVE).**
  - ✅ **Faz-4 KAPANIŞ adversarial review** (`docs/REVIEW-FAZ4-CLOSING-FINDINGS.md`, develop `30dc94b`): 3 paralel mercek
    (frozen-kontratlar / token-disiplini+supply-chain / genuineness) — 0 crit/high. Frozen kontratlar INTACT (additive,
    git-doğrulandı); token disiplini SOUND; testler GENUINE (sahte-yeşil yok). Düzeltildi: M1 vsce exact-pin + M2 zorunlu
    commit-pin (fork) + dürüstlük (G2/G3/G4 = manuel runbook doğrulaması, otomatik test DEĞİL — ledger+README'de etiketlendi).
  - ✅ **4D-2b rebrand TAMAMLANDI** (fork main `489c5ca`): CLI adı `bin/code`→`bin/conductor` (`patches/0002-cli-name.patch`,
    gulpfile.vscode.ts 1-satır VSCodium-tarzı build patch) + **Conductor app ikonu** (`resources/conductor.icns`: hub-glyph
    beyaz, indigo→violet gradyan, 16..1024 rsvg+iconutil; `prepare.sh` binary-asset overlay). REBUILD doğrulandı:
    verify-app PASS (icon=`Conductor.icns` byte-eşit + CLI=`bin/conductor`) + `conductor --version`→1.122.1 + verify-runtime
    PASS (built-in aktive). vsce@3.2.0 (M1 pin) kullanıldı. Rebrand artık TAM: ad/nameLong/bundle-id/data-folder/CLI/ikon.
  - ✅ **4D-3 İMZA ALTYAPISI HAZIR + VALIDATED** (2026-06-20): kullanıcı Apple **Developer ID Application** cert'i edindi
    (keychain'de, `codesign` testi PASS — tam zincir + hardened runtime) + **App Store Connect notary API key** (Key
    `JXQ7W4J66T`, Issuer `b526ab07…`; `xcrun notarytool history` Apple'a bağlandı PASS). Tüm imza materyali **Infisical**
    `conductor-editor`/prod'a kondu (8 secret: cert .p12+pw + notary .p8+key-id+issuer + Team ID + identity + cert-SHA) +
    yerel `~/conductor-signing/` (gizli). Detay+plan: memory `conductor-editor-signing`. → **4D-3 ARTIK OTONOM YAPILABİLİR**
    (cert KULLANICI-girdisi engeli kalktı): `build.sh`'a codesign(by-hash `3612565AB7…`, `-o runtime --timestamp`) +
    `notarytool submit --wait` + `stapler staple` ekle → imzalı+notarize `.app`/`.dmg`; fork CI Infisical'dan çeker.
  - ✅ **4D-3 İMZALI + NOTARIZE RELEASE ÜRETİLDİ + DOĞRULANDI** (2026-06-20, otonom): fork `everva/conductor-editor` main
    `e37d7ae`. `build/sign.sh` (inside-out Developer ID codesign + hardened runtime — `--deep` YOK; 37 nested Mach-O + 4
    framework + 4 helper [per-helper entitlements] + ana app; her biri `flags=0x10000(runtime)`, Team `VT3X56P4ZL`) +
    `build/notarize.sh` (ditto zip → `notarytool submit --wait` → `stapler staple` → doğrula). GERÇEK koşuldu: notary
    submission `dd7acfa9…` **Accepted**, `spctl -a -t exec` → **`source=Notarized Developer ID`**, `stapler validate` +
    `codesign --verify --deep --strict` PASS; dağıtılabilir `dist/Conductor-Editor-darwin-arm64.zip` (364M). Entitlements
    VS Code'dan vendor'landı (`build/entitlements/`, MIT). **Fork CI signing:** `.github/workflows/release.yml`
    (workflow_dispatch; build→sign→notarize; tüm Apple secret'ları Infisical'dan runtime'da çeker; tek GitHub secret =
    Infisical machine-identity) + `build/ci-keychain.sh` (ephemeral keychain import — YEREL doğrulandı: identity import +
    throwaway sign PASS). actionlint + shellcheck temiz. İmza materyali repoya GİRMEDİ. **→ FAZ-4 TAMAMEN BİTTİ (4A→4E +
    4D-0/1/2/2b/3). Kalan yalnız opsiyonel kozmetik (imzalı `.dmg`).**
  - ✅ **OVERNIGHT HARDENING — gateway onboard-500 (PG) DÜZELTİLDİ** (develop `ed1ed3d`, CI yeşil): demo'da bulunan bug;
    kök-neden gateway başlangıçta migration KOŞMUYORDU (daemon koşar) → taze/migrate-edilmemiş DB'de her store op 500, ve
    500'ler gerçek hatayı YUTUYORDU (loglanmıyordu). Fix: `newStore` idempotent `pg.Migrate` (daemon'la simetrik) +
    `apiServer.serverError(op,err)` 16 yutulmuş-500 yerine gerçek nedeni server-side loglar (secret-free). Gerçek-PG test
    `onboard_pg_test.go` (newStore-migrates-fresh-schema + onboard 201→idempotent 200; teeth-check: migrate kapalıyken
    SQLSTATE 42P01 ile düşüyor). Rule#9: build/vet/`-race ./...`(gerçek-PG)/e2e-tag/golangci hepsi yeşil.
