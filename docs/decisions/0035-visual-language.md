# ADR-0035 — Conductor görsel dili: design-token sistemi + premium tip (V0)

## Bağlam
Cockpit (web/) İŞLEVSEL olarak tam (board · session · intake · ⌘K · needs-review
· replay · multi-select — E1–E4) ama kullanıcı **görsel zanaatı 3/10** buldu;
hedef Devin.ai seviyesi **≥9/10** (`docs/EDITOR-DEVIN-GRADE-UI-PLAN.md`). Kök
neden bir FEATURE eksiği değil: **design system yok**. Renk/spacing/type token'ı
olmadığı için her CSS dosyası kendi literal'lerini taşıyor ve sapıyor —
`index.css` `#14161a` · `fleet.css` `#1b1e24` · `board.css` `--cc-bg:#14161c` —
üç ayrı "dark gri" hissi. Tipografi sistem fontu (hiyerarşisiz), motion yok,
durum (skeleton/empty/error) eksik, ikonlar emoji/glyph. Bunları yüzey-yüzey
elle düzeltmek tutarsızlığı büyütür.

## Karar
Önce **TEMEL**, sonra yeniden-skin. Tek kanonik token katmanı:
**`web/src/theme/tokens.css`** — `:root` üstünde iki kat:
- **Primitives**: soğuk-gri nötr rampa (`--gray-50`→`--gray-950`) + ham aksan
  hue'ları (indigo/violet/emerald/amber/red/blue). Component'ler bunlara
  DOĞRUDAN dokunmaz.
- **Semantic** (component'lerin kullandığı TEK katman): surface elevation
  (`--bg`/`--surface-1..4`), `--text-1..faint`, `--border(-soft/-strong)`,
  brand (`--brand`/`--brand-strong`/`--brand-gradient`/`--brand-ring`), status
  dörtlüsü (her biri base·text·soft·border: success/warn/danger/info), type
  (family + 8-adım scale + weight + tracking), space (4/8 ritim), radius,
  elevation (shadow-1..3 + pop + brand), `--focus-ring`, motion (dur + easing),
  z-index. `prefers-reduced-motion` süreleri sıfırlar.

**Premium tip**: **Geist Variable** (sans) + **Geist Mono Variable**,
`@fontsource-variable/*` ile **self-host** edilir → woff2 bundler'dan gelir,
HARİCİ istek yok = CSP-temiz, offline çalışır. `main.tsx` sırası:
@font-face → tokens.css → index.css. V0 görünür deltası = font + antialias +
token-driven base bg/text + tek tutarlı `:focus-visible` ring + `::selection`.
Yüzey/component literal'leri tokenlara V2+'da taşınır (board re-skin); bu ADR
yalnız TEMELİ koyar.

## Gerekçe
- **Tutarlılık ucuzlar**: bir kez token, her yüzey aynı dili konuşur; tema
  değişimi tek dosya re-map'i. Sapan literal'lerin kök nedeni ortadan kalkar.
- **Tip = en yüksek kaldıraç**: premium değişken font tek başına "amatör →
  rafine" algısının çoğunu taşır; foundation'da uygulanınca her yüzeye bedava
  yayılır.
- **Düşük risk / kanıtlı**: V0 yüzeyleri yeniden örmez — yalnız global font +
  base token uygular; davranış AYNEN korunur. Çalışma-zamanında doğrulandı
  (Playwright canlı :5173: `document.fonts.check('Geist Variable')=true`,
  `getComputedStyle(body).fontFamily`=Geist, `--bg`/`--brand` çözülüyor, 0
  console error). Geist Mono henüz lazy (hiçbir yüzey `--font-mono`'yu
  KULLANMIYOR; V2 card-id/.mono literal'lerini token'a bağlayınca yüklenir).
- **Self-host neden**: CSP-temizlik + offline + editör webview reuse (ADR-0029)
  — harici font CDN'i webview CSP'sinde sorun olurdu.

## Kapsam (sonraki dalgalar — yalnız referans, bu ADR'de DEĞİL)
V1 component primitives (`web/src/ui/`) → V2 shell+board re-skin (literal→token)
→ V3 session+intake+⌘K → V4 motion+states+lucide → V5 Devin-referans iterate-to-9.

## Frozen kontratlara etki
**HİÇBİRİ.** Saf görsel/web katmanı. Go/gateway/frozen kontrat (ADR-0021)
dokunulmaz; engine/statestore/EventBus imzaları sabit. Token disiplini: bearer
token webview/log/test'e GİRMEZ (bu iş zaten claude-free + secret-free).

## Durum
✅ V0 TAMAM. `tokens.css` + Geist self-host + global wiring + ADR. Canlı
Playwright KENDİM doğruladı (before/after board + signin; runtime proof). İlişkili:
ADR-0021 (frozen-additive), ADR-0028/0029 (paylaşılan cockpit / webview reuse).
Sonraki: V1 component primitives.
