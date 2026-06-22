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

## Kalan (sonraki commitler)
- **Q3c.2** gateway: `handleDistill` `ClarifyingDistiller` ise `DistillOrClarify`; `distillResponse`'a
  ADDITIVE `questions` alanı (boşsa eski `{scenarios,yaml}` birebir; LLM hiçbir şey vermezse 422 KORU);
  `control_test.go` scenarios/questions/422/501 + GERÇEK-PG conformance.
- **Q3c.3** web: `types.questions?` + `QuestionCard.tsx` (CC-tarzı chip + option + "Other" + multiSelect) +
  `IntakeChat` soru-turu → cevap → re-distill; web gate + Playwright e2e (KENDİM) + editör gate + electron smoke.
- **Q3c.4** (ops.) streaming + fork re-inject (re-sign + `open -n`) + görsel capstone + kullanıcı onayı.
