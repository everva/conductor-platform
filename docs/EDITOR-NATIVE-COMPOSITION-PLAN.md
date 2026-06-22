# Conductor Editor — Native Composition & Conversational Intake Plan (Faz-Q)

Faz-P (tema + native diff + agentic tree) editör'ü "yabancı dark webview" olmaktan çıkardı; AMA
kullanıcı CANLI fork'ta derin bir yapısal eleştiri yaptı (2026-06-22): **"daha native olsun, web
uygulaması gibi değil"**. Bu plan o eleştirinin — cockpit'in TEK monolitik webview olmaktan çıkıp
**seçim-güdümlü, native-kompozisyonlu** bir agent-IDE'ye dönüşmesi.

> ⚠️ DİSİPLİN: UYDURMA YOK. Her madde gerçek koda + gerçek fork runtime'ına + kullanıcının canlı
> ekran-görüntülerine dayanır. Bu dosya YALNIZ PLAN — implementasyon kullanıcı onayından sonra.

## §0 — Bağlam
- Faz-P TAMAM (P1 tema · P2a/b/c native+full-file+multi-file diff · P3 agentic tree), hepsi CI-yeşil.
- Kullanıcı canlı demo'da (seeded gateway, web-shop + ios-app) gerçek UX boşlukları gördü. Bu plan onları
  kapatır. Aynı disiplin (N0–N5 / P1–P3): faz-faz, her faz Rule#9 + GERÇEK fork runtime + kullanıcı onayı.

## §1 — Kullanıcı feedback'i (canlı fork'u görünce, verbatim öz)
1. Sol "Conductors" ağacından **birini seçince aşağıdaki Fleet (ve diğer yüzeyler) seçilene göre güncellensin.**
2. **Event** ayrı bir pencere/yüzey olsun.
3. **Intake** ayrı bir pencere olsun.
4. **Daha native olsun, şu an web uygulaması gibi.**
5. **Intake Claude Code gibi olsun** (şu an çok basit). Claude Code OSS'tan öğren.

## §2 — EKSİKLER LİSTESİ (kod'a-dayalı; kullanıcının + benim bulduklarım)
**A. "Web app, native değil" (yapısal kök-neden):**
- TEK monolitik `web/src/fleet/FleetDashboard.tsx`: kendi `FleetStatusBar` + tabbar (board/fleet/events/
  intake) + `SessionView`, hepsi TEK React webview. **İKİ kez mount**: sidebar (`FleetViewProvider`) +
  Command Center (`CommandCenterPanel`). VS Code native yüzeyleri (editor-tab / Panel-area / ek TreeView /
  native StatusBar) tree dışında kullanılmıyor. Cockpit kendi chrome'unu çiziyor → web-app hissi.

**B. İki instance → GERÇEK tutarsızlık (BUG):**
- offline/live: `FleetStatusBar` `streamState` gösterir (`open`=live, `closed`=offline). Sidebar + CC AYRI
  mount → AYRI `useFleet`/`useEventStream` → AYRI WS bağlantısı. Ekran-görüntüsünde sol "● offline", sağ
  "● live" (aynı an, aynı gateway). İki bağımsız bağlantı + iki bağımsız seçim/state.
- Duplicate fleet-stat (PROJECTS/HOSTS/ACTIVE LEASES/LAST EVENT) iki yerde aynı anda.

