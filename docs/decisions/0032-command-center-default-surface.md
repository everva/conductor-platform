# ADR-0032 — Editör agent-native yeniden tasarım: Command Center = default surface (E1)

## Bağlam
Mevcut editör "VS Code + activity-bar'da Conductor cockpit paneli" — sektörün 2025–26'da TERK ETTİĞİ
model. Devin Desktop (eski Windsurf 2.0), Augment Intent, Google Antigravity AYNI desene yakınsadı:
**agent komuta merkezi = DEFAULT yüzey** (editör/chat değil), durum-bazlı **Kanban**, director-first.
Tam analiz + araştırma: `docs/EDITOR-AGENT-NATIVE-REDESIGN-PLAN.md`. Conductor'ın **backend'i bu modeli
zaten uyguluyor** (Fleet, multi-host, lifecycle, governance/approve, native diff, event bus) — hatta
rakiplerden disiplinli: **deterministik gate = TEK merge mercii** (LLM-yargıç DEĞİL). Eksik = UX çerçevesi.

Kod incelemesiyle doğrulanan kısıtlar (plan §0):
- **B1** gate "Verifier verdict" gözlemlenebilir DEĞİL: `KindDecision` yalnız `{result}` taşır, per-gate
  checks değil → session view için E2'de additive payload gerek (frozen-safe; `events.Event` zarfı sabit).
- **B2** scenario/acceptance gateway'de yok → E2'de additive read.
- **B3** tüm-task aggregate yok → board client-side fan-out (mevcut `useFleet` deseni).
- **B4** "default surface" thin-overlay'de MÜMKÜN (editör `onStartupFinished` + singleton WebviewPanel;
  Code-OSS workbench'e PATCH YOK).
- **B5** Conductor'da canlı shell/browser YOK (agent host'ta headless) → follow-along = event timeline +
  diff + verdict (Devin 4-pane'i değil; daha temiz).

## Seçenekler
- **(a) Webview command center = default surface, IDE drill-in'de.** Editör açılışında board gelir; native
  IDE bir oturuma drill-in edince. "Agent manager wrapped in an IDE" (Devin Desktop). Thin-overlay korunur.
- **(b) Workbench'i derin fork'la (custom layout).** Code-OSS'a ağır patch → upstream-merge pahalı,
  ADR-0031 (thin-overlay) bozulur. Reddedildi.
- **(c) Mevcut yan-panelde kal, sadece içeriği iyileştir.** "Bad design" şikayetini çözmez; sektör-dışı.
  Reddedildi.

## Karar
**(a).** Conductor Editor = **agent-native Command Center**, açılış yüzeyi. Board düzeni = **Durum-Kanban**
(kullanıcı kararı 2026-06-20): kolonlar Ready / Running / **Needs Review** (awaiting-approval + blocked) /
Done; "Needs Review" director'ın aksiyon-lane'i, en görünür. Mekanizma (B4): editör extension
`onStartupFinished`'da **singleton WebviewPanel** açar (cockpit bundle reuse); **workbench'e patch yok**.
Backend dokunuşu yalnız **2 additive** (B1 gate-checks event + B2 scenario read; her ikisi frozen-safe),
geri kalan tamamen `web/` + `editor/` webview. Differansiyatör (gate verdict) UX'in kahramanı olur.

Fazlar (plan §6): **E0** bu ADR · **E1** board (default yüzey) · **E2** session view + Verifier verdict
(B1+B2) · **E3** spec-first intake · **E4** director cilası · **E5** ACP/standalone (opsiyonel).

## Sonuçlar
- **E1 (bu commit, web):** `web/src/fleet/board.ts` (pure bucketing) + `CommandCenter.tsx` (Kanban) +
  `board.css`; FleetDashboard'a **default "Board" tab** (non-breaking — Fleet/Events/Intake korunur).
  Veri = mevcut `useFleet` snapshot (fan-out, B3) + WS event buffer (canlı faz/diff); yeni gateway YOK.
  Kart tıklama → projeyi Fleet view'de odaklar (E1 drill-in; E2 tam session view ile değişir).
- Doğrulama (Rule#9): tsc + eslint + **96 vitest** + **7 Playwright e2e** (board default surface, 4 kolon,
  sayaçlar, host, Approve, drill-in) + **canlı board ekran görüntüsü** (KENDİM doğruladım,
  [[always-self-verify-with-playwright]]). Go/editör gate ETKİLENMEZ (yalnız `web/`).
- ADR-0021 (frozen-additive), ADR-0025 (gateway frontend-agnostik), ADR-0027/0028/0029 (fork/cockpit
  reuse), ADR-0031 (thin-overlay) KORUNUR. İleride: **ADR-0033** (gate verdict event), **ADR-0034**
  (scenario read) E2'de.
