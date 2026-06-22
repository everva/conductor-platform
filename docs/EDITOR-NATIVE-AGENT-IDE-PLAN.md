# Conductor Editor — native agent-IDE layout plan (Devin 2.0 / Devin Desktop model)

Kullanıcı düzeltmesi (2026-06-22): web cockpit'i renk/effect/token ile cilalamak
(V0–V5c) **asıl mesele değildi**. Devin'in tasarımı bir **native VSCode-OSS
agent-IDE layout'u** — bespoke web dashboard değil. conductor-editor ZATEN bir
Code-OSS fork; iş, cockpit'i o native IDE layout'una getirmek. **Bu plan o işin.**

> ⚠️ DİSİPLİN: UYDURMA YOK. Her faz gerçek Devin davranışına + gerçek VSCode
> runtime'ına dayanır. Doğrulama yüzeyi `:5173` web DEĞİL — **fork runtime'ı**
> (electron smoke + elle take-over ekran-görüntüsü).

## §0 — Devin'in GERÇEK modeli (araştırıldı, kaynaklı)
Devin 2.0 (Nisan 2025) + **Devin Desktop** (Haz 2026) = **agent-native IDE**:
- **Command Center kanban = DEFAULT ana yüzey.** Devin Desktop "IDE hiyerarşisini
  tersine çevirir: ilk karşılaştığın arayüz tüm aktif session'ların Kanban'ı
  (Agent Command Center); kod editörü tam erişilebilir ama **ikincil**."
- **İki-pane, resizable**: solda agent/chat/sessions, sağda **workspace + sekmeler:
  Progress · Shell · Browser · Editor**. Genişlik sürüklenerek ayarlanır.
- **GERÇEK editör**: sol file-tree, native diff, IDE kısayolları (**Cmd-K**=NL→terminal,
  **Cmd-I**=inline edit, tab-autocomplete, jump-to-def, dosya sekmeleri). Take-over.
- **Session time-travel**: session sayfasında **ok tuşları (→←↑↓)** ile workspace
  ilerlemesinde zamanda adım-adım.
