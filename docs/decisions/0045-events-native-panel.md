# ADR-0045 — Events as a native panel (TreeView), not a buried cockpit tab (Faz-Q / Q2)

## Bağlam
Kullanıcı (2026-06-22, canlı fork): **"Event ayrı bir pencere/yüzey olsun"** + genel **"daha
native olsun, web app gibi değil."** Mevcut durumda canlı event akışı, monolitik cockpit'in
**"Events" TAB'ında** (`web/src/events/EventStreamView.tsx`) gömülü — ayrı bir yüzey değil, native
bir konumda (panel-alanı) değil.

**Kritik kısıt (Q0.4 dersi):** offline/live bug TAM da cockpit'in İKİ kez mount edilip İKİ ayrı WS
açmasından kaynaklanıyordu; Q0.4 ikinci mount'u kaldırarak çözdü. `EventStreamView` KENDİ bağlantı
göstergesini (`feed.state` "open/connecting/closed") çizer → onu yeni bir İKİNCİ webview-bridge'te
reuse etmek, çözdüğüm yüzeyler-arası bağlantı tutarsızlığını GERİ getirirdi.

## Karar
**Events'i native bir TreeView olarak panel-alanına (Output/Problems/Terminal komşusu) taşı** —
webview DEĞİL, ikinci bridge YOK. Host-tarafı bir `EventsWatcher` veri katmanı:
- **`EventsWatcher`** (`editor/src/eventsWatcher.ts`, vscode-free): notifier/DiffObserver SEAM'lerini
  (WsConnector/WsHandle/TokenProvider) aynalar. `start()`'ta (1) REST **backfill** (`GET {rest}/events?
  limit=N`, panel boş açılmaz) + (2) canlı **WS** (`{ws}/ws`); sınırlı, newest-first ring; her ring
  değişiminde `onChange`. Tek aktif handle (restart predecessor'ı kapatır). **Token YALNIZ** WS URL
  `?token=` + backfill `Authorization` header'ında — FeedEvent'e/log'a/onChange'e ASLA (leak-guard test).
- **`EventsTreeProvider`** (`editor/src/eventsTree.ts`): düz liste (root→events newest-first); satır =
  kind-codicon + "phase / kind" label + "project·task · time" detay. `eventKindPresentation` pure
  (events.gen.ts kind sözlüğüne dayalı), `eventShortTime` pure.
- **package.json:** `viewsContainers.panel` ("conductorEvents") + view `conductor.events` (tree) +
  empty-welcome. activate: watcher+tree kur; connect'te start/stop (notifier/diffObserver ile aynı
  blok); `onChange`→`eventsTree.refresh()`; `createTreeView`; subscriptions'a ekle.

## Sonuçlar
- **Editör-only** (`eventsWatcher.ts`+`eventsTree.ts`+package.json+activate+testler). web/src + Go
  DOKUNULMADI (ADR-0021). **Token DONMUŞ** (leak-guard'lı). İkinci webview-bridge YOK → Q0.4'ün
  tek-bağlantı kazanımı korunur; bağlantı göstergesi native status-bar'da TEK ($(plug)), events paneli
  bağlantı durumu çizmez.
- **Doğrulama:** editör gate (typecheck×2 + eslint-0 + **vitest 248/3**: eventsWatcher [parse/ring/cap/
  dedupe/**leak-guard** sentinel yalnız URL+header/restart] + eventsTree [kind-presentation/shortTime/
  getChildren/getTreeItem] + activate 24 subscription + 2. createTreeView + esbuild) + **electron smoke
  YEŞİL** (1.125.1, panel view contribution activation'ı bozmaz — P3-gotcha sınıfı; exit 0).
- **KAPSAM (dürüst):** Q2 fleet-genelinde (TÜM events) gösterir; **seçime-göre kapsamlama (proje
  filtresi) ERTELENDİ** (host seçim-state'i tutmuyor; plan §2.2'nin "seçime göre"si bir follow-up).
  Satır-tık→session deep-link de follow-up (şimdilik gözlemsel). Çekirdek ask (user #2 "ayrı event
  penceresi" + #4 native) karşılandı.
- Görsel capstone (panel'in gerçek fork'ta canlı event'lerle dolması) = Q5/kullanıcı host (smoke'ta
  gateway yok → backfill boş, ama wiring throw-suz). ADR-0044/0021 korunur.
