# Q3c — Claude-Code-faithful clarifying intake (AskUserQuestion + plan-mode) — detailed plan

Kullanıcı (2026-06-22) Q3b'yi gerçek Claude Code kaynağına karşı denetletti
(`github.com/yasasbanukaofficial/claude-code` = tam çıkarılmış CLI) ve haklı olarak **eksik** buldu:
Q3b "konuşmalı + bağlam-biriken + plan-preview" verdi AMA Claude Code'un **imza mekanizması yok** —
LLM'in **spesifik, yapılı çoktan-seçmeli clarifying soruları** üretmesi. Q3c bunu kapatır.

> ⚠️ DİSİPLİN (Faz-Q ile AYNI): UYDURMA YOK · frozen-additive (ADR-0021; mevcut sözleşmeleri kırma) ·
> token DONMUŞ · HER task: Go gate (build+vet+golangci-0+`-race`) + GERÇEK-PG/stub + web gate +
> Playwright e2e (`:5173` KENDİM) + GERÇEK fork electron smoke + commit develop + **CI 3-job yeşil**
> (`gh run view`) + ledger/memory. **Bu PLAN; implementasyon compact sonrası.**

## §0 — GERÇEK KAYNAK (grounding; uydurma değil)
**Claude Code (verilen repo `src/tools/`):**
- **`AskUserQuestionTool`** (`src/tools/AskUserQuestionTool/{AskUserQuestionTool.tsx,prompt.ts}`): LLM
  belirsizlikte SORU sorar. Zod şeması:
  `questions: [{ question:string, header:string(≤12 char chip), options:[{label:string(1-5 word),
  description:string, preview?:string}](min2,max4), multiSelect:bool(def false) }](min1,max4)`.
  "Other" özgür-metin OTOMATİK eklenir (şemada yok). Öneri varsa ilk option + "(Recommended)".
  Cevap: `answers: record<questionText, answerString>` (multi = virgül-ayrık). Açıklama: "clarify
  ambiguity, gather preferences, make decisions, offer choices."
- **`ExitPlanModeTool`** (`src/tools/ExitPlanModeTool/prompt.ts`): plan yazılır → ExitPlanMode onaya
  sunar → kullanıcı onaylamadan implementasyon yok. "Unresolved questions → AskUserQuestion FIRST
  (earlier phases); finalized plan → ExitPlanMode." **Conductor karşılığı ZATEN VAR:** intake
  Approve-gate (confirm → POST /intake) = ExitPlanMode analoğu. Q3c yalnız AskUserQuestion'ı ekler.

**Conductor /distill zinciri (mevcut, değiştirilecek noktalar):**
- Gateway `cmd/conductor-api/control.go`: `handleDistill` → `distillRequest{Conversation string}` →
  `s.distiller.Distill(ctx, conversation) ([]Scenario, error)` → 422 (ErrNoScenarios) / 501 (nil) /
  500. Yanıt `{scenarios, yaml}` (`distillResponse`).
- `internal/intake/distiller.go`: `Distiller interface { Distill(ctx, conversation string)
  ([]Scenario, error) }`; `CommandDistiller` (`distiller_real.go`) `claude -p` çalıştırır
  (`distillPrompt + conversation` stdin → JSON parse → []Scenario).
- web `IntakeClient.distill(projectId, conversation) → DistillResult{scenarios, yaml}` (Q3b: çok-turlu
  thread, bağlam client-side birikiyor, 422→generic guidance). **IntakeChat zaten konuşmalı (Q3b).**

## §1 — Hedef (Q3c = AskUserQuestion mekanizması)
Director mesaj yazar → distiller **ya scenario önerir (yeterli bağlam) YA DA spesifik clarifying
SORULARI döndürür (belirsizlik)** → IntakeChat soruları **CC-tarzı option-çipleriyle** render eder
(label+description, multiSelect, "Other" özgür-metin) → director seçer → cevap konuşmaya eklenir →
yeniden distill → netleşince scenario → plan-preview → Approve (mevcut gate = ExitPlanMode analoğu).

