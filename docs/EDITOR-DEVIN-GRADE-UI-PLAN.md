# Conductor Editor — Devin-grade UI/UX elevation plan (3/10 → 9/10)

Kullanıcı (2026-06-21): cockpit **işlevsel ama görsel zanaat amatör — 3/10**; hedef
**Devin.ai seviyesi ≥9/10**. Bu plan FEATURE değil **VISUAL CRAFT** overhaul'u:
mevcut yüzeyleri (board · session · intake · ⌘K · needs-review · shell) aynen
korur, ama ad-hoc CSS'ten **design-system-öncelikli**, motion'lı, durum-zengini,
erişilebilir bir görsel dile taşır. (Agent-native FEATURE planı ayrı:
`EDITOR-AGENT-NATIVE-REDESIGN-PLAN.md` — E1–E4 bitti; bu onun üstüne CİLA.)

## §0 — Dürüst boşluk analizi (neden 3/10)
Mevcut `web/src/**/*.css` (board/session/intake/fleet/index) elle yazılmış,
dağınık literal'ler. Somut eksikler:
- **Design system YOK**: renk/spacing/type token'ı yok → tutarsızlık; "bootstrap
  dark theme" hissi. Aksан indigo→violet düz gri üstünde.
- **Tipografi zayıf**: sistem fontu, type-scale yok, hiyerarşi/tracking tunesiz.
- **Renk**: gerçek nötr rampa + semantic sistem + kısıtlılık yok.
- **Ritim**: 4/8px grid yok; padding/density/alignment göz kararı.
- **Derinlik**: düz paneller + ince border; katmanlı surface + rafine shadow yok.
- **Motion YOK**: transition/easing/list-animasyon yok → cansız.
- **Durumlar eksik**: skeleton/loading-shimmer, düşünülmüş empty/error state yok.
- **Component katmanı yok**: buton/input/card elle, tutarsız, a11y zayıf.
- **İkonografi**: emoji/metin glyph (◂ ▸ ●) — gerçek icon seti (lucide) yok.
- **Detay**: focus-ring, scrollbar, hover/active craft, tooltip cilasız.

## §1 — Devin-grade ilkeleri (9/10 dili)
Refined tipografi (premium font + type-scale + tracking) · gerçek nötr renk rampası
+ kısıtlı semantic aksan · 8px ritim + hizalama · katmanlı elevation + yumuşak
shadow · her durum (loading/empty/error/skeleton) · akıcı motion (easing eğrileri,
list enter/leave, state geçişleri) · tutarlı erişilebilir component'ler · gerçek
icon seti · focus/contrast/keyboard hijyeni · "az ama mükemmel" detay.

## §2 — Yaklaşım: design-system-ÖNCE, sonra yeniden-skin
Çöpe atma yok — yüzeyler + davranış aynı; YALNIZ görsel katman token-driven
yeniden örülür. Önce temel (token+component), sonra her yüzey o temele geçer.

## §3 — Fazlar
- **V0 — Görsel temel** (`web/src/theme/`): tek `tokens.css` — renk rampası
  (nötr 50→950 + semantic success/warn/danger/info + brand restraint), type-scale
  (premium font: Geist/Inter; size/weight/line-height/tracking), space (4/8),
  radii, shadow/elevation katmanları, motion (duration + easing). Fontu yükle
  (self-host, CSP-uyumlu). 1-sayfa "Conductor visual language". ADR.
- **V1 — Component primitives** (`web/src/ui/`): küçük tutarlı a11y katman —
  Button/IconButton, Input/Select, Card, Badge/Chip, Panel, **Modal/Dialog**,
  **Tooltip**, Tabs/Segmented, **Skeleton**, Toast — token'lar üstünde. KARAR:
  a11y-kritik primitives için Radix (Dialog/Tooltip/Dropdown/Popover) değerlendir;
  CSS yaklaşımı plain/module kalır ama token-driven. Ad-hoc CSS'i buraya çek.
