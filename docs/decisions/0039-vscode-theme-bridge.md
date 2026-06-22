# ADR-0039 — Fork-webview VS Code theme bridge (Faz-P / P1)

## Bağlam
N0–N5 native-IDE layout gerçek fork'ta canlı. Kullanıcı (2026-06-22) gördükten sonra: "temayı VS Code'da
daha uygun yap, şu an uyumlu değil; Devin.ai gibi profesyonel olsun." Denetim (kod'a dayalı): cockpit
(`web/src/theme/tokens.css`) **sabit cool-gray DARK** token seti kullanıyor; `web/src` genelinde
**`--vscode-*` = 0**; `prefers-color-scheme` / `.vscode-light|dark` handling yok → editör temasından
BAĞIMSIZ. Sonuç: light/high-contrast editörde bariz çakışır, dark'ta bile editörden farklı; webview
editör fontunu (`--vscode-font-family`) onurlandırmıyor. VS Code her webview kök'üne aktif temayı
`--vscode-*` custom property'leri olarak enjekte eder — cockpit bunları HİÇ okumuyordu.

Kısıt: ADR-0029 (paylaşılan cockpit reuse) → cockpit web App + fork webview tarafından PAYLAŞILIR; web/src'e
dokunuş web App'in markalı görünümünü BOZMAMALI. Token disiplini DONMUŞ (bu katman saf sunum, token taşımaz).
Frozen-additive (ADR-0021): yalnız editör-içi; backend/Go DOKUNULMAZ.

## Seçenekler
- **(a) Fork-only token override katmanı.** Editör-yerel `editor/webview/theme-vscode.css`: cockpit'in
  SEMANTİK CHROME token'larını (`--bg`/`--surface-*`/`--text-*`/`--border-*`/`--font-*`/`--focus-ring`/
  scrollbar) `var(--vscode-…, <web-fallback>)`'a re-map eder; fork entry'sinde (`webview/main.tsx`) tokens.css
  + index.css'ten SONRA import edilir → `:root` redefinitions cascade'i kazanır. Web App bu dosyayı İMPORT
  ETMEZ → markalı görünümü byte-aynı. `--vscode-*` tema değişiminde otomatik değişir → light/dark/HC JS'siz. [seçildi]
- **(b) tokens.css'i `--vscode-*` ile değiştir (paylaşılan).** Web App'i de etkiler (standalone'da `--vscode-*`
  yok → fallback'e düşer ama markalı kontrol kaybolur); ADR-0029 paylaşım sınırını bulandırır. Reddedildi.
- **(c) JS ile tema tespit + dinamik token.** `--vscode-*` zaten reaktif; JS gereksiz karmaşa. Reddedildi.
- **(d) Fork'a zorunlu "Conductor" color-theme ship et.** Kullanıcının kendi temasını EZER (Devin kendi
  temasını ship etse de kullanıcı temasına saygı daha iyi varsayılan). Opsiyonel follow-up olarak bırakıldı.

## Karar
**(a).** P1 şunu sevk eder (yalnız `editor/`):
- **`editor/webview/theme-vscode.css`** — chrome bridge. İLKELER:
  - **SURFACES** → editör'ün tema-ayarlı bg rampası (`--vscode-editor-background` / `sideBar-background` /
    `editorWidget-background` / `list-hoverBackground` / `input-background`). Tema yazarı elevation YÖNÜNÜ
    light (raised=daha açık kart) + dark için doğru ayarladığından, foreground-mix yerine bunlar kullanıldı.
  - **BORDERS** → `color-mix(in srgb, var(--vscode-foreground) %, transparent)` (soft 8% / default 14% /
    strong 24%): dark'ta ince AÇIK, light'ta ince KOYU hairline — ikisi de doğru + gerçek hiyerarşi.
    (Web default'u beyaz-alpha border light temada KAYBOLUR.) VS Code'un Chromium'u color-mix destekler.
  - **TEXT** → `--vscode-foreground` / `descriptionForeground` / `disabledForeground`. `--text-on-accent`
    (#fff) KASITLI re-map'lenMEZ (her temada yeterince koyu marka dolgularının üstünde durur).
  - **BRAND** → KORUNUR (indigo→violet kimlik: `--brand`/`--brand-strong`/gradient/soft). Yalnız
    `--brand-text` → `--vscode-textLink-foreground` (link metni temalı/light bg'de okunabilir kalsın).
  - **STATUS** → bazlar `--vscode-charts-{green,yellow,red,blue}`; translucent soft/border alpha'lar korunur.
  - **TYPE** → `--font-sans` → `--vscode-font-family`, `--font-mono` → `--vscode-editor-font-family`.
  - **FOCUS** → `--vscode-focusBorder`. **SCROLLBAR** → `--vscode-scrollbarSlider-background` (index.css
    `--gray-700`'ü ezer; bu yüzden index.css'ten SONRA import).
  - HER map web token'ını FALLBACK tutar → eksik `--vscode-*` var marka-dark'a düşer (kırılmaz).
- **`editor/webview/main.tsx`** — import EN SONA (`tokens.css` → `index.css` → `theme-vscode.css`): `:root`
  override'ları tokens.css'i, scrollbar kuralları index.css'i kazanır. (Bonus: `--vscode-font-family`
  onurlandığından Geist webfont artık zorunlu değil — opsiyonel.)
- **`editor/src/webviewBundle.test.ts`** — guard: theme-vscode.css bundle input + emitted CSS load-bearing
  chrome token'larını `--vscode-*`'a (fallback'li) map eder (`--bg`/`--text-1`/`--font-sans`).

## Sonuçlar
- **P1 (bu commit):** yalnız `editor/` (theme-vscode.css + main.tsx import + test guard + ADR). **web/src
  DOKUNULMADI** → web App byte-aynı, web gate tetiklenmez. Backend/Go DOKUNULMADI (ADR-0021). Token DONMUŞ.
- **Doğrulama (Rule#9 editör gate + gerçek runtime):** editör gate (typecheck×2 + eslint-0 + **vitest 206/3**
  incl. yeni bridge guard + esbuild) + **emitted main.css denetimi** (tokens.css `:root` ÖNCE → theme-vscode
  `:root` SONRA kazanır; `color-mix` 3× passthrough unmangled; `--brand` kimlik korunur) + **electron smoke
  YEŞİL** (VS Code 1.125.1: N1 startup `tabs: ["Conductor"]`, yeni bundle throw-suz boot, exit 0).
- **GÖRSEL capstone (KULLANICI host'u):** fork rebuild/inject + **light + dark ekran-görüntüsü** + ONAY —
  `--vscode-*` yalnız gerçek webview'de var olduğundan görsel doğrulama fork runtime'ında yapılır (smoke
  CSS'i değerlendirmez). Bu, N0–N5 ile aynı "fork runtime + kullanıcı görsel onayı" disiplinidir.
- **Sıradaki: P2** — native `vscode.diff` (unified patch'ten per-file reconstruct = P2a, backend'siz;
  full-file = P2b frozen-additive backend) → **P3** agentic/uyum (tree context-menü + keybinding + context-key).
- ADR-0021 / ADR-0027/0028/0029 (cockpit reuse) / ADR-0031 / ADR-0032 / ADR-0035 (visual-language) /
  ADR-0036 / ADR-0037 / ADR-0038 KORUNUR. Opsiyonel default "Conductor" color-theme ship'i ileriye bırakıldı.
