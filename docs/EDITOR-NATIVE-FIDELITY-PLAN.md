# Conductor Editor — Native Fidelity & Polish Plan (Faz-P)

Native-IDE layout (N0–N5, `docs/EDITOR-NATIVE-AGENT-IDE-PLAN.md`) **işlevsel olarak bitti +
GERÇEK fork'ta CANLI** (kullanıcı ekran-görüntüsüyle doğruladı): sol native "Conductors" tree
+ ana-alan Command Center + activity-bar + session=workspace + ok-tuşu time-travel. Sonra
kullanıcı gerçek fork'u görüp **net feedback** verdi (2026-06-22). Bu plan o feedback'in.

> ⚠️ DİSİPLİN: UYDURMA YOK. Her şey gerçek Devin'e + gerçek fork runtime'ına + kullanıcının
> ekran-görüntülerine dayanır. Doğrulama yüzeyi `:5173` web DEĞİL — **fork runtime'ı**.

## §0 — Durum (Faz-P öncesi)
- N0–N5 native-IDE layout CANLI (develop 7 commit `08c790e…321a627` + fork `5b67cbc`, CI-yeşil).
- **CSS-foundation bug düzeltildi** (`321a627`): fork webview `tokens.css`+`index.css` import etmiyordu
  → tamamen stilsizdi → fix + `webviewBundle.test` guard. **GOTCHA (kalıcı): `@cockpit` barrel visual
  foundation'ı İÇERMEZ; consumer entry'si tokens+index (+font) import etmeli.**
- esbuild arm64 arch fix (kullanıcı makinesinde build için `@esbuild/darwin-arm64` yan-kuruldu).

## §1 — Kullanıcı feedback'i (gerçek fork'u gördükten sonra)
1. **TEMA**: "temayı VSCode'da daha uygun yap, şu an uyumlu değil; Devin.ai gibi profesyonel olsun."
2. **AGENTIC + UYUM**: "tam agentic ve VS Code full uyumlu olsun; eksik yer varsa kontrol et."
3. **DIFF**: "diff gösterimi iyi değil; Claude Code ve ya native git diff gibi olsun."

## §2 — EKSİKLER LİSTESİ (kod'a dayalı; post-compact GENİŞLET)
**TEMA / GÖRSEL UYUM:**
- Cockpit **sabit cool-gray dark** token'lar kullanıyor (`web/src/theme/tokens.css`); **`--vscode-*`
  YOK** (grep: 0). Editör temasıyla ÇAKIŞIR (light/HC'de bariz; dark'ta bile editörden farklı gri/renk).
- Light / high-contrast tema desteği yok.
- Webview editör fontunu (`editor.fontFamily`) onurlandırmıyor; Geist/system-ui sabit.
- (Geist webfont fork build'inde yüklü değil → şu an system-ui fallback.)
**DIFF (native değil):**
- Diff = TEK salt-okunur `conductor-diff:` **virtual doc**, içinde **unified PATCH metni** (`.diff`
  dili → kaba +/- renk). `renderDiffDocument` → `showTextDocument` (vscode.diff DEĞİL). Yani:
  per-file gezinme yok, yan-yana/inline native diff editörü yok, gutter/red-green yok. **İstenen:
  native `vscode.diff`** (base|branch, dosya başına) = git/Claude-Code görünümü. **Engel:** KindDiff
  yalnız unified patch taşır; native diff için base+modified İÇERİK lazım → **frozen-additive backend**
  (zengin diff event YA DA fetch endpoint) gerekir.
**AGENTIC / VS-CODE UYUMU:**
- Tree item **context menu YOK** (`view/item/context` yok) → bir session'a sağ-tık → aksiyon yok
  (approve/abort/open-diff/pause). Sadece `view/title` (refresh).
- Keybinding sadece `conductor.open`. approve/diff/refresh/session-step yok.
- **Context key / when-clause yok** (`conductor.connected` vb.) → komutlar koşulsuz.
- Welcome sekmesi gerçek fork'ta hâlâ göründü (0003 `startupEditor:none`'a rağmen) → DOĞRULA
  (kullanıcı ayarı mı, 0003 yetmedi mi; belki ek default'lar).
- Genişlet (post-compact denetim): komut-paleti tamlığı, a11y, multi-root, status-bar cilası,
  intake/"New work" akışının nativeliği, take-over'ın native editöre tam oturması.

## §3 — Fazlar (her biri ayrı; Rule#9 + GERÇEK fork runtime)
- **P1 — VS Code tema entegrasyonu.** Fork-webview token override katmanı: semantic token'lar
  `var(--vscode-…, <web-fallback>)`'a map'lenir (web App brandlı kalır, fork editör temasına UYAR).
  Light/dark/HC; editör fontunu onurlandır. Opsiyon: fork'a default "Conductor" color-theme ship +
  cockpit `--vscode-*` ⇒ kohezyon (Devin kendi temasını ship eder). Gerçek fork'ta light+dark ekran-
  görüntüsüyle doğrula. ADR (theme-tokens).
- **P2 — Native diff editörü.** Task diff'i **dosya başına native `vscode.diff`** (base|branch) olarak
  render et (git/Claude-Code görünümü: yan-yana/inline, red-green gutter, gez). base+modified içeriğini
  **frozen-additive backend**'den al (P2 ADR kararı: zengin DiffSummary alanı VS GET file-content
  endpoint; fetch-on-open daha yalın). Mevcut `conductor-diff:` unified görünümü fallback/yedek kalabilir.
