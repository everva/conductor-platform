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

## DURUM (2026-06-22) — V0→V4 TAMAM, SIRADAKİ V5 (iterate-to-9 + kullanıcı onayı)
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
- **V1 ✅ (develop `d1c8e74`)**: `web/src/ui/` token-driven primitive katmanı —
  Button (5 variant × 2 size + loading + icon slots) · IconButton (zorunlu a11y
  label) · Badge (6 semantic ton) · Chip (neutral/mono) · Card (interactive
  hover-lift / selected ring / status accent rail) · Panel (opsiyonel header) ·
  Skeleton (shimmer, reduced-motion) · StatusDot (ton + live pulse). Barrel
  `index.ts` (ui.css yükler). Boundary-lock (ADR-0028) `src/ui/**` eklendi.
  **Dev gallery** `src/dev/ui-gallery.tsx` + `ui.html` (dev'de `/ui.html`; prod
  build'de DEĞİL) = her re-skin dalgasının screenshot yüzeyi. Canlı doğruladım
  (Playwright `/ui.html`, Geist yüklü, 0 err; primitives izole ~8/10 — in-context
  tuning V2). Gate: tsc+eslint(0 warn)+vitest 151/151 (16 yeni). Henüz yüzey
  re-skin YOK (V2). Overlay primitives (Dialog/Tooltip/Tabs/Toast/Select + Radix
  değerlendirmesi) tüketen dalgada (V3) gelecek — KASITLI scoping.
- **V2 ✅ (develop `f8a6db7`)**: shell + board re-skin = İLK gerçek görsel sıçrama.
  Board (`CommandCenter.tsx`+`board.css`): kartlar→`<Card>` (gradient surface +
  hover-lift + selected brand-ring + lifecycle accent-rail), tier→`<Badge>`,
  lane/host→`<Chip>`+`<StatusDot pulse>`, ID'ler Geist Mono, Approve→`<Button
  success>`, Review→`<Button>`, New work→`<Button primary>`+icon; board.css
  token-only layout'a indi (yerel `--cc-*` blok + kart/buton/badge/dot CSS emekli).
  Tüm test-hook'ları korundu (role=button kartlar, `.cc-stat`, checkbox aria,
  "Bulk actions" region, buton adları). Shell: app-bar+Sign-out→`<Button ghost>`,
  FleetStatusBar conn→`<StatusDot>`+Refresh→`<Button>`, segmented tabs+⌘K trigger
  token, NeedsReviewBadge→`<StatusDot warn pulse>`. **DÜRÜST skor board ~7.5/10**
  (3.5 baseline'dan). Canlı KENDİM (before/after; Geist+Mono yüklü, 0 err). Gate:
  tsc+eslint+vitest 151/151+**Playwright e2e 14/14** (intake "+ New work"→"New
  work" selector; + artık dekoratif aria-hidden icon). NOT: Fleet/Events tab
  detay CSS'i (panel/tablo/notice/intervention/ticker/modal `fleet.css`) board
  yüzeyinde DEĞİL → o yüzeylerle birlikte sonra re-skin (literal kaldı, dürüst).
- **V3 ✅ (develop `1926033`)**: tüm kalan yüzeyler sisteme geçti = bütün cockpit
  tek tutarlı premium dil. SessionView (ghost back + Badge/Chip header + verdict-
  hero + timeline-replay + inline-diff token; Approve→`<Button success>`/Abort→
  `<Button danger>` — V2'nin `.cc-btn` regresyonunu DÜZELTTİ; `--sv-*` emekli).
  ConfirmDialog→`<Button>` (forwardRef focus). ⌘K CommandPalette (palette.css)
  token spotlight (Raycast-tarzı). IntakeChat (intake.css) + **paylaşılan fleet.css
  TÜM sınıfları** (panel/btn/chip/badge/muted/mono/modal/notice/table/intervention/
  ticker) token-migrate → Fleet/Events detay component'leri de tek geçişte yükseldi.
  Divergent literal KALMADI. **Radix: DEĞERLENDİRİLDİ, ALINMADI** (mevcut overlay'ler
  el-yapımı ama TEST'li + erişilebilir: role=dialog/aria-modal/Escape/focus-mgmt,
  role=listbox/option; Radix migrasyonu marjinal kazanç için büyük churn — a11y
  boşluğu çıkarsa revisit). Canlı KENDİM (session/palette/intake/confirm hepsi
  premium, 0 err). Gate: tsc+eslint+vitest 151/151+**e2e 14/14** (intake selector
  fix dahil).
- **V4 ✅ (develop `0bb4f54`)**: delight pass. **lucide-react** (tree-shaken) TÜM
  emoji/glyph/inline-SVG'leri değiştirdi — tab ikonları (LayoutGrid/Server/Activity/
  Inbox), ⌘K Search, New work Plus, Review ChevronRight, session back ChevronLeft,
  verdict gerçek Check/X, needs-review+"View on board" ArrowRight, notice dismiss X;
  .fleet-btn/.fleet-tab/dismiss inline-flex (ikon+metin hizası). **Motion** (token
  süreli→reduced-motion sıfırlar): board kartları ui-card-in mount'ta (stable key→
  her refresh'te değil), session+intake ui-surface-in. **States**: board initial-load
  `<Skeleton>` (fleet.loading→yalnız yükleniyor+boşken) + rafine themed scrollbar.
  Canlı KENDİM (ikonlar crisp, tablar scannable, 0 err). Gate: tsc+eslint+vitest
  151/151+**e2e 14/14** (tab'lar map'e refactor; a11y adları korundu).
- **SIRADAKİ: V5 iterate-to-9** — gerçek Devin.ai UI ekran-görüntüleriyle YAN YANA;
  her yüzey için öz-eleştiri → kalan boşlukları kapat (density/balance, empty-state
  craft, tooltip, hover/active ince ayar) → DÜRÜST 9/10 okuyana kadar iterate.
  **Bitiş = ekran-görüntüsünde "Devin gibi" diyebilmem + KULLANICI ONAYI.** Her
  iterasyon Playwright-canlı KENDİM. Cockpit şu an ~8.5/10.
