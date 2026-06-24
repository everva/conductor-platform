# ADR-0052 — Native "Dispatch Work" (webview-less agentic-native entry point)

## Bağlam
Kullanıcı (2026-06-24): *"onay tüm her yeri kullanışlı yap ve maksimum vscode agentic-native kodlama
istiyorum."* AskUserQuestion → **"Native dispatch + kontrol"** seçti: işi tarif et → dispatch et →
agent'ı izle → native diff review → approve/müdahale, hepsi fork içinde, mümkün olan her yerde
webview'siz.

Mevcut durumda iş yaratmanın TEK yolu webview Intake'ti (`IntakePanel`, `conductor.newWork` →
`IntakeChat` data-surface="intake"): zengin, claude-destekli drafting yüzeyi — ama webview, fare-ağırlıklı.
Mimaride kod UZAK host'larda (davinci/everva) yazılır; editör = native KONTROL/REVIEW yüzeyi. Kontrol
tarafı zaten native + zengindi (4C-2 pause/resume/abort/approve, retry, P3 context-menüleri,
keybinding'ler, `conductor.connected` context-key). Eksik olan tek agentic-native parça: **klavyeyle,
webview'siz İŞ YARATMA/DISPATCH.**

## Karar
**Native, webview-LESS "Dispatch Work" akışı (Faz-R) — komut paleti + QuickPick/InputBox zinciri webview
Intake'in tamamlayıcısı (onu DEĞİŞTİRMEZ).**

- `conductor.dispatch` komutu (palette + sessions-tree `view/title` rocket + proje context-menüsü
  `0_create` + keybinding `Ctrl/Cmd+Alt+N`, hepsi `when: conductor.connected`). Palette'ten →
  proje QuickPick; ağaç proje-menüsünden → proje preselected.
- Prompt zinciri (`runDispatch`, extension.ts): başlık → acceptance (`;`/newline ayrılır) → tier
  (T1..T4 QuickPick) → lane (prefilled "backend") → id (başlıktan slug, düzenlenebilir) → modal
  confirm → POST. Başarıda: created id'ler toast + sessions-tree refresh + "Open Session" ile yeni
  session'a deep-link (N3 `conductor.openSession`).
- PURE çekirdek `editor/src/dispatch.ts` (slug / `buildIntakeYaml` / tier vocab / acceptance split /
  holdout default / `{created,skipped}`→mesaj) — vitest'le pinlenir, vscode'suz. YAML, gateway'in
  intake şemasının TAM aynı: id/title/lane/tier/acceptance/hidden_holdout_ref; holdout repo-EXTERNAL
  default `store://holdouts/{id}/holdout_test.go` (ADR-0018). Her scalar double-quote'lanır → tuhaf
  başlık dokümanı bozamaz.
- `ControlClient.intake(projectId, yaml)` — RAW YAML gövde (JSON değil) + `Content-Type:
  application/yaml`, gateway `handleIntake`'in okuduğu gibi. Yeni 400→`invalid` reason + secret-free
  `detail` (gateway'in `{error}` validation mesajı) → kullanıcı NEDEN reddedildiğini görür.

**Neden ayrı komut, newWork'ü değiştirmeden:** webview Intake claude-destekli drafting için değerli
(distill/clarify); native dispatch klavye-öncelikli hızlı giriş. İkisi aynı gateway endpoint'ine
(`POST /intake`) düşer — native olan sadece YAML'ı yerel sentezler. Gateway DEĞİŞMEDİ.

Frozen-additive (ADR-0021): yalnız editor/ eklendi; Go/gateway/web DOKUNULMADI. TOKEN DİSİPLİNİ:
intake POST'u host-side ControlClient'ın Authorization header'ında; YAML/id/mesajlar token taşımaz;
dispatch.ts token'ı hiç görmez.

## Doğrulama
- `editor/src/dispatch.test.ts` (8 test): slug, YAML (well-formed + quoting + deps), tier parse,
  acceptance split, holdout default, outcome mesajı.
- `editor/src/controlClient.test.ts`: intake RAW-yaml POST (content-type + bearer-only) + 400→invalid
  (detail) + 401/no-token/throw mapping; token-leak guard intake'i de kapsar.
- `editor/src/extension.test.ts` (`runDispatch`): preselected happy-path (sentez YAML her alanı taşır
  + refresh + Open-Session deep-link), palette proje-pick, başlık-cancel no-op, boş-acceptance reddi,
  confirm-decline no-op, 400 detail mesajı. Disposable 17→18, subscription 40→41.
- Editör gate yeşil: tsc×2 + eslint-0 + vitest 336/3 + esbuild + **electron smoke** (GERÇEK VS Code:
  `conductor.dispatch` getCommands'ta kayıtlı). Görsel runtime capstone = kullanıcı fork rebuild.
