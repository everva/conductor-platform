# ADR-0042 — Agentic tree context menus + context keys + keybindings (Faz-P / P3)

## Bağlam
Faz-P denetimi (kod'a-dayalı) agentic/VS-Code-uyum eksiklerini saptadı: native "Conductors"
sessions tree'de **`view/item/context` menüsü YOK** (session/proje'ye sağ-tık→aksiyon yok),
keybinding yalnız `conductor.open`, **context-key/when-clause YOK** (komutlar koşulsuz), ve
Welcome sekmesi 0003'e rağmen göründü (doğrulanacak). Kullanıcı 3. talebi: "tam agentic ve VS
Code full uyumlu olsun." TreeItem.contextValue ZATEN set (`conductorProject`/`conductorTask`,
N2/ADR-0037) → menü eklemeye HAZIR. Kontrol komutları (pause/resume/abort/approve) + diff komutu
zaten var ama palette-only (her zaman proje quick-pick'i sorar). Frozen-additive; editör-only.

## Karar
**Tree'yi agentic yap — sağ-tık aksiyonlar + context-key + keybinding (yalnız `editor/`):**
- **`view/item/context` menüleri** (package.json): **proje** item'ı (`viewItem == conductorProject`)
  → Approve / Pause / Resume / Abort (`1_control` grubu); **task** item'ı (`viewItem ==
  conductorTask`) → Open Session / Open Diff (`navigation` grubu). Hepsi `view ==
  conductor.sessions` ile sınırlı.
- **Komutları context-AWARE yap** (extension.ts): `runControl`'a opsiyonel `preselectedProjectId`
  — context menüden tıklanan proje DOĞRUDAN işlenir (listProjects + quick-pick YOK); palette
  yolu (preselect yok) eski list-then-pick akışını korur. Komut handler'ları tıklanan NODE'u
  `nodeProjectId(arg)` ile çözer (sessionsTree.ts pure helper). Yeni **`conductor.openTaskDiff`**
  komutu (task context "Open Diff" → o task'ın diff'i, `runShowDiffForTask`). `openSession`
  handler'ı HEM tree-click `[projectId, taskId]` HEM context-menu task-node'unu çözer (`nodeTaskRef`).
- **Context key `conductor.connected`** (extension.ts): bağlantı durum değişiminde `setContext`
  ile yayınlanır; keybinding'ler `when: conductor.connected` ile koşullanır (bağlıyken).
- **Keybinding seti**: `conductor.showDiff` (⌘⌥D) + `conductor.refreshSessions` (⌘⌥R), ikisi de
  `when: conductor.connected`; mevcut `conductor.open` (⌘⌥C) korunur.
- **commandPalette gizleme**: arg-gerektiren `openSession`/`openTaskDiff` palette'te `when:false`
  (anlamsız no-op olmasınlar; yalnız click/context'ten çalışırlar).
- **Welcome**: 0003 (`startupEditor:none`) mekanizması DOĞRU; "yine göründü" = ortam (kullanıcı
  ayarı/cache state). Kod değişikliği YOK — fork runtime'da yeniden doğrulanır (kullanıcı).

## Sonuçlar
- **P3 (bu commit):** yalnız `editor/` (package.json contributes + extension.ts handler'lar +
  sessionsTree.ts node-helper'ları + testler). Backend/Go + web/src DOKUNULMADI. Token DONMUŞ
  (helper'lar yalnız id taşır). **GERÇEK-FORK BULGUSU (smoke yakaladı, mocked-gate kaçırdı):**
  menü `conductor.openSession`'a referans veriyordu ama `contributes.commands`'ta DEKLARE
  değildi (VS Code menü komutlarının deklarasyonunu şart koşar) → commands'a eklendi. Bu tam da
  "fork runtime'da doğrula" disiplininin değeri.
- **Doğrulama (Rule#9 editör gate + GERÇEK fork runtime):** editör gate (typecheck×2 + eslint-0 +
  **vitest 242/3**: runControl preselected[no-pick]+confirm-gated, nodeProjectId/nodeTaskRef
  çözümleme, openTaskDiff register, disposable sayıları + esbuild) + **electron smoke YEŞİL**
  (VS Code 1.125.1: contributes geçerli, **menü-uyarısı YOK**, activate + N1 startup, exit 0).
- **GÖRSEL capstone (kullanıcı host'u):** gerçek fork'ta session'a sağ-tık → agentic aksiyonlar +
  keybinding'ler + ONAY.
- **→ FAZ-P İŞLEVSEL TAMAM (P1 tema + P2a/P2b native diff + P3 agentic/uyum).** Kalan: kullanıcı
  deploy (P2b 00008 migration) + görsel capstone'lar + onay.
- ADR-0037 (sessions tree contextValue) / ADR-0040 / ADR-0041 / N5b 0003 (Welcome) KORUNUR.
