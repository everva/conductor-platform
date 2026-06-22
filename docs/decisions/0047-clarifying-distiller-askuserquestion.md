# ADR-0047 — Clarifying distiller (AskUserQuestion-style intake) (Faz-Q / Q3c)

## Bağlam
Kullanıcı (2026-06-22) Q3b'yi GERÇEK Claude Code CLI kaynağına karşı denetletti
(`github.com/yasasbanukaofficial/claude-code` = tam çıkarılmış CLI) ve haklı olarak **eksik** buldu:
Q3b "konuşmalı + bağlam-biriken + plan-preview + never-fabricate-guidance" verdi AMA Claude Code'un
**imza mekanizması yok** — belirsizlikte LLM'in **spesifik, yapılı çoktan-seçmeli clarifying soruları**
üretmesi. CC `src/tools/AskUserQuestionTool`: `questions[1-4]{question, header(≤12 char chip),
options[2-4]{label(1-5 word), description, preview?}, multi_select}` + "Other" özgür-metin OTOMATİK.
"Clarify ambiguity, gather preferences — ask, don't assume." Q3b'nin clarify'ı generic 422-guidance idi.

Conductor `/distill` zinciri (mevcut): gateway `handleDistill` → `intake.Distiller.Distill(ctx,conv)
([]Scenario,error)` (422 `ErrNoScenarios` / 501 nil / 502 runner); `CommandDistiller` (`distiller_real.go`)
`claude -p` + `distillPrompt` → fenced YAML → `ParseScenarios`. Bu yol DONMUŞ kalmalı (eski testler/fake'ler).

## Karar
**AskUserQuestion'ı ADDITIVE bir distiller seam'i olarak ekle; mevcut `Distiller.Distill` + 422 yolu
DOKUNULMAZ (ADR-0021 frozen-additive).** Distiller'ın LLM'i tek çağrıda **ya scenario ya clarifying-soru**
döndürür (model karar verir; varsayma → sor).

- **`intake.Question`/`QuestionOption`** (CC AskUserQuestion aynası) + **`ValidateQuestions`**: 1-4 soru,
  benzersiz soru metni; her soru: metin + header(≤12 char) + 2-4 option (label + description, label
  benzersiz). "1-5 kelime" guidance HARD-enforce DEĞİL (model çıktısı için kırılgan, UI-invariant değil);
  header-uzunluğu + option-sınırları (UI sözleşmesi) enforce. `preview` ertelendi.
- **`DistillOutcome{Scenarios, Questions}`** + AYRI **`ClarifyingDistiller{DistillOrClarify(ctx,conv)
  (DistillOutcome,error)}`** seam. `CommandDistiller` HER İKİSİNİ implement eder (AYRI `clarifyRun`
  runner — `clarifyPrompt` prepend eder; frozen `run`/`distillPrompt` el değmemiş). Gateway type-assert
  ile zengin seam'i seçer; yoksa eski `Distill`.
- **`ParseOutcome`** (deterministik): scenarios bloğu varsa kazanır (model belirsizliği çözmüş);
  yoksa questions bloğu; ikisi de yoksa → **`ErrNoScenarios` KORUNUR** (gateway 422 birebir aynı). Malformed
  scenarios bloğu yüzeye çıkar (gizlenmez). Fence'ler `<<<SCENARIOS`/`<<<QUESTIONS` (paylaşılan `extractFenced`).
- **`clarifyPrompt`**: (A) yeterli bağlam → scenarios fence; (B) belirsiz → "VARSAYMA, sor" → questions fence;
  "EITHER/OR, never both/neither; 'Other'ı UI ekler — sen ekleme; multi_select yalnız >1 geçerliyse."

## Sonuçlar (Q3c.1 — backend tipler + seam; bu commit)
- `internal/intake/question.go` (Question/QuestionOption/DistillOutcome/ClarifyingDistiller +
  ValidateQuestions + ParseQuestions + ParseOutcome + sentinels `ErrNoQuestions`/`ErrMalformedQuestions`) +
  `distiller.go` (`clarifyRun` alanı + `DistillOrClarify` + `ClarifyingDistiller` assertion +
  `NewClarifyingDistillerWithRunner` + generic `extractFenced`, `extractScenarioBlock` delegate eder —
  davranış-koruyan refactor, mevcut testler kanıtlar) + `distiller_real.go` (`clarifyPrompt` +
  `claudeClarifyRunner` + paylaşılan `claudeRunnerWithPrompt`; `NewCommandDistiller` her iki runner'ı bağlar).
- **`Distiller.Distill` + `ParseScenarios` + 422 yolu DEĞİŞMEDİ** (frozen-additive çizgisi). Token DONMUŞ
  (distiller env'i `envsafe.Sanitize` ile CONDUCTOR_* / GH_TOKEN strip — DEĞİŞMEDİ).
- **Doğrulama:** Go gate GREEN (build + `go test ./...` + vet + **golangci-lint 0 issues** + gofmt-clean) +
  **`-race`** (intake + conductor-api) yeşil. `question_test.go`: ValidateQuestions 14-satır tablo +
  12-char sınır + ParseQuestions (valid/no-block/empty/bad-yaml/invalid-set) + ParseOutcome
  (scenarios-win/questions/both-prefer-scenarios/malformed-surfaces/neither→ErrNoScenarios) +
  DistillOrClarify (questions/scenarios/nil-runner/empty-conv/run-err-empty/run-err-with-output).

## Sonuçlar (Q3c.2 — gateway additive `questions`; bu commit)
- `cmd/conductor-api/control.go` `handleDistill`: distiller `ClarifyingDistiller` ise `DistillOrClarify`,
  değilse frozen `Distill` (eski fake'ler/`stubDistiller` bu fallback'tan geçer = davranış birebir).
  `distillResultDTO`'ya **`questions []questionDTO json:"questions,omitempty"`** (snake_case `questionDTO`/
  `questionOptionDTO` + `toQuestionDTO`). Eşleme: scenarios→200 `{scenarios,yaml}` (questions OMIT) ·
  clarify→200 `{scenarios:[],questions:[...]}` · `ErrNoScenarios`→**422 KORUNDU** · `ErrMalformed{Scenarios,
  Questions}`→422 · diğer→502 · nil→501. Conversation loglanmaz.
- **Entegrasyon bulgusu + düzeltme:** `*CommandDistiller` HER ZAMAN `ClarifyingDistiller`'ı implement eder
  (method-set), bu yüzden `NewCommandDistillerWithRunner` (distill-only) ile kurulu distiller de gateway'in
  type-assert'inden geçer → `DistillOrClarify` "no clarify runner"→502 ile mevcut `TestDistillRoundTripViaRunnerStub`'ı
  kırdı. **FIX:** `DistillOrClarify` clarifyRun yoksa **distill runner'a graceful-degrade** eder (scenarios
  üretir, asla questions). Böylece her constructor için tutarlı bir ClarifyingDistiller; gateway sürprizsiz
  type-assert eder. (Q3c.1 unit testi `NoRunner_Rejected` + `FallsBackToDistillRunner` olarak güncellendi.)
- **Doğrulama:** Go gate GREEN (build+test+vet+**golangci 0**+gofmt) + **`-race`** (intake+conductor-api) +
  **GERÇEK-PG** (`make itest` statestore+events conformance + conductor-api suite `TEST_DATABASE_URL` ile,
  hepsi yeşil — distill yeni persistence EKLEMEZ, yalnız read-only `GetProject`). `distill_test.go`:
  `stubClarifyingDistiller` + clarify-questions-200 (snake_case `multi_select`) + scenarios-200-omits-questions
  (frozen-additive byte-kanıtı) + no-scenarios-422 + malformed-questions-422 + clarify-via-runner-stub
  (GERÇEK CommandDistiller + ParseOutcome). Mevcut 11 distill testi DEĞİŞMEDEN geçer (legacy fallback kanıtı).

## Sonuçlar (Q3c.3 — web AskUserQuestion UI; bu commit)
- `web/src/api/types.ts`: additive `Question`/`QuestionOption` + `DistillResult.questions?` (eski alanlar
  korunur; `request<DistillResult>` JSON cast → questions otomatik akar, client.ts mantığı DEĞİŞMEDİ, 422
  yolu korunur). `web/src/intake/QuestionCard.tsx`: CC-tarzı — header chip + her soru fieldset/legend +
  option'lar (label kalın + description) + radio (single) / checkbox (multiSelect) + **auto "Other"** (ayrı
  label + özgür-metin, geçerli-HTML tek-kontrol) + "Submit answers" (tüm sorular yanıtlanana dek disabled);
  yapılı `{question,answer}[]` emit eder (label'lar virgülle, Other→metin). `IntakeChat`: distill yanıtında
  `questions` → thread'e asistan turu + QuestionCard render; cevap → konuşmaya **"Q: … → A: …"** director-turu
  eklenir → re-distill (Q3b client-side accumulation reuse); scenarios → plan-preview (korundu); 422 →
  never-fabricate guidance (fallback korundu). `intake.css` soru/option/Other stilleri (design-token).
- web App + Go DOKUNULMADI dışında: bu EDITÖR+WEB paylaşımlı cockpit'i etkiler (ADR-0029 reuse) → web App
  intake'i de CC-soru-turlarını alır. **Token DONMUŞ** (saf UI; gateway verisi). cockpit barrel değişmedi
  (QuestionCard IntakeChat-içi; editör bundle transitif alır).
- **Doğrulama:** web gate (typecheck + eslint-0 + **vitest 170/170**: QuestionCard 5 [render/single/Other/
  multi/all-answered] + IntakeChat clarifying-turn [sorular render → cevap → "Q:→A:" folded re-distill → proposal])
  + **Playwright e2e 16/16 KENDİM** (`:5173`; yeni `intake.spec` stateful `/distill` mock: 1.çağrı questions →
  card → Postgres seç → Submit → 2.çağrı scenarios → YAML editör; card kaybolur) + **editör gate** (tsc×2 +
  eslint-0 + vitest + esbuild; webview bundle QuestionCard'ı içerir, main.css +1.2kb) + **GERÇEK fork electron
  smoke YEŞİL** (VS Code 1.125.1: `tabs after conductor.newWork: ["Conductor","New Work"]`, exit 0 — New Work
  intake yüzeyi QuestionCard'lı bundle ile gerçek runtime'da açılır).
- **#5 (CC-gibi intake) KAPANDI:** Q3b konuşmalı + Q3c gerçek AskUserQuestion (LLM-üretimli spesifik yapılı
  clarifying sorular) → kullanıcı denetiminin "eksik" bulduğu imza mekanizması artık var.

## Kalan (yalnız kullanıcı / opsiyonel)
- **Q3c.4** (ops.) streaming + fork re-inject (inject SONRASI `codesign --force --deep --sign -` + `open -n`) +
  canlı gateway görsel capstone (CC-tarzı soru-çipleri gerçek fork'ta) + kullanıcı görsel onayı.
