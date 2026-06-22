# ADR-0044 — Host-owned selection channel + single cockpit connection (Faz-Q / Q0)

## Bağlam
Kullanıcı canlı fork'u görünce yapısal eleştiri yaptı (2026-06-22): **"daha native olsun, web
uygulaması gibi değil."** Kod'a-dayalı kök neden (`docs/EDITOR-NATIVE-COMPOSITION-PLAN.md` §2):
TEK monolitik `web/src/fleet/FleetDashboard.tsx` (kendi statusbar + tab board/fleet/events/intake
+ SessionView) **İKİ kez mount ediliyor** — sidebar (`FleetViewProvider`) + editör-alanı
(`CommandCenterPanel`). Bunun iki gerçek sonucu var:
1. **Seçim paylaşılmıyor (kullanıcı #1):** seçim = her instance'ın iç React state'i
   (`selectedProjectId`/`selectedTask`/`tab`). Native "Conductors" tree → CC'ye yalnız task-deep-link
   eder (N3 `navigate-session`); **proje seçmek hiçbir yüzeyi güncellemez.**
2. **offline/live BUG:** iki mount → AYRI `useFleet`/`useEventStream`/WS → AYRI `streamState`.
   Ekran-görüntüsünde sol "● offline", sağ "● live" (aynı an, aynı gateway).

N3'te task-deep-link için zaten bir host↔webview **kontrol kanalı** var (`navigate-session` +
`webview-ready`, correlation-id YOK → bridge router'ları reddeder, token YOK). Bu kanal yalnızca
"bir task'a git" yapabiliyordu — proje seçimini taşıyamıyordu.

## Karar
**Native sessions tree SEÇİMİN TEK SAHİBİ; cockpit webview bir PROJEKSİYON.** N3 task-only
`navigate-session` kontrol mesajını **tek genel `select` kanalına genelleştir:**

- **`select { project, task? }`** (`HostSelectMessage` + `isSelect` guard, `bridge/protocol.ts`):
  `task` YOK → projeyi seç (cockpit board'unu/yüzeylerini ona kapsar); `task` VAR → ayrıca o task'ın
  SessionView'ini aç (eski `navigate-session` deep-link'i `select`-with-task olur). Correlation-id
  YOK (bridge router'ları reddeder), **token YOK** — yalnız project/task id'leri.
- **Host** (`CommandCenterPanel.select(project, task?)`): cold-start buffer korunur (`#pendingSelect`,
  `webview-ready`'de flush); `conductor.openSession` artık `select(project, task)` çağırır. Proje-only
  `select` `task` alanını TELE KOYMAZ (`{ kind, project }`).
- **Web** (`FleetDashboard` `selection?: { project, task? }` prop, `navigateTo`'nun yerine): bir effect
  seçimi sürer — task VAR → live fleet'e çözüp SessionView (board drill-in'in AYNI seam'i); task YOK →
  `selectedProjectId` set + board'a dön. Distinct-obje useRef guard ile bir kez tüketilir.

**Tek bağlantı (Q0.4, ayrı commit):** sidebar webview cockpit'i (`FleetViewProvider` + `conductor.fleet`
view) KALDIRILIR → cockpit yalnız Command Center'da mount edilir → TEK `useEventStream`/WS → **offline/live
bug FIX.** Sidebar yalnız native "Conductors" tree'yi (seçim sürücüsü) taşır.

## Sonuçlar
- **Bu commit (Q0.1–0.3):** `editor/` (protocol + `CommandCenterPanel` + fork `main.tsx`) + paylaşılan
  `web/src/fleet/FleetDashboard.tsx` (prop rename + effect). Web App (`web/src/main.tsx`) prop'u GEÇMEZ →
  byte-aynı (selection opsiyonel). Backend/Go DOKUNULMADI (ADR-0021). **Token DONMUŞ** (kanal token-free).
- **Doğrulama:** editör gate (typecheck×2 + eslint-0 + **vitest 241/3** incl. yeni `isSelect` guard +
  proje-only select host testi + esbuild) + **web gate** (typecheck + eslint-0 + **vitest 160/160**:
  `cockpit.test.tsx` task-select→session, unknown-task no-op, **proje-only select→board'a dön**) +
  **Playwright e2e 15/15** + **GERÇEK fork runtime electron smoke YEŞİL** (1.125.1, N1 startup
  `["Conductor"]`, `select` deep-link komut yolu throw-suz, exit 0).
- N3'ün deep-link davranışı KORUNUR (`select`-with-task = eski `navigate-session`); seçim artık
  proje düzeyinde de taşınabilir. Proje-only seçimin GÖRÜNÜR board-filtresi Q1'de (seçim-güdümlü
  yüzeyler); bu commit seam'i kurar.
- **Q0.4 SEVK EDİLDİ (ayrı commit):** `FleetViewProvider` sınıfı + `conductor.fleet` view contribution
  + `FLEET_VIEW_ID` + `placeholderHtml` (no-config sidebar HTML) + bunların testleri KALDIRILDI; cockpit
  yalnız `CommandCenterPanel`'de mount edilir → TEK HostBridge/WS → **offline/live bug GİTTİ.** Sidebar
  yalnız native "Conductors" tree'yi taşır. **Editör-only** (extension.ts + package.json + testler; web/Go
  DOKUNULMADI). Doğrulama: editör gate (typecheck×2 + eslint-0 + **vitest 232/3**: registerConductor 11
  disposable + `registerWebviewViewProvider` ARTIK ÇAĞRILMAZ; subscriptions 20; extension.js 190.8→187.9kb)
  + **electron smoke YEŞİL** (1.125.1, N1 startup `["Conductor"]`, dangling view/menu referansı YOK, exit 0).
- ADR-0038 (N3 control channel) bu ADR ile genelleştirilir; ADR-0021 / token-disiplini KORUNUR.
