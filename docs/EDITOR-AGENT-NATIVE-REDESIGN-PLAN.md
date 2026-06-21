# Conductor Editor → Agent-Native "Command Center" redesign — PLAN

**Sorun (kullanıcı):** mevcut editör "VS Code + yan panelde Conductor cockpit" — Devin/Augment
gibi **agent-native** değil, kullanımı kötü. **Tez:** Conductor'ın **backend'i bu işi zaten
yapıyor** (hatta rakiplerden daha disiplinli: deterministik gate = TEK merge mercii); eksik olan
SADECE editörün **çerçevesi**. Mevcut editör, sektörün 2025–26'da terk ettiği eski modelde. Bu
plan editörü "yan panel"den **agent komuta merkezi** (default surface) modeline geçirir.

> Disiplin DEĞİŞMEZ: frozen-additive (engine/statestore/EventBus imza-sabit, ADR-0021);
> deterministik-gate TEK merge mercii; token yalnız SecretStorage+host header/WS URL (webview'e
> ASLA); webview-first (thin-overlay ADR-0031 korunur); her iş Rule#9 + CI 3-job yeşil +
> Playwright/gerçek-koşu KENDİM doğrularım.

---

## DURUM (2026-06-21) — E1+E2+E3+shell+E4-b/c/d TAMAM, sıradaki E4 (çoklu-seçim + fork-runtime)

**develop @ `cf857c3` (CI 3-job yeşil).** Hepsi non-breaking, çoğu web-only (B1/B2 hariç additive Go),
Playwright-CANLI KENDİM doğrulandı (Devin-grade):
- **E1** Command Center board (default Kanban yüzey) + **görsel-polish** (`50b1c55`/`a764782`).
- **E2** premium Session view drill-in (`2b67bbf` backend + `e1bfc4c` web): SPEC/acceptance +
  **Verifier-verdict hero** (B1/ADR-0033: conductor `KindDecision`'a bounded per-gate checks emit —
  checks artık ATILMIYOR) + inline renkli diff + activity timeline + Approve. **B2/ADR-0034**: gateway
  `GET /projects/{id}/scenarios`.
- **E4-a shell-polish** (`f4d0d28`): app-bar (indigo→violet marka hub-glyph) + segmented-control tab'lar
  + slim telemetri çubuğu — tüm kabuk Devin-grade.
- **E3** spec-first intake (`d3ebc53`): claude-FREE **"Write spec directly"** (YAML editor template →
  Approve → /intake dispatch) + "View on board →"; **CANLI loop kanıtlı** (LIVE-1 yazıldı → /intake 200 →
  board READY).
