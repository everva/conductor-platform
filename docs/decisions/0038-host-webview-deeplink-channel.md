# ADR-0038 — Host→webview deep-link control channel (session = workspace, N3a)

## Bağlam
N2 (ADR-0037) sessions tree click'i Command Center'ı reveal ediyordu. Kullanıcı kararı (2026-06-22):
N3 "session = workspace" için **birleşik deep-link** — tree'de bir task'a tıkla → Command Center'ın
cockpit'i o session'ın SessionView'ine GEÇSİN (Devin: tek cockpit, gezinir). Bu, iki yeni şey gerektirir:
(1) paylaşılan cockpit'in DIŞARIDAN bir session'a yönlendirilebilmesi; (2) host'tan webview'e bir kontrol
mesajı. Mevcut bridge (4B-2) yalnız REST/WS'i **correlation id**'yle taşır — deep-link ise host-başlatımlı,
korelasyonsuz bir kontrol sinyali.

Kısıt: token disiplini DONMUŞ (mesajda token YOK). ADR-0029 (paylaşılan cockpit reuse) → web/src'e dokunuş
minimal + her iki tüketici (web App + fork webview) için non-breaking olmalı.

## Seçenekler
- **(a) AYRI kontrol kanalı.** `navigate-session` (host→webview) + `webview-ready` (webview→host),
  bridge'in `HostMessage`/`WebviewRequest` union'larından AYRI; **correlation id taşımaz** → bridge router'ları
  (her ikisi de string `id` ister) onları reddeder → REST/event map'leri dokunulmaz. Cockpit'e opsiyonel
  `navigateTo` prop'u + effect. [seçildi]
- **(b) HostMessage'i navigate ile genişlet.** Korelasyon-tabanlı router'ı bulandırır (id'siz bir HostMessage
  özel-durum gerektirir). Reddedildi.
- **(c) Session başına ayrı panel.** Kullanıcı ADR-0037'nin N3 sorusunda "birleşik deep-link"i seçti. Reddedildi.

## Karar
**(a).** N3a şunu sevk eder:
- **Protokol** (`bridge/protocol.ts`): `HostControlMessage = {kind:"navigate-session",project,task}` +
  `WebviewControlMessage = {kind:"webview-ready"}` + `isHostNavigate`/`isWebviewReady` guard'ları. **id YOK →
  bridge router'ları reddeder** (test'le kilitli: çapraz-kontaminasyon yok). Token YOK (yalnız proje/task id).
- **Paylaşılan cockpit** (`FleetDashboard`): opsiyonel `navigateTo?: {project,task}` prop'u + `useRef`-korumalı
  effect — canlı fleet'e (`tasksByProject`) karşı çözer ve o task'ın `SessionView`'ini açar (board drill-in'in
  AYNI `selectedTask` seam'i). FORK-only (web App geçmez → web modu byte-aynı). Bir kez tüketilir (geri çıkınca
  yeniden açmaz); task henüz yüklenmediyse sonraki fleet güncellemesinde çözer.
- **Webview** (`main.tsx` → `ForkApp`): `navigate-session`'a abone olur → `navigateTo` state'i sürer; mount'ta
  `webview-ready` postlar. Her mesajda YENİ obje → aynı id'lere tekrar deep-link yine fire eder.
- **Host** (`CommandCenterPanel.navigateToSession`): paneli açar/reveal eder, sonra `navigate-session` postlar;
  webview daha `webview-ready` dememişse (cold-start) BUFFER'lar + ready'de flush eder (soğuk açılıştan hemen
  sonraki ilk tıklama kaybolmaz). `conductor.openSession` komutu (args `[projectId, taskId]`) tree task-click'e
  bağlı.
- **Fork scenario client** (`forkClientFactories` + `makeScenarioClient`): deep-link'lenen SessionView'in spec
  fetch'i (`/projects/{id}/scenarios`) bridge üzerinden gider (CSP `connect-src 'none'` doğrudan fetch'i bloklar).

## Sonuçlar
- **N3a (bu commit):** `editor/` (protocol + main.tsx ForkApp + connect makeScenarioClient + CommandCenterPanel
  navigateToSession + openSession komutu + sessionsTree task deep-link + mock `__fireMessage` + testler +
  smoke) + `web/` (FleetDashboard navigateTo + cockpit nav testi). Backend/Go DOKUNULMADI (ADR-0021). Token DONMUŞ.
- **Doğrulama (Rule#9 iki-gate + gerçek runtime):** editör gate (typecheck×2 + eslint-0 + **vitest 201/3** +
  esbuild) + web gate (typecheck + eslint + **vitest 87** + **Playwright e2e 14/14**; `cockpit.test.tsx` nav
  testi deep-link'in SessionView'i açtığını kanıtlar) + **electron smoke YEŞİL** (openSession register+execute
  throw-suz; N0/N1/N2 hâlâ yeşil). **GÖRSEL uçtan-uca** (deep-link gerçek gateway verisiyle SessionView gösterir)
  = fork-runtime-gateway'li kontrol (N5 / kullanıcı host'u) — dürüstçe ertelendi (smoke'ta gateway yok → veri yok).
- **Sıradaki: N3b** — session detail YANINDA native diff (4C-1) split editör grubunda (`openSession` o task'ın
  diff'ini `ViewColumn.Beside`'da açar). → N4 native-etkileşim → N5 fork-default + capstone (KULLANICI ONAYI).
- ADR-0021 / ADR-0027/0028/0029 (cockpit reuse) / ADR-0031 / ADR-0032 / ADR-0036 / ADR-0037 KORUNUR.