- **Activity-bar** + "Your Devins" (paralel session'lar); çoklu-agent **ACP** ile
  aynı kanban'da peer (Claude/Codex/custom) + **Spaces** (proje-bağlam katmanı).
- Rol: developer = **fleet coordinator** (agent dispatch eden, kod üreten değil).

Kaynaklar: cognition.com/blog/devin-2 · the-agent-report.com/2026/06 (Devin Desktop)
· docs.devin.ai (session-tools, IDE) · CognitionAI/devin-extension.

## §1 — conductor-editor ŞU ANki durumu (kod haritalandı)
- Cockpit (board/session/intake — V0–V5c cilalı) = **TEK webview, activity-bar
  SIDEBAR'da** (`conductor.fleet`, dar). `FleetViewProvider` (extension.ts:340).
- **Zaten NATIVE**: diff (4C-1b `conductor-diff:` virtual doc + gerçek diff editörü;
  `DiffStore`/`DiffObserver`) · intervention toast + 3 status-bar item (bağlantı/
  $(bell)/$(git-compare)) (4C-3) · inline control komutları pause/resume/abort/approve
  (4C-2) · token-güvenli host↔webview postMessage bridge (4B-2; CSP `connect-src 'none'`).
- **Token disiplini (DONMUŞ)**: token yalnız SecretStorage (rest) + HTTP/WS header
  (transit); webview/mesaj/log'a ASLA. `*-leak-guard` testleriyle kanıtlı.
- Fork: everva/conductor-editor = Code-OSS overlay (ADR-0031), bu extension'ı inject eder.
- **ADR-0032** "command-center-default-surface" KARARI var ama editör hâlâ sidebar
  webview gönderiyor — yani native ana-yüzey HENÜZ uygulanmadı.

**Boşluk:** parçalar var (native diff, board, bridge) ama **native-IDE LAYOUT yok**:
cockpit dar sidebar; Command Center ana editör-alanı değil; native editör agent-
workspace olarak entegre değil; resizable iki-pane yok; ok-tuşu time-travel yok.

## §2 — Hedef layout (Devin → conductor eşlemesi)
```
┌────────────────────────────────────────────────────────────────┐
│ Activity bar │  EDITOR AREA (resizable groups)                  │
│  ▸ Conductor │  ┌────────────── Command Center ──────────────┐  │
│              │  │  default ana yüzey: status-Kanban board     │  │
│  SIDEBAR     │  │  (Ready/Running/Needs-Review/Done)          │  │
│  native tree │  └─────────────────────────────────────────────┘ │
│  "Conductors"│  ── session aç → split ──                         │
│  proj▸tasks  │  ┌─ Session detail ─┐ ┌─ NATIVE diff (4C-1) ──┐  │
│  status-icon │  │ verdict/spec/    │ │ gerçek diff editörü   │  │
│  click→reveal│  │ timeline (webview)│ │ (conductor-diff:)     │  │
│              │  └──────────────────┘ └───────────────────────┘  │
│ status bar:  $(plug) · $(bell) N · $(git-compare) N             │
└────────────────────────────────────────────────────────────────┘
```

## §3 — Fazlar (her biri ayrı; Rule#9 + GERÇEK runtime doğrulama)
- **N0 — Spike/de-risk**: Command Center'ı **ana editör-alanı singleton
  WebviewPanel** (ViewColumn.One) olarak aç (sidebar yerine) — gerçek fork
  runtime'da doğrula (electron). Fork layout default sorusunu netleştir. ADR.
- **N1 — Command Center = default ana yüzey**: `conductor.open` komutu + activate'te
  startup singleton WebviewPanel (editör alanı), V0–V5c board reuse, bridge aynen.
  Sidebar ikincil. (ADR-0032'yi UYGULAR.)
- **N2 — Native sessions sidebar (TreeView)**: sidebar webview yerine/yanında native
  `TreeDataProvider` "Conductors" (projeler→tasks, status codicon'lar, "Your Devins"
  analog). Click → Command Center'ı o session'a odakla / session aç. Hafif + çok-native.
  (Host-side veri: mevcut snapshot/REST; token host'ta.)
- **N3 — Session = workspace layout**: session drill-in çok-grup layout açar — session
  detail (webview) YANINDA native diff (4C-1) split editör grubunda (resizable). Native
  editör = diff workspace. "Open diff"/session panel layout'u düzenler.
- **N4 — Native etkileşim**: Conductor komutlarına keybinding (palette-native), session'da
  **ok-tuşu (←→) timeline-stepping** (web replay'i native'e bağla), quick-actions; 3
  status-bar item entegre.
- **N5 — Fork layout default + capstone**: Code-OSS overlay ince patch'ler (ilk açılışta
  Conductor activity-bar + Command Center editör-alanı default); imzalı fork rebuild;
  **gerçek runtime doğrula** (electron smoke + elle take-over ekran-görüntüsü); Devin-
  referans karşılaştır + **KULLANICI ONAYI**.

## §4 — Kabul (native-IDE 9/10 bar)
Command Center default ana yüzey · native sessions tree · session=workspace (detail+
native diff split) · resizable groups · IDE kısayol/komut entegrasyonu · ok-tuşu
time-travel · 3 status-bar · take-over native editörde · **gerçek fork runtime ekran-
görüntüsünde "Devin gibi" diyebilmem + KULLANICI ONAYI**. Devin-referansla yan-yana.

## §5 — Disiplin (DEĞİŞMEZ; web standardıyla aynı + native eklentiler)
Her faz: kısa spec → kodla → **Rule#9 editör gate** (typecheck BOTH tsconfig'ler +
eslint + vitest[mocked vscode] + esbuild build) **+ GERÇEK VSCode runtime doğrula**
(CP_VSCODE_SMOKE electron smoke + elle take-over; native iş headless tam doğrulanamaz —
yüzey fork runtime'ı, `:5173` DEĞİL) → push develop(+fork) → ledger+memory → **CI 3-job
(`gh run view --json conclusion`, watch exit'ine GÜVENME)**. **Frozen-additive (ADR-0021;
gateway/kontrat değişmez, additive)**. **Token disiplini DONMUŞ** (SecretStorage+header;
webview/log/mesaj'a ASLA; `*-leak-guard` testleri korunur). V0–V5c **design-system'i
(token/primitive)** webview yüzeylerinde reuse. **Paylaşılan-cockpit yeni dep → editor/
package.json + tsconfig.webview.json `paths`** (V4 lucide gotcha). UYDURMA YOK — gerçek
Devin'e + gerçek runtime'a dayan. Yeni ADR'ler (editör-alanı-panel, native-sessions-tree).
Canlı optiway/xirigo'ya DOKUNMA. isolation:"worktree" KULLANMA.

## DURUM (2026-06-22) — PLAN HAZIR, N0'dan başlanacak
- Web design-system (V0–V5c) ✅ CI-yeşil (develop `576061b`) — **görsel katman; native
  layout içindeki webview yüzeyleri için REUSE edilir, çöp değil.** Ama asıl iş bu plan.
- Editör arch haritalandı (§1) + Devin modeli araştırıldı (§0). Native parçalar mevcut
  (diff/toast/status-bar/bridge); LAYOUT eksik.
- **SIRADAKİ: N0 spike** — Command Center'ı fork runtime'da editör-alanı singleton panel
  olarak aç, de-risk + ADR → N1 default-surface → N2 native tree → N3 session-workspace
  → N4 native-etkileşim → N5 fork-default+capstone. Her faz GERÇEK runtime KENDİM.
