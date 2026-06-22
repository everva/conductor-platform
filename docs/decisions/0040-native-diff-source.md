# ADR-0040 — Native diff via per-file `vscode.diff` reconstructed from the unified patch (Faz-P / P2)

## Bağlam
Kullanıcı (2026-06-22, gerçek fork'u gördükten sonra): "diff gösterimi iyi değil; Claude Code ve ya
native git diff gibi olsun." Mevcut (4C-1b): task diff'i TEK salt-okunur `conductor-diff:` virtual doc'ta
**unified PATCH metni** (`.diff` dili) → `showTextDocument`. Yani per-file gezinme yok, yan-yana/inline
native diff editörü yok, red-green gutter yok, F7 next-change yok.

İstenen: **native `vscode.diff`** (dosya başına, yan-yana/inline, red-green, gezilebilir). Bu, iki dokümanı
(before/after) gerektirir. Veri (`events.DiffSummary`, ADR-0030): `branch`, `base`, `files[{path,status,+/-}]`,
**`patch` (unified)**, `truncated`. **Tam-dosya içeriği YOK.** Kısıt: frozen-additive (ADR-0021) — backend/Go
kontratı değişmez/yalnız additive; token DONMUŞ; editör gate + gerçek fork runtime.

## Seçenekler
- **(a) P2a — unified `patch`'ten per-file before/after RECONSTRUCT, backend'siz.** Bir unified hunk her iki
  tarafı taşır: context (" ") HER İKİ tarafa, removed ("-") yalnız before'a, added ("+") yalnız after'a. Bir
  dosyanın hunk'larını birleştirince before/after = patch'in GÖSTERDİĞİ haliyle (hunk + context) yeniden kurulur
  — terminal `git diff` / Claude Code'un gösterdiğinin AYNISI. `vscode.diff(beforeUri, afterUri)` → native diff
  editörü. **SIFIR backend dokunuşu** (tamamen editör-içi → frozen-additive trivially). Sınır: tam dosya DEĞİL
  (hunk'lar arası değişmemiş bölgeler patch'te yok) — ama review için istenen tam da budur. [seçildi]
- **(b) P2b — backend full-file base+modified ship etsin (zengin DiffSummary alanı VEYA GET file-content
  endpoint), tam-dosya `vscode.diff`.** Tam dosya bağlamı (collapse'lı). Ama backend değişikliği (additive olsa
  da) + payload bütçesi (DiffSummary ~8KB cap) + bir tur daha iş. P2a fidelity zaten "git/Claude-Code gibi"yi
  karşıladığından **opsiyonel follow-up'a** bırakıldı.
- **(c) Mevcut unified-text doc'u tut.** Kullanıcı açıkça reddetti ("iyi değil"). Yalnız FALLBACK olarak kalır.

## Karar
**(a) P2a.** Yalnız `editor/`:
- **`editor/src/diffReconstruct.ts`** (PURE, vscode'suz) — `reconstructDiffFiles(patch)`: unified patch'i
  per-file `{path, before, after, binary, diffable}`'a böler. `diff --git` ile dosya ayırır; `+++ b/` (yoksa
  `--- a/`, yoksa header) ile path; context→iki taraf, `-`→before, `+`→after; new-file (`--- /dev/null`)→boş
  before, delete (`+++ /dev/null`)→boş after; **binary** ("Binary files…")→`diffable:false`; pure-rename
  (hunk yok)→`diffable:false`; boş/whitespace patch→`[]`; truncated-mid-hunk → best-effort, throw-suz. 13 test.
- **`extension.ts` `DiffStore`** — artık monotonic `n` ile keyed; her entry per-file `before/after` (reconstruct)
  + `unified` (4C-1b render, FALLBACK) tutar. URI'ler `n` gömülü: `conductor-diff:/<n>/unified.diff` (fallback)
  + `conductor-diff:/<n>/file/<idx>/<before|after>/<path>` (side; path gerçek dosya → VS Code dil/highlight
  çıkarımı). `parseDiffUri`/`diffSideUri`/`diffUnifiedUri` pure helper'lar. Content provider iki türü de servis eder.
- **Açma akışı** — `openStoredDiff`: 0 diffable dosya → unified doc FALLBACK; 1 → doğrudan `vscode.diff`;
  >1 + interactive → dosya quick-pick → `vscode.diff`; >1 + non-interactive (deep-link session split) → BİRİNCİL
  dosya (prompt'suz). `runShowDiff`/`runShowDiffForTask` interactive; `openTaskDiffBeside` (N3b split) non-interactive,
  `ViewColumn.Beside`. `vscode.diff` built-in komutu `commands.executeCommand` ile (DiffVscodeApi'ye eklendi).
- **Token DONMUŞ:** side URI'leri token-free store'u key'ler; `vscode.diff` argümanları yalnız URI/başlık.

## Sonuçlar
- **P2a (bu commit):** yalnız `editor/` (diffReconstruct + extension diff bölümü + DiffVscodeApi.executeCommand +
  vscode-mock variadic + testler). **web/src + backend/Go DOKUNULMADI** (ADR-0021; frozen-additive trivially —
  hiç backend yok). Token DONMUŞ.
- **Doğrulama (Rule#9 editör gate + gerçek runtime):** editör gate (typecheck×2 + eslint-0 + **vitest 226/3**:
  13 reconstructor + 7 yeni diff testi [native vscode.diff tek/çoklu-dosya quick-pick, binary fallback, per-file
  content-provider, parseDiffUri round-trip] + esbuild) + **electron smoke YEŞİL** (VS Code 1.125.1, exit 0, yeni
  diff bundle throw-suz boot). **GÖRSEL capstone (kullanıcı host'u):** gerçek task diff'inde native yan-yana
  red-green + F7 gezinme ekran-görüntüsü + ONAY (gateway-canlı diff = fork runtime; smoke'ta diff verisi yok).
- **Sıradaki:** P2b (opsiyonel, full-file frozen-additive backend) — kullanıcı isterse; yoksa **P3 agentic/uyum**
  (tree context-menü + keybinding + context-key + Welcome doğrula).
- ADR-0021 / ADR-0030 (KindDiff/DiffSummary diff source) / ADR-0036/0037/0038 / ADR-0039 (theme bridge) KORUNUR.
  Mevcut unified `conductor-diff:` doc'u FALLBACK olarak korunur (binary / dropped-patch için).