- **V2 — Shell + board**: app-bar/nav + Command Center board'u sisteme geçir —
  density, hiyerarşi, kart craft (status semantic, canlı pulse, hover-lift),
  empty/loading state. İlk "9/10 mu?" ekran-görüntüsü öz-değerlendirmesi.
- **V3 — Session + intake + ⌘K**: session detail (verdict-hero, inline diff,
  timeline/replay), intake (spec editor), komut paleti — premium craft +
  micro-interaction. needs-review sinyali rafine.
- **V4 — Motion + delight + icons**: surface/list/state transition'ları (CSS veya
  framer-motion-lite), skeleton shimmer, focus craft, rafine scrollbar, tooltip,
  **lucide** ikon seti (emoji/glyph'leri değiştir).
- **V5 — 9/10'a iterate**: Devin referans ekran-görüntüleriyle YAN YANA; her yüzey
  için öz-eleştiri → gerçek 9/10 okuyana kadar iterate. Dürüst skorlama; kullanıcı
  onayı = bitiş.

## §4 — 9/10 bar (kabul)
Refined type · 8px ritim · semantic renk · katmanlı elevation · akıcı motion · TÜM
durumlar · tutarlı component'ler · gerçek icon · a11y (focus/contrast/keyboard) —
ve **ekran-görüntüsü incelemesinde "Devin gibi" diyebilmem + kullanıcının onayı**.
Her yüzey için before/after + öz-skor.

## §5 — Disiplin (DEĞİŞMEZ)
Her faz: kısa spec → kodla → **Rule#9 BAĞIMSIZ doğrula** (tsc+eslint+vitest +
**Playwright CANLI KENDİM** :5173'e karşı + ekran-görüntüsü öz-incelemesi + dürüst
skor; yeni test sonrası lint'i TEKRAR koş) → push develop(+fork main fork ise) →
ledger+memory → CI 3-job yeşil. Çoğunlukla **web-only** (görsel; backend/frozen
kontrat DEĞİŞMEZ — ADR-0021). Editör webview cockpit'i reuse eder (cross-dir
alias). Token disiplini (webview/log/test'e ASLA). Canlı optiway/xirigo'ya DOKUNMA.
isolation:"worktree" KULLANMA. NOT: `claude -p` bu makinede ÇALIŞIYOR (bu oturumda
kanıtlandı) ama UI işi claude-free.

## DURUM (2026-06-21) — V0 TAMAM, SIRADAKİ V1
- Önkoşul: agent-native FEATURE planı E1–E4 ✅; k8s GitOps deploy ✅ (ayrı iş).
  Cockpit `:5173` dev + seeded gateway `:8080` AYAKTA (görsel doğrulama yüzeyi).
- **V0 ✅ (develop `6f26a1f`)**: `web/src/theme/tokens.css` tek kanonik token
  katmanı (nötr rampa 50→950 + semantic surface/text/border + brand indigo→violet
  + status-quad + 8-adım type-scale + 4/8 space + radii + elevation + focus-ring +
  motion[reduced-motion] + z-index). Premium tip **Geist Variable** sans + mono
  `@fontsource` ile **self-host** (CSP-temiz). main.tsx wiring + index.css base
  token-driven + tek `:focus-visible` ring + `::selection`. **ADR-0035**. Canlı
  Playwright KENDİM doğruladı (before/after board + signin; `document.fonts.check
  ('Geist Variable')=true`, body/label=Geist, --bg/--brand çözülüyor, 0 console
  error). Gate: tsc+eslint+vitest 135/135. NOT: V0 görünür deltası KASITLI ufak
  (yalnız tip+base); yüzey re-skin V2+. Board hâlâ ~3.5/10 — TEMEL kuruldu.
- **SIRADAKİ: V1 component primitives** (`web/src/ui/` Button/IconButton/Input/
  Select/Card/Badge/Panel/Dialog/Tooltip/Tabs/Segmented/Skeleton/Toast; a11y-
  kritik için Radix değerlendir; token-driven plain CSS; ad-hoc CSS'i buraya çek)
  → V2 board → V3 session/intake/⌘K → V4 motion+lucide → V5 iterate-to-9. Her
  faz Playwright-canlı KENDİM.