## §2 — Mimari karar (frozen-additive)
Distiller'ın LLM'i, tek JSON'da **scenario VEYA question** döndürür (LLM karar verir). Seam'leri
ADDITIVE genişlet; mevcut `Distill(...)→[]Scenario` + 422 yolu DOKUNULMADAN korunur (eski testler/
fake'ler kırılmaz). **Yeni:** zengin sonuç + yeni distiller metodu + additive yanıt alanı.

- `intake.Question` tipi (CC AskUserQuestion aynası): `Question{ Question string; Header string;
  Options []QuestionOption; MultiSelect bool }`, `QuestionOption{ Label string; Description string }`
  (preview opsiyonel/sonraki). Validasyon: 1-4 soru, soru/label benzersiz, 2-4 option (CC kuralı).
- `intake.DistillOutcome{ Scenarios []Scenario; Questions []Question }` + yeni metot
  `ClarifyingDistiller interface { DistillOrClarify(ctx, conversation string) (DistillOutcome, error) }`
  (AYRI seam; `Distiller` DOKUNULMAZ → eski yol/fakeler intact). `CommandDistiller` İKİSİNİ de
  implement eder (type-assert ile gateway hangisini kullanacağına karar verir; yoksa eski Distill).
- LLM prompt (`distiller_real.go` `distillPrompt` YANINDA yeni `clarifyPrompt`): "Yeterli bağlam
  varsa `{"scenarios":[...]}`; eksikse `{"questions":[{question,header,options:[{label,description}],
  multiSelect}]}` döndür — VARSAYMA, sor (never-fabricate=CC ask-don't-assume)." JSON-parse her iki
  şekli ayırt eder.

## §3 — TASK-TASK plan (otonom; her task CI-yeşil)
**Q3c.1 — backend tipler + distiller seam (Go)** [ADR-0047]
- `internal/intake/question.go`: `Question`/`QuestionOption` + `ValidateQuestions` (1-4, benzersiz,
  2-4 opt; CC kuralları). Pure + tablo testleri. `DistillOutcome` + `ClarifyingDistiller` seam.
- `CommandDistiller.DistillOrClarify`: `clarifyPrompt`+conversation → `claude -p` → JSON parse
  (scenarios|questions) → DistillOutcome. Parse/validate testleri (stub runner; GERÇEK claude YOK).
- Go gate (build+vet+golangci-0+`-race`) + intake unit testler.

**Q3c.2 — gateway additive yanıt (Go)** [ADR-0047]
- `handleDistill`: distiller `ClarifyingDistiller` ise `DistillOrClarify` kullan; `distillResponse`'a
  **additive `questions []questionDTO`** ekle (eski `{scenarios,yaml}` korunur; questions boşsa eski
  davranış birebir). LLM hiçbir şey vermezse 422 KORUNUR (never-fabricate). 200 + questions = "clarify
  gerek". `cmd/conductor-api/control_test.go`: scenarios / **questions** / 422 / 501 yolları.
- Go gate + **GERÇEK-PG conformance** (docker; mevcut + yeni alan) + gateway testleri.

**Q3c.3 — web: AskUserQuestion-tarzı UI (paylaşılan cockpit)** [ADR-0047]
- `web/src/api/types.ts`: `DistillResult`'a additive `questions?: Question[]` + `Question`/
  `QuestionOption` tipleri (eski alanlar korunur). `IntakeClient.distill` aynı imza (yanıt zenginleşir).
- `web/src/intake/QuestionCard.tsx`: CC-tarzı — soru başlığı + `header` chip + option'lar (label kalın
  + description) + multiSelect (checkbox) / single (radio) + **"Other" özgür-metin** + "Submit". A11y.
- `IntakeChat` (Q3b thread): distill yanıtında `questions` varsa → thread'e assistant "soru" turu
  (QuestionCard) ekle; director cevaplar → cevapları konuşmaya **"Q: … → A: …"** olarak ekle → yeniden
  distill. scenarios varsa → plan-preview (mevcut). 422 → generic guidance (fallback korunur).
- web gate (vitest: QuestionCard render/multi/Other + IntakeChat soru-turu→cevap→re-distill akışı) +
  **Playwright e2e KENDİM** (mesaj→sorular→seç→öneri) + editör gate (fork bundle rebuild) + electron smoke.

**Q3c.4 — (ops.) streaming + capstone**
- (Stretch) distill satır-satır streaming (SSE/WS) — ayrı, riskliyse ATLA.
- Capstone: re-inject fork + electron smoke + **canlı demo** (:8099 distiller'lı; yoksa stub) + kullanıcı
  görsel onayı (CC-tarzı soru-çipleri gerçek fork'ta).

## §4 — Frozen-additive ÇİZGİSİ (net)
- `Distiller.Distill` + 422 yolu = DOKUNULMAZ (eski fakeler/testler/clientler intact).
- Yeni: `ClarifyingDistiller`/`DistillOutcome`/`Question` + gateway additive `questions` alanı + web
  additive `questions?`. Eski client questions'ı yok sayar; yeni IntakeChat kullanır. **Davranış:**
  LLM hiçbir şey vermezse 422 korunur (never-fabricate); questions = yeni 200 başarı şekli.
- **GOTCHA (Faz-Q'dan):** vitest `vi.fn<typeof fetch>()` formu · CSS-foundation · cross-dir dep
  (editor/package.json + tsconfig.webview) · CI `gh run view` (watch yanıltır) · self-hosted runner
  backlog → poll · **fork app inject SONRASI ad-hoc re-sign** (`codesign --force --deep --sign -`;
  inject seal'i bozar → app sessiz ölür) + `open -n` (tek-instance handoff'u bypass).

## DURUM
- Faz-Q TAMAM (Q0–Q4 + Q3a/Q3b, develop `ff9da3f`, 10 commit CI-yeşil). Q3b konuşmalı AMA AskUserQuestion
  YOK (kullanıcı denetledi, haklı). **Q3c = bu plan: AskUserQuestion-tarzı clarifying (backend LLM +
  gateway additive + web CC-UI).** İmplementasyon compact sonrası, Q3c.1→.4 sırasıyla. ADR-0047.