**C. Seçim paylaşılmıyor (kullanıcı #1):**
- Seçim modeli `selectedProjectId` / `selectedTask` / `tab` = tek instance'ın iç React state'i
  (`FleetDashboard`). Native tree → CC'ye yalnız `navigateTo` ile **task→SessionView** deep-link eder.
  **Proje (Conductor) seçmek Fleet'i FİLTRELEMEZ** (`conductor.open` sadece CC reveal). Sidebar instance'ı
  CC'den bağımsız. "Seç → alt-yüzeyler güncellensin" YOK.

**D. Events gömülü (kullanıcı #2):**
- `events/EventStreamView.tsx` + `EventTicker.tsx` → monolit içinde "Events" TAB'ı. Kendi yüzeyi değil;
  seçimi takip etmiyor; native Panel-area (Output/Problems gibi) veya ayrı editor-tab değil.

**E. Intake gömülü + tek-atış form (kullanıcı #3 + #5):**
- `intake/IntakeChat.tsx` adına rağmen **tek-atış form**: proje dropdown + büyük textarea → **Distill**
  (POST `/distill`, tek `conversation` string) → scenario kartları + YAML editörü → Approve. **Çok-turlu
  değil, streaming yok, clarifying-question yok, plan-preview yok.** Ayrı yüzey de değil (tab). Kullanıcının
  "çok basit / Claude Code gibi değil" tespiti doğru.

**F. Session view GEÇMİŞ backfill etmiyor (BUG):**
- I-3 *done* iken SPEC "Loading spec…", ACTIVITY "No activity yet", VERDICT "Awaiting the gate…", DIFF
  "No diff yet". `SessionView` canlı-veriye dayanıyor; bitmiş task'ın geçmişini (scenario + events + verdict
  + diff) backfill etmiyor → review İMKÂNSIZ. (`/events?kind=…` backfill endpoint'i + `/scenarios` VAR; verdict/
  diff geçmiş event'lerden gelmeli — bağlanmıyor.)

**G. Diğer native-uyum (benim):**
- Cockpit kendi ⌘K paleti (`fleet/CommandPalette.tsx`) VS Code native ⌘⇧P ile ikilik/çakışma.
- "needs review" badge + "Review →" iki yerde (sidebar + CC).
- "New work" üç yerden (board + intake tab + palette) aynı basit tab'a → native "yeni iş" akışı yok.
- Native "Hosts" / "Leases" yüzeyi yok (sadece webview stat); host_id boş görünüyor (registry/lease native değil).

## §3 — Hedef mimari: native kompozisyon + paylaşılan seçim
**Dürüst "native" tanımı:** webview'ler kalır (zengin board/session/diff React içeriği için doğru araç);
ama **YERLEŞİM + GEZİNME + chrome + seçim/bağlantı state'i native olur.** Monolit, TEK host-sahipli bir
**seçim modeline** bağlı, doğru native konumlardaki **amaç-yüzeylere** ayrışır:
- **Sidebar (driver):** native "Conductors" tree (var) — seçimin TEK kaynağı. (+ ops. native "Hosts" tree.)
- **Editor-alanı (Command Center):** board (overview) VEYA seçili session (detail + native diff split, var).
- **Panel-alanı (alt, Output/Terminal gibi):** **Events** — seçimi takip eden ayrı "Conductor Events" yüzeyi.
- **Editor-tab:** **Intake** — kendi sekmesi, Claude-Code-tarzı konuşmalı.
- **Native StatusBar:** fleet-stat (projects/hosts/leases/last-event) + bağlantı — TEK kaynak (duplicate'i
  ve offline/live bug'ını çözer).
- **TEK host-sahipli bağlantı + event state'i;** yüzeyler abone. **TEK host-sahipli seçim;** yüzeyler projeksiyon.

## §4 — Fazlar (her biri ayrı; Rule#9 + GERÇEK fork runtime + kullanıcı onayı)
- **Q0 — Paylaşılan seçim + tek bağlantı (temel).** Host-sahipli seçim state'i (selected project/task) + bir
  kontrol kanalı (N3 `navigate-session` kanalını genelleştir: `select-project`/`select-task`) → yüzeyler tepki
  verir. Bağlantı/event state'ini TEKİLLEŞTİR (sidebar+CC tek mantıksal akış) → offline/live bug FIX. ADR.
- **Q1 — Seçim-güdümlü yüzeyler (kullanıcı #1).** Tree'de **proje** seç → board O projeye filtrelenir + CC o
  projeyi gösterir; **task** seç → session (var, genelleştir). Seçim tüm yüzeylere yayınlanır. Sidebar webview
  cockpit'i incelt/kaldır (native tree + CC yeter). Native diff/agentic korunur.
- **Q2 — Events ayrı native yüzey (kullanıcı #2).** Events'i monolitten çıkar → Panel-area webview "Conductor
  Events" (veya native TreeView), **seçime göre kapsamlı** (proje/task). `/events` backfill + canlı WS birleşik.
  Native yerleşim (alt panel). ADR.
- **Q3 — Intake ayrı yüzey + Claude-Code-tarzı konuşmalı (kullanıcı #3+#5).** Ayrı editor-tab. Konuşmalı:
  çok-turlu mesaj geçmişi + streaming + clarifying-question + **plan-preview** (ExitPlanMode-benzeri onaylanabilir
  scenario planı) → Approve→dispatch. YAML-authoritative + never-fabricate korunur. **Backend:** çok-turlu
  `/distill` (conversation HISTORY) + ops. streaming (SSE/WS). Frozen-additive. (§5 detay.) En büyük faz.
- **Q4 — Native-feel pass + bug fix.** Cockpit instance de-dup; **native StatusBar** fleet-stat + TEK bağlantı
  state; **session-view geçmiş backfill** (scenario/events/verdict/diff — done task review FIX); native empty/
  loading state; ⌘K paleti ya native ⌘⇧P'ye devret ya da kapsamla; review-badge de-dup; ops. native Hosts tree.
- **Q5 — Capstone.** Gerçek fork runtime + kullanıcı görsel onayı (Devin/native referansıyla).

## §5 — Claude-Code-tarzı Intake (anthropics/claude-code'u KLONLAYIP inceleyerek; `plugins/feature-dev`)
**GERÇEK KAYNAK:** CC'nin "Guided feature development" komutu (`plugins/feature-dev/commands/feature-dev.md`)
7-fazlı bir **KONUŞMA + ONAY** döngüsü; intake'in tam analoğu:
1. **Discovery** — hedefi anla; belirsizse SOR (problem? ne yapmalı? kısıt?); özetle + onayla.
2. **Exploration** — paralel `code-explorer` agent'ları kod tabanını file:line referanslı anlar.
3. **Clarifying Questions** (dosyada "**CRITICAL · DO NOT SKIP**") — tüm belirsizlik/edge-case/scope'u net
   soru-listesi olarak sun, **cevabı BEKLE, VARSAYMA**; "sen bilirsin" → öneri ver + AÇIK onay al.
4. **Architecture Design** — 2-3 `code-architect` yaklaşımı (minimal / clean / pragmatik trade-off), **öner**,
   "hangisini istersin?" SOR.
5. **Implementation** — "**DO NOT START WITHOUT USER APPROVAL**"; açık onay bekle; TodoWrite ilerlet.
6. **Quality Review** — paralel `code-reviewer` agent'ları; bulguları sun, ne yapsın diye SOR.
7. **Summary** — ne yapıldı / kararlar / sonraki adımlar.
Çekirdek ilkeler (komut + agent frontmatter'ından): **erken-sor → bekle → varsayma** · **öner + AÇIK onay** ·
TodoWrite progress · streaming satır-satır · agent persona deseni (`agents/*.md` frontmatter `name/description/
tools/model`; `commands/*.md` frontmatter `description/argument-hint` + `$ARGUMENTS`).

**Conductor intake'e uyarlama** (mevcut tek-atış `converse→distill→review→approve`'u CC-grade'e çıkar):
- **Discovery + Clarifying = çok-turlu CHAT (tek-atış textarea DEĞİL):** director hedefi yazar → distiller
  EKSİKSE net SORULAR sorar (lane? tier? acceptance? holdout? edge-case?) → **cevap BEKLER** → netleşince önerir.
  Conductor'ın **never-fabricate**'i (422→"add more detail" guidance) tam da CC'nin "varsayma, sor" ilkesi.
- **Plan-preview = feature-dev "Architecture Design" / CC plan-mode:** önerilen scenario'lar **onaylanabilir PLAN**
  (öneri + gerekçe, düzenlenebilir kart/YAML) → **Approve→dispatch** (POST `/intake`; "DO NOT START WITHOUT
  APPROVAL" = mevcut confirm-gate). **YAML authoritative KORUNUR.**
- **Conductor verifier/gate = feature-dev "Quality Review"** analoğu (dispatch SONRASI; zaten var).
- **Progress + streaming:** TodoWrite-tarzı faz çubuğu; distillasyon satır-satır akar (CHANGELOG: "text appears
  line-by-line").
- **Slash + agent kancası:** `/spec` (var), `/lane`, `/tier`, `/holdout`; ops. repo-keşif agent'ı (`code-explorer`
  deseni) → distiller hedef repo'yu file:line ile anlayıp daha iyi scenario önerir.
- **Backend (frozen-additive):** çok-turlu `/distill` (tek string yerine conversation HISTORY + bir clarifying-
  question turu döndürebilir) + ops. streaming (SSE/WS). Mevcut tek-atış `/distill` FALLBACK. Sahneli:
  **Q3a** chat-UI (mevcut distill üstüne) → **Q3b** çok-turlu backend + clarifying → **Q3c** streaming + repo-keşif.

## §6 — Disiplin (DEĞİŞMEZ; N0–N5 / P1–P3 ile aynı)
Her faz: spec → kodla → **Rule#9** (editör gate: typecheck×2 + eslint-0 + vitest + esbuild; paylaşılan cockpit'e
dokununca **web gate** + **Playwright e2e** `:5173` KENDİM) **+ GERÇEK fork runtime** (electron smoke + kullanıcı
ekran-görüntüsü; yüzey fork runtime'ı). **Frozen-additive (ADR-0021):** gateway/Go additive (Q0 kontrol-kanalı,
Q2 events, Q3 çok-turlu distill). **Token DONMUŞ.** **CSS-foundation + cross-dir-dep gotcha'ları.** CI 3-job
`gh run view --json conclusion`. Yeni ADR'ler. Canlı optiway/xirigo'ya DOKUNMA. worktree YOK. **UYDURMA YOK.**

## §7 — Açık kararlar (kullanıcıya; implementasyondan ÖNCE)
1. **Events yeri:** Panel-area (alt, Output gibi) **VS** ayrı editor-tab **VS** native TreeView? (öneri: Panel-area webview.)
2. **Native derinliği:** board/session/diff webview kalsın (öneri) **VS** daha çok native widget?
3. **Sidebar cockpit:** tamamen kaldır (native tree + CC) **VS** inceltilmiş kalsın?
4. **Intake kapsamı:** tam konuşmalı+streaming (büyük, backend) **VS** sahneli (Q3a önce)?
5. **⌘K:** cockpit paleti kalsın **VS** native ⌘⇧P'ye devret?
6. **Faz sırası:** öneri Q0→Q1 (seçim, en görünür) → Q4-bugfix (offline/live + session backfill, hızlı kazanç) →
   Q2 (events) → Q3 (intake, en büyük). VEYA kullanıcı önceliği.

## §8 — Kaynaklar (Claude Code OSS / UX)
- **anthropics/claude-code (klonlandı + incelendi):** `plugins/feature-dev/commands/feature-dev.md` (7-fazlı
  guided-dev konuşma+onay döngüsü = intake analoğu) · `plugins/feature-dev/agents/{code-explorer,code-architect,
  code-reviewer}.md` (persona frontmatter deseni) · `.claude/commands/*.md` (slash-komut formatı) · `CHANGELOG.md`
  (streaming satır-satır · AskUserQuestion clarify+preview+multi-select+Other · plan-mode/opusplan · `/config`).
- Docs/üçüncü-taraf: code.claude.com/docs/en/cli-reference · github.com/anthropics/claude-code ·
  shipyard.build/blog/claude-code-cheat-sheet · introl.com/blog/claude-code-cli-comprehensive-guide-2025

## DURUM (2026-06-22) — PLAN HAZIR (implementasyon yok; kullanıcı onayı bekleniyor)
- Faz-P CANLI + CI-yeşil. Bu plan (Faz-Q) kullanıcının native-kompozisyon eleştirisini kapatır.
- SIRADAKİ: kullanıcı §7 kararları + faz seçimi → seçilen fazdan başla (aynı disiplin).