- **E4-b ⌘K komut paleti** (`d438680`, web-only, non-breaking): klavye-öncelikli overlay — herhangi bir
  projedeki task'ın session'ına atla / yüzey değiştir / yeni iş başlat. ⌘K/Ctrl+K (global) veya tab-bar
  trigger; yaz→filtrele (AND token, id/proje/lane/status), ↑/↓, Enter, Esc. Saf model (`palette.ts`) +
  ince view (`CommandPalette.tsx`); FleetDashboard ⌘K'yı bağlar + seçimi mevcut setTab/setSelectedTask
  seam'lerine eşler (B3 client-side, gateway dokunulmadı). Rule#9: tsc+eslint+120 vitest+11 e2e + **CANLI
  Playwright gerçek gateway** (⌘K gerçek cp_view session'ları, lane-filtre, LIVE-1 drill-in — KENDİM).
- **E4-c needs-review sinyali** (`a895a69`, web-only): kabuk-seviyesi kalıcı rozet — HER yüzeyde (board, tab,
  session içi) görünür; Needs-Review lane'indeki (awaiting-approval+blocked) task sayısını gösterir + tek-tıkla
  review kuyruğuna (board) götürür. Boşken hiç render etmez (sinyal, gürültü değil); `role="status"`+aria-live
  ile erişilebilir nudge. Sayı `needsReviewCount` = board'un AYNI leaseHostByTask+columnFor bucketing'i (leased/
  running task şişirmez → board kolonuyla asla çelişmez). Rule#9: tsc+eslint+127 vitest+12 e2e + **CANLI gerçek
  gateway** ("2 tasks need your review" board ile eşleşti, Events'te kalıcı, tek-tık döndü — KENDİM).
- **E4-d replay/timeline** (`cf857c3`, web-only): session activity timeline'ı replay log'una yükseltti — her
  entry payload-özeti taşır ("2 files +74/−2", "pass", "42%") + SEÇİLEBİLİR: tıklayınca Verifier-verdict + diff
  panelleri O ANIN durumuna PİNLENİR (gate karar vermemiş→changes-requested→pass; diff büyürken scrub). "Return
  to live" + "replaying as of …" notu; paneller "replay" tag'ler. Saf+geriye-uyumlu: `parseVerdict`/`parseDiff`
  opsiyonel `asOf` ts-cutoff (yoksa=latest), `buildTimeline` per-entry summary (gateway/Go dokunulmadı). Rule#9:
  tsc+eslint+131 vitest (asOf-replay pre-decision-null + summary + component scrub) +13 e2e (timeline replay) +
  **CANLI gerçek gateway** (I-1 timeline scrub: verdict/diff as-of'a pinlendi, return-to-live, 0 konsol hatası — KENDİM).

**SIRADAKİ — E4 kalan director güçleri:** (1) kullanıcı tüm akışı gezer; (2) **çoklu-seçim** (board'dan toplu
approve/abort — web, hassas: bulk-onay), **"take over"** → native editör/diff aç (fork-runtime), 4C-3 native toast
yükselt (editör-runtime). Sonra E5 (ACP interop + standalone). Web-verifiable kalan = çoklu-seçim; gerisi fork-runtime.

**SERT KISIT:** bu sandbox'ta `claude` subprocess auth YOK → assisted distill (yazışma→senaryo) + real-claude
develop BURADA koşamaz; UI'lar mock/seed ile, gerçek-claude kullanıcının host'unda. E3 direct-path bu yüzden eklendi
(claude'suz tam-kullanılır).

**Canlı demo stack** (gez/doğrula için): gateway `/tmp/cockpit-demo/conductor-api` `:8080` (schema `cp_view`,
token `cockpit-demo-token-2026`) + web dev `:5173` (Vite proxy→:8080). Kapanmışsa: B2-rebuild'li gateway'i
yeniden başlat (`go build -o /tmp/cockpit-demo/conductor-api ./cmd/conductor-api`; `CONDUCTOR_API_TOKEN=… -addr :8080
-dsn '…search_path=cp_view'`) + gerekirse cp_view seed (projects/tasks/hosts/leases + I-1 scenario+events) +
`cd web && npm run dev`.

---

## 0. REVİZYON v2 — kod incelemesiyle doğrulanan boşluklar + net kararlar

Planı kesinleştirmeden ÖNCE kritik varsayımları gerçek kodla sınadım. Bulgu + karar:

**B1 — Verifier verdict GÖZLEMLENEBİLİR DEĞİL (fark yaratıcımızın ön-koşulu).** Conductor
`PhaseReview/KindDecision` event'i yalnız `{"result": pass|changes-requested}` taşıyor; per-gate
CHECK detayı (go build ✓ / go test ✓ / holdout ✓ + evidence) HİÇBİR event payload'ında YOK
(`engine.ReviewResult.Findings`+`checks` yalnız `TickResult`'ta, daemon-içi kalıyor;
`conductor.go:880`). → **KARAR (E2 backend additive, frozen-safe):** conductor `KindDecision`
payload'ına BOUNDED `checks:[{name,result,evidence}]` ekler (evidence zaten "ilk-satır-capped";
KindDiff'in ~8KB PG-NOTIFY bütçesi gibi sınırlı). `events.Event` ZARFI imza-sabit kalır; payload
`map[string]any` = additive. Bu, session view'in **"Verifier verdict"** panelinin TEK backend dokunuşu.

**B2 — Spec/acceptance gateway'de YOK.** `taskDTO` `ScenarioID` taşıyor ama scenario'nun
title/acceptance'ı hiçbir GET'te yok (scenario yalnız intake/distill ile YAZILIYOR). → **KARAR
(additive read):** `GET /projects/{id}/scenarios` (veya E2 session-aggregation scenario'yu içersin).
Frozen StateStore'da scenario okuma var; yalnız gateway projeksiyonu eklenir.

**B3 — Board için tüm-task aggregate YOK** (yalnız `GET /projects/{id}/tasks`). → **KARAR:**
başlangıçta **client-side per-project fan-out** (küçük N: /projects→her biri /tasks); ölçek gerekirse
**additive `GET /tasks`**. Kart "canlı faz" (developing/verifying/reviewing) **event feed**'den
türetilir (Phase{Develop,Verify,Review}/Started + KindDiff). "Needs-review" = `KindInterventionNeeded`
(4C-3 kanalı, `conductor.go:906` "human approval required") + awaiting-approval/blocked. Hepsi MEVCUT akıştan.

**B4 — "Default surface" thin-overlay'de MÜMKÜN (deep-fork YOK).** editör `onStartupFinished`
aktive; mevcut UI activity-bar WebviewView (`conductor.fleet`). → **KARAR:** command center =
`onStartupFinished`'da editör-alanında açılan **singleton WebviewPanel** (sekme), cockpit bundle
reuse; Code-OSS workbench'e PATCH YOK (ADR-0031 korunur). Activity-bar girişi = hızlı-aç. Risk:
her açılışta sekme-spam → tek-panel (singleton) + kapatılabilir "varsayılan-aç" ayarı.

**B5 — Conductor'da CANLI shell/browser YOK (Devin'den KASITLI sapma).** agent (`claude -p`)
HOST'ta (daemon) headless koşar; editör event+diff ile GÖZLER. → **KARAR (dürüst):** Conductor
"follow-along" = **event timeline + native diff + gate verdict** (Devin'in canlı shell/browser
pane'i DEĞİL). Daha temiz (host-side izolasyon) + yeterli; gelecekte develop-stdout streaming
(backend additive) opsiyonel, E5+.

**B6 — TEK UI (çakışma yok) + ilk-açılış.** command center mevcut activity-bar panelini SOĞURUR
(board=ana yüzey; Hosts/Events/Intake board içinde sekme/bölüm); iki rakip UI olmaz. Bağlantısız/
ilk-açılış = temiz "connect to gateway" ekranı (4B-1 connect akışı reuse), hata-ekranı değil.

**Özet:** redesign'ın gerektirdiği backend dokunuşları yalnız **2 additive** (B1 gate-checks event +
B2 scenario read; her ikisi frozen-safe projeksiyon/payload). Geri kalan TAMAMEN webview (`web/`+
`editor/`). "Default surface" deep-fork gerektirmiyor. Differansiyatör (gate verdict) B1'e bağlı.

---

## 1. Araştırma — sektör neye yakınsadı (2025–2026)

Üç lider agent-native ürün, AYNI modele yakınsadı:

- **Devin Desktop (eski Windsurf 2.0; Cognition, Haziran 2026):** "an agent manager wrapped in a
  full IDE." **Agent Command Center** artık opsiyonel panel değil, **DEFAULT yüzey** — agent
  oturumlarının durum-bazlı **Kanban** görünümü. **Spaces** = ilişkili oturum/PR/dosya/context
  gruplama. Local + cloud agent'lar **tek görünümde** (köken ikincil; ne yaptığı önemli). Diff
  review **aynı yüzeyde**. ACP (Agent Client Protocol) = "agent'lar için LSP" interop.
- **Augment Intent ("a workspace for agent orchestration"):** her oturum kendi **git-worktree**
  izole workspace'i; "agents, terminals, diffs, browsers, git ops tek workspace'te." 3 rol:
  **Coordinator** (planı **"living spec"** olarak önerir) → insan onayı → **Implementor**'lar
  **paralel dalgalar**da yürütür → **Verifier** spec'e karşı doğrular → insan review.
- **Devin (cloud):** tek oturum = **plan + chat + shell + editor + browser**, planner görevi
  adımlara böler, **gerçek-zamanlı "follow-along" + "take over"**, **replay timeline** (ne
  değişti, hangi sırada, neden — audit trail).
- **Google Antigravity:** agent-first VS Code fork; iki yüzey: **Editor** + **Manager** (mission
  control). 2.0'da Agent Manager **IDE'den bağımsız standalone** uygulama oldu.

**Yakınsamış 10 desen:**
1. **Default yüzey = Agent Command Center** (editör/chat değil, agent board'u ana ekran).
2. **Kanban / durum-bazlı board** (queued → running → needs-review → done/blocked).
3. **Spaces** (ilişkili işleri grupla, context paylaş).
4. **Local + cloud agent tek birleşik görünüm** (köken ikincil).
5. **Director modeli** (insan = yönetmen/orkestratör, baş-yazıcı değil).
6. **Oturuma drill-in = çok-pane** (plan + steer-chat + activity + diff + verdict).
7. **Living spec** (Coordinator önerir → insan onaylar → paralel yürütme → Verifier doğrular).
8. **Review/approve aynı yüzeyde** (built-in diff + approve/merge, harici araç yok).
9. **Replay / audit timeline** (tam izlenebilirlik).
10. **Async + bildirim** (arka planda koş, dikkat/review gerektiğinde haber ver).

Kaynaklar §11.

---

## 2. Conductor ZATEN agent-native (eşleme) — backend hazır, eksik = UX çerçevesi

| Yakınsamış desen | Conductor'da MEVCUT primitif | Editörde yapılacak |
|---|---|---|
| Command Center default surface (Kanban) | Fleet (projeler×task×host) + task lifecycle (todo/ready/running/awaiting-approval/done/blocked) | Board layout + **default yüzey** yap |
| Oturum kartı = bir agent koşusu | Bir task tick'i (develop→verify→diff→merge/hold) | Per-task session kartı + drill-in |
| Tek-oturum çok-pane (plan/steer/activity/diff/verdict) | Loop fazları (develop/verify) + **native diff (4C-1)** + event bus + control | Session detail view (kompoze et) |
| Living spec + insan onayı | Intake distill (konuşma→senaryo+acceptance) + governance **hold/approve** | Spec/plan view + approve (VAR) |
| **Verifier spec'e karşı doğrular** | **Deterministik GATE = TEK merge mercii** | Gate sonucunu "Verifier verdict" olarak öne çıkar |
| Local + cloud agent birleşik | Multi-host capability routing + hosts registry | Host'ları "agent nerede koşuyor" olarak göster |
| Spaces (ilişkili işler) | **Project** | Project = Space (VAR) |
| Review/approve aynı yüzeyde | Native diff + control (approve/abort/pause) | Session view'a göm |
| Replay / audit timeline | **Event bus** (tam lifecycle event'leri) | Event'lerden timeline view |
| Bildirim (dikkat gerek) | Intervention toast (4C-3) + awaiting-approval | Var; board'a yükselt |
| ACP interop (gelecek) | — | Stratejik: ACP konuş (Conductor agent'ı başka editörlerde) |

**Sonuç:** bu bir motor projesi DEĞİL, bir **kokpit yeniden-çerçeveleme** projesi. Çoğu iş `web/` +
`editor/` webview'inde; gateway'e gerekirse yalnız **additive read** eklenir.

**Conductor'ın TEK FARKI (rakiplerde YOK):** Verifier = **deterministik gate** (LLM-yargıç değil,
GÜVENİLİR). Devin/Augment/Antigravity'de merge kararı bir LLM'e güvenir; Conductor'da kapı
makineseldir. UX'in **kahramanı bu olmalı** ("yeşil = makineyle kanıtlandı, model 'iyi görünüyor'
dediği için değil").

---

## 3. Vizyon

**Conductor Editor = "Agent Command Center wrapped in an IDE."** Açılışta editör değil, **komuta
merkezi** gelir: tüm host'lardaki tüm projelerin agent oturumları durum-bazlı bir board'da; insan
**yönetmen** — iş dağıtır (intake/spec), uzaktan izler (follow-along), **review gerektiğinde** haber
alır, diff'i yerinde inceler, **deterministik gate'in** verdict'ini görür, approve/redirect eder.
IDE ikincil: bir oturuma drill-in edince native diff/dosyalar açılır.

---

## 4. Bilgi mimarisi — 5 yüzey

### A. Command Center (DEFAULT açılış yüzeyi)
Board/Kanban: **kolonlar = lifecycle** (Ready → Running → Needs-Review[awaiting-approval/blocked] →
Done). **Kart = task/oturum**: proje(Space), lane/tier, host (nerede koşuyor), canlı faz
(develop/verify/diff), gate durumu, diff özeti (±satır). **Swimlane/filtre = Project(Space) veya
Host.** Canlı (WS) güncelleme. Üst şerit: "N running · M needs your review · K blocked." **"Needs
your review"** en görünür yer (director'ın tek kritik aksiyonu).

### B. Session view (drill-in — agent-native detay)
Tek oturum, çok-pane:
- **Spec/Plan:** senaryo başlığı + acceptance kriterleri + tier/lane + holdout (living spec).
- **Activity timeline:** event bus'tan faz-faz (develop başladı → verify → gate check'leri →
  diff → merge/hold) — Devin "follow-along" + replay/audit.
- **Diff (review):** native `conductor-diff:` virtual doc (4C-1) — yerinde inceleme.
- **Verifier verdict:** gate sonucu (her check pass/fail + evidence + holdout) — **öne çıkan**.
- **Controls (steer):** Approve / Abort / Pause / Resume (VAR) + (gelecek) "redirect/steer" notu.
- **Take over:** native editörde dosyayı/diff'i aç (IDE drill-in).

### C. Spec / Intake (intent → living spec)
Distill-chat (VAR, 3B-4): konuşma → önerilen senaryolar + holdout → **editlenebilir living spec** →
Approve → task'lar dispatch. Augment Intent'in Coordinator rolü; Conductor'da distiller + intake.

### D. Hosts / Capacity
Host registry (linux/mac, capabilities, heartbeat, aktif lease) — "agent'lar nerede koşuyor",
capability routing görünür (web→linux, ios→mac). Devin Desktop "local+cloud birleşik" karşılığı.

### E. IDE (ikincil, drill-in)
Tam Code-OSS; diff/dosya native açılır. "Agent manager wrapped in an IDE."

---

## 5. Etkileşim modeli (director-first)
Dispatch (spec) → arka planda koş → **board'da izle** → **review gerektiğinde bildirim** (native
toast + board "needs-review" rozeti) → diff'i yerinde incele → **gate verdict'ini gör** →
approve/redirect. Async; hiçbir iş diğerini bloklamaz; dikkat yalnız review anında gerekir.

---

## 6. Fazlı teslimat (her faz additive + Rule#9 + CI yeşil; webview-first)

Her kart: **kapsam · nerede (web/editor/fork/gateway) · ✓Done=kabul kriteri**.

- **E0 — Karar + ADR.** Kapsam: "command center = default surface" + B1–B6 kararlarını ADR'ye
  yaz (ADR-0032). Nerede: docs. **✓Done=** ADR merged; sınırlar (frozen-additive yalnız B1+B2,
  deep-fork yok, token disiplini) yazılı.
- **E1 — Command Center board (default yüzey).** Kapsam: Fleet → durum-kolonlu board (Ready/
  Running/Needs-Review/Done; swimlane=Project, filtre=Host; canlı-WS; üst-şerit sayaçlar);
  `onStartupFinished` singleton WebviewPanel açar; bağlantısız=connect ekranı (B4/B6). Nerede:
  `web/` (board bileşeni) + `editor/` (WebviewPanel açma). Gateway: client-side fan-out (B3),
  yeni endpoint YOK. **✓Done=** editör açılışında board geliyor; canlı task'lar doğru kolonda;
  "N needs review" doğru; **Playwright canlı**: açılış→board→3 proje/host render, kolon-geçişi WS ile.
- **E2 — Session detail view + Verifier verdict.** Kapsam: B yüzeyi (Spec + Activity-timeline +
  native Diff + **Verifier verdict** + Controls). Nerede: `web/`+`editor/` (view) **+ gateway/
  conductor additive: B1 (KindDecision'a bounded checks) + B2 (scenario read)**. **✓Done=** bir
  oturuma drill-in → acceptance + faz-faz timeline + diff + per-gate ✓/✗-evidence + approve/abort;
  Rule#9: B1/B2 real-PG cross-process (checks event'i gerçek-gate'ten gelir, sahte değil) +
  **Playwright canlı** (gerçek tick → verdict render).
- **E3 — Spec-first intake (living spec).** Kapsam: intake-chat'i öneri→editle→approve→dispatch
  "living spec" akışına yükselt; board "+ New work" girişi. Nerede: `web/`+`editor/`; gateway
  mevcut /distill+/intake. **✓Done=** konuşma→spec→onay→task'lar board'da Ready; Playwright canlı.
- **E4 — Director cilası.** Kapsam: board needs-review odağı, bildirim (4C-3 yükselt), replay/
  timeline, klavye+komut-paleti dispatch, çoklu-seçim, "take over" (native diff/dosya aç). Nerede:
  `web/`+`editor/`. **✓Done=** bildirim→tek-tık review; klavye akışı; Playwright canlı.
- **E5 — Stratejik (opsiyonel).** (a) **ACP** (Agent Client Protocol) konuş: Conductor oturumları
  ACP-uyumlu editörlerde görünür / 3.parti ACP agent'ları board'da; (b) command center **standalone**
  (Antigravity 2.0 yönü, aynı gateway); (c) develop-stdout streaming (B5 canlı log). Her biri ayrı ADR.

Her faz: spec → kodla (frozen oku→gate→commit) → Rule#9 (editör: tsc+eslint+vitest+iki bundle +
**Playwright canlı KENDİM**; gateway/conductor değişirse real-PG cross-process + `-tags e2e` her iki
mod) → push develop(+fork) → ledger+memory → CI 3-job yeşil. **Sıra E0→E1→E2→…; her faz tek başına
shippable** (board E1'den sonra zaten kullanılır; verdict E2 ekler).

---

## 7. Reuse vs yeni
**Reuse (çöp değil):** 3B cockpit bileşenleri (Fleet/Hosts/Events/Intake), native diff (4C-1),
control client (approve/abort/pause), event feed, intervention (4C-3), transport seam (4A — web=
fetch/WS, fork=postMessage). **Yeni:** board layout (durum-kolonları/swimlane), session detail
kompozisyonu, **default-surface çerçevesi** (açılış=command center), spec/living-spec view, (E5)
ACP adaptörü. **Backend'e dokunmadan** çoğu iş webview'de.

## 8. Gateway — yalnız gerekirse additive
Önce **client-side kompozisyon** (mevcut /projects /tasks /hosts /events + diff). Board ve session
view bunlardan kurulabilir. Yalnız performans/temizlik için **additive read projeksiyonu** (örn.
session-aggregation) — frozen EventBus/StateStore imzaları DEĞİŞMEZ; ADR-0021.

## 9. Riskler / disiplin
- **Overlay sınırı (ADR-0031):** command center bir **webview + birkaç editör-native afford** olarak
  gelir; Code-OSS workbench'i derin fork ETME (upstream-merge ucuz kalsın). "Default surface" =
  açılışta webview'i göster, deep-core değişiklik değil.
- **Token disiplini:** board/session canlı veriyi host-tarafı/bridge transport'tan alır; token
  webview'e/loga ASLA.
- **Frozen-additive:** engine/statestore/EventBus imza-sabit; yeni read'ler projeksiyon.
- **Self-verify:** her UX adımını Playwright ile canlı gateway'e karşı KENDİM sürerim
  ([[always-self-verify-with-playwright]]).

## 10. Fark yaratıcı (öne çıkar)
Devin/Augment/Antigravity'de "Verifier" bir LLM yargıç. **Conductor'da Verifier = deterministik
gate** (build/test/vet/lint + gizli holdout + capability-routed gerçek-host gate'leri; LLM yalnız
kodlar, "geçti" demez). Command Center'ın her kartında **güvenilir yeşil** = makineyle kanıt. Bu,
"agent yönetimi" pazarında Conductor'ın **wedge**'i; UX bunu kahraman yapmalı.

## 11. Görsel taslak (mockup)

**E1 — Command Center board (açılış yüzeyi):**

```
┌─ Conductor — Command Center ─────────────────────────  host-linux ● host-mac ● ──┐
│ 3 running · 2 need your review · 1 blocked        [ + New work ]   Filter: All ▾  │
├──────────────┬───────────────┬────────────────────────┬─────────────────────────┤
│ READY (4)    │ RUNNING (3)   │ NEEDS REVIEW (2) ⚠      │ DONE (12)                │
├──────────────┼───────────────┼────────────────────────┼─────────────────────────┤
│ web-shop     │ web-shop      │ ios-app                 │ web-shop                 │
│ W-7  T2      │ W-5 ▸develop  │ I-3  T3  await-approval │ W-4 ✓gate [task:W-4]     │
│ web·linux    │ ●host-linux   │ ios·mac    +88 −5       │ +42 −7                   │
│              │ +12 −3        │ [ Review ▸ ]            │                          │
│ ios-app I-9  │ ios-app       │ web-shop                │ …                        │
│ ios·mac  T2  │ I-7 ▸verify   │ W-6  blocked (gate)     │                          │
│              │ ●host-mac     │ [ Review ▸ ]            │                          │
└──────────────┴───────────────┴────────────────────────┴─────────────────────────┘
```

**E2 — Session view (drill-in; Verifier verdict öne çıkar):**

```
┌ ◂ Command Center · ios-app / I-3 "Add onboarding screen" · T3 · ios·mac ─────────┐
│ SPEC (living)                       │ ACTIVITY (timeline / follow-along)          │
│ • Onboarding 3 slayt gösterir       │ 10:02 develop started  ●host-mac            │
│ • "Skip" bayrağı kalıcı             │ 10:04 verify started                        │
│ • maestro akışı yeşil               │ 10:05 ✓ gate (build·test·maestro·holdout)   │
│ holdout: store://…/I-3              │ 10:05 diff ready  +88 −5                     │
├─────────────────────────────────────┤ 10:05 HELD — human approval (T3)            │
│ VERIFIER VERDICT (deterministik)     ├─────────────────────────────────────────────│
│  go build …… ✓ exit 0               │ DIFF (native, read-only)                    │
│  go test ……… ✓ exit 0               │  Onboarding.swift  +66 −0                    │
│  maestro …… ✓ flow passed           │  AppRouter.swift   +22 −5  [ Open in editor ]│
│  hidden holdout ✓                   │                                             │
│  → MERGE-READY (machine-proven)     │                                             │
├─────────────────────────────────────┴─────────────────────────────────────────────│
│ [ Approve & merge ]   [ Request changes ]   [ Abort ]   [ Pause ]                   │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

Görsel ilke: VS Code temasıyla **native** (kendi renk-token'ları), yoğun-ama-okunur kart, **gate
yeşili = kahraman** (her kartta görünür güven sinyali), director'ın tek kritik aksiyonu (**Needs
Review**) en görünür yerde. **Board düzeni KARARI (kullanıcı, 2026-06-20): Durum-Kanban** (kolonlar
= Ready/Running/Needs-Review/Done; Devin Desktop/Windsurf modeli; çok-proje swimlane; "Needs Review"
kendi kolonu). E1 bunu uygular.

## 12. ADR'ler
- **ADR-0032** command-center = default surface (B4 mekanizma + B6 tek-UI/ilk-açılış).
- **ADR-0033** gate Verifier verdict event'i (B1 additive payload; bounded checks).
- **ADR-0034** scenario/spec read projeksiyonu (B2 additive gateway read).
- (E5) ACP interop + standalone command center = ayrı ADR'ler.

## 13. Kaynaklar
- Devin Desktop / Agent Command Center: fixedlabs.ai/blog/devin-desktop-review ·
  chatforest.com (Windsurf 2.0 review + Devin Desktop rebrand) · apidog.com/blog/whats-new-in-devin-2026
- Augment Intent: augmentcode.com/blog/intent-a-workspace-for-agent-orchestration ·
  augmentcode.com/tools/intent-vs-cline
- Devin session (shell/browser/editor + planner + replay): docs.devin.ai/get-started/devin-intro ·
  medium @nitinmatani22 "Devin's Cloud Sandbox Explained"
- Google Antigravity (Agent Manager / Mission Control): developers.googleblog.com (Build with
  Antigravity) · arjankc.com.np (Agent Manager deep dive) · codecademy.com (agentic IDE comparison)
