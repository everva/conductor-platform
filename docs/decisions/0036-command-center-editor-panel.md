# ADR-0036 — Command Center editör-alanı paneli (ADR-0032'nin artımlı uygulanışı, N0)

## Bağlam
ADR-0032 kararı: Conductor Editor'ün ana yüzeyi = **Command Center**, mekanizma = editör extension
`onStartupFinished`'da **singleton WebviewPanel** (cockpit bundle reuse; workbench'e patch yok). Ama o karar
E1–E5 **web** redesign'ı kapsamında alındı ve **editör tarafı hiç sevk edilmedi**: `editor/` hâlâ cockpit'i
activity-bar **sidebar** webview'i (`conductor.fleet` / `FleetViewProvider`) olarak gönderiyor. Native parçalar
mevcut (4C-1 native diff, 4C-3 intervention toast + status-bar, 4B-2 token-güvenli postMessage bridge) ama
**native-IDE LAYOUT yok** — Command Center ana editör-alanı değil.

Native agent-IDE planı (`docs/EDITOR-NATIVE-AGENT-IDE-PLAN.md`, Devin 2.0 / Devin Desktop modeline dayalı) bu
boşluğu kapatır. **N0** o planın ilk, riski-azaltıcı adımı: Command Center'ı **gerçek fork runtime'ında** editör
alanında açılan bir panele dönüştürmek — flip'ten önce kanıtla.

Kısıt: ADR-0031 (thin-overlay) gereği Code-OSS workbench'ine **patch yok**; her şey extension API'siyle. ADR-0027/
0029 (fork/cockpit reuse) + **token disiplini DONMUŞ** (token yalnız SecretStorage + HTTP/WS header; webview/
mesaj/log'a ASLA; `*-leak-guard` testleri) korunur.

## Seçenekler
- **(a) Editör-alanı singleton WebviewPanel, sidebar ile YAN YANA (artımlı).** Yeni `conductor.open` komutu +
  `CommandCenterPanel` (ViewColumn.One). `FleetViewProvider`'ın TAM cockpit+bridge tellemesini reuse eder
  (`webviewHtml` + `makeBridgeFactory`). Sidebar kaldırılmaz; başlangıçta otomatik-açılış ŞİMDİLİK yok. N0
  paneli sevk eder + gerçek runtime'da kanıtlar; N1 default'u çevirir.
- **(b) Sidebar view'ı hemen panelle değiştir.** Tek harekette daha büyük; geri-alınması zor; N0'ın de-risk
  amacını bozar (kanıtla-sonra-çevir disiplini gider). Reddedildi.
- **(c) Workbench'i fork-patch'le (custom layout).** ADR-0031 thin-overlay'i bozar, upstream-merge pahalı.
  Reddedildi (ADR-0032 (b) ile aynı gerekçe).

## Karar
**(a).** N0 şunu sevk eder:
- **`conductor.open` komutu** ("Conductor: Open Command Center") + **`CommandCenterPanel`** sınıfı: editör
  alanında (`ViewColumn.One`) **singleton** WebviewPanel. İkinci `conductor.open` yeni panel doğurmaz, mevcudu
  **reveal** eder.
- **Tellemeyi reuse eder**, kopyalamaz: aynı strict-CSP `webviewHtml` (dist/webview bundle'ını nonce-load eder)
  + aynı `makeBridgeFactory` HostBridge. Yani panel **YENİ bir token yüzeyi DEĞİL** — CSP `connect-src 'none'`
  webview-kaynaklı ağı yasaklar, auth host-side kalır (sidebar ile birebir aynı güvenlik duruşu).
- **`retainContextWhenHidden: true`**: command center uzun-ömürlü; sekme arkaplandayken canlı cockpit + WS
  bridge'i ayakta tutar (her sekme-değişiminde remount = event akışını düşürmek olurdu).
- **Sidebar Fleet view KALIR** (coexist). N0 sidebar'ı kaldırmaz (o N2: native sessions TreeView) ve
  **başlangıçta otomatik açmaz** (o N1: ADR-0032 default-surface flip'i). N0'ın işi: paneli + komutu sevk et,
  gerçek runtime'da editör-alanına indiğini kanıtla.

## Sonuçlar
- **N0 (bu commit, `editor/` only):** `editor/src/extension.ts` (`OPEN_COMMAND` + `COMMAND_CENTER_VIEW_TYPE`/
  `_TITLE` + `CommandCenterPanel` + `registerConductor` tellemesi) + `editor/package.json` (komut katkısı) +
  unit testler (`extension.test.ts`: open/singleton/teardown/dispose/token-CSP; mock'a `createWebviewPanel` +
  `ViewColumn` eklendi) + **in-host electron smoke** (`test/suite/extension.test.ts`): GERÇEK VS Code host'ta
  `conductor.open` çalıştırılır → editör-alanında **"Conductor" sekmesi** açılır (tab-groups ile doğrulanır) +
  tekrar açınca tek sekme (singleton). Backend / gateway / Go **DOKUNULMADI** (ADR-0021 frozen-additive).
- **Doğrulama (Rule#9 + gerçek runtime):** editör gate (typecheck her iki tsconfig + eslint `--max-warnings 0`
  + vitest + esbuild) yeşil; **`CP_VSCODE_SMOKE=1` electron smoke YEŞİL** (gerçek 1.125.1 host'ta panel editör-
  alanına indi: `tabs after conductor.open: ["Conductor"]`). Doğrulama yüzeyi `:5173` web değil — **fork
  runtime'ı** (plan §5).
- **Sıradaki (plan §3):** **N1** başlangıç default-surface flip (`onStartupFinished` singleton panel; ADR-0032'yi
  tam UYGULAR) → **N2** native sessions TreeView (sidebar webview yerine) → **N3** session=workspace (detail +
  native diff split) → **N4** native etkileşim (keybinding + ok-tuşu time-travel) → **N5** fork layout-default +
  imzalı rebuild + capstone.
- ADR-0021 (frozen-additive), ADR-0027/0029 (fork/cockpit reuse), ADR-0031 (thin-overlay), **ADR-0032**
  (command-center default-surface — bu ADR onun editör-tarafı artımlı uygulanışıdır) KORUNUR.