- **P3 — Agentic + VS-Code tamlığı (eksikler listesini kapat).** Tree `view/item/context` menüleri
  (session başına approve/abort/open-diff/pause), keybinding seti, context key'ler + when-clause'lar,
  Welcome bastırma doğrula+düzelt, palette/a11y/font cilası. Denetimi tüketene kadar.

## §4 — Kabul (gerçek fork runtime'ında)
Cockpit editör temasına UYAR (light+dark; profesyonel/Devin-grade) · diff **native yan-yana/inline**
(red-green, gezilebilir) açılır · sağ-tık + klavye agentic aksiyonları kapsar · yabancı-görünen yüzey
yok · ekran-görüntüsünde "Devin gibi/native VS Code gibi" + **KULLANICI ONAYI**. Devin + native git-diff
referansıyla yan-yana.

## §5 — Disiplin (DEĞİŞMEZ; N0–N5 ile birebir aynı)
Her faz: kısa spec → kodla → **Rule#9 editör gate** (typecheck BOTH tsconfig + eslint `--max-warnings 0`
+ vitest[mocked vscode] + esbuild) **+ paylaşılan cockpit'e dokunulduğunda web gate** (tsc+eslint+vitest
+ **Playwright e2e**, `:5173` CANLI KENDİM) **+ GERÇEK fork runtime doğrula** (CP_VSCODE_SMOKE electron
smoke + kullanıcının elle take-over ekran-görüntüsü — yüzey fork runtime'ı, `:5173` DEĞİL) → push
develop(+fork) → ledger+memory → **CI 3-job (`gh run view --json conclusion`, watch'a GÜVENME)**.
**Frozen-additive (ADR-0021): gateway/Go-kontrat değişmez, yalnız additive** (P2'nin diff-source'u
additive olmalı). **Token disiplini DONMUŞ** (SecretStorage+header; webview/log/mesaj'a ASLA; leak-guard).
**CSS-foundation gotcha** (fork entry tokens.css+index.css import etmeli). **Paylaşılan-cockpit yeni dep
→ editor/package.json + tsconfig.webview.json `paths`** (lucide/V4 gotcha; P1 font / P2 için geçerli).
Yeni ADR'ler (theme-tokens, native-diff-source). Canlı optiway/xirigo'ya DOKUNMA. isolation:"worktree"
KULLANMA. **UYDURMA YOK** — gerçek Devin + native git-diff + gerçek fork runtime + kullanıcı ekran-
görüntülerine dayan.

## DURUM (2026-06-22)
- N0–N5 + CSS-fix CANLI + CI-yeşil (develop `321a627`, fork `5b67cbc`). Native layout gerçek fork'ta çalışıyor.
- **Eksikler denetimi TAMAM** (§2 kod'a-dayalı doğrulandı: tema `--vscode-*`=0 · diff unified-text `vscode.diff`
  değil · agentic `view/item/context`+keybinding+context-key yok). Kullanıcı **P1**'i ilk faz seçti.
- **P1 ✅ SEVK EDİLDİ — VS Code tema köprüsü** (develop `1796144`, **CI 3-job YEŞİL**: Go + web cockpit
  [e2e dahil] + editor). ADR-0039. `editor/webview/theme-vscode.css`: chrome token'ları (`--bg`/`--surface-*`/
  `--text-*`/`--border-*` color-mix/`--font-*`/`--focus`/scrollbar) → `var(--vscode-…, web-fallback)`; main.tsx'te
  EN SONA import (cascade kazanır); marka KORUNDU; **web/src + Go DOKUNULMADI** (web App byte-aynı). Doğrulama:
  editör gate (vitest 206/3 + bridge guard) + emitted main.css denetimi (cascade + color-mix passthrough + brand)
  + **electron smoke YEŞİL** (VS Code 1.125.1, exit 0). **KALAN: kullanıcı görsel capstone** (fork pull+inject +
  light/dark ekran-görüntüsü + ONAY); `--vscode-*` yalnız gerçek webview'de → görsel fork runtime'da doğrulanır.
- **SIRADAKİ: P2 native diff** (P2a unified-patch'ten per-file `vscode.diff` reconstruct = backend'siz;
  P2b full-file = frozen-additive backend) → **P3 agentic/uyum**. Her faz GERÇEK fork runtime + kullanıcı onayı.
