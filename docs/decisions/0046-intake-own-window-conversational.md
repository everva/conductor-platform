# ADR-0046 — Intake as its own window + (planned) Claude-Code-style conversational flow (Faz-Q / Q3)

## Bağlam
Kullanıcı (2026-06-22, canlı fork): **"intake ayrı bir window olsun"** (#3) + **"buradaki claude
code gibi bir intake olsun, şu an çok basit duruyor"** (#5). Mevcut `web/src/intake/IntakeChat.tsx`
adına rağmen monolitik cockpit'in **"Intake" TAB'ında** gömülü + **tek-atış form** (proje + textarea
→ `/distill` tek string → YAML editör → approve). Ayrı yüzey değil; çok-turlu/clarifying/streaming yok.

CC grounding (`anthropics/claude-code` klonu, `plugins/feature-dev`): 7-fazlı konuşma+onay döngüsü
(Discovery→Exploration→**Clarifying[CRITICAL: sor-bekle-VARSAYMA]**→Architecture[öner+sor]→Implementation
[onaysız başlama]→Review→Summary). Conductor'ın **never-fabricate**'i (422→"add more detail" guidance)
CC'nin "varsayma, sor" ilkesiyle birebir.

## Karar (sahneli: Q3a şimdi, Q3b/Q3c sonra)
**Q3a — Intake'i KENDİ editör-alanı penceresine taşı.** `IntakeChat`'i `@cockpit` barrel'ından
STANDALONE export et; fork webview entry'sini **çift-modlu** yap: `#root` `data-surface="intake"`
ise IntakeChat'i (proje listesini bridge'den çekip) mount eder, değilse Command Center cockpit'i
(byte-aynı). `webviewHtml({surface})` `data-surface`'i ekler (yalnız "intake" literal'i; injection-free).
Host'ta **`IntakePanel`** (singleton WebviewPanel, `conductor.intake` viewType, "New Work" başlık) +
**`conductor.newWork`** komutu. CC ile AYNI bundle+bridge factory (token host-tarafı, CSP
`connect-src 'none'`). **REST-only** (distill/intake/listProjects) → canlı WS YOK → bağlantı göstergesi
YOK → ikinci canlı akış YOK (**Q0.4 tek-bağlantı disiplini korunur**).

**Q3b/Q3c (sonraki, henüz YOK):** IntakeChat'i çok-turlu konuşmaya çevir (mesaj geçmişi + clarifying
turu + plan-preview) + backend `/distill` conversation HISTORY (frozen-additive opsiyonel alan; tek-string
fallback) + streaming. CC 7-faz modeli + never-fabricate=clarify.

## Sonuçlar (Q3a)
- `editor/` (`IntakePanel`+`conductor.newWork`+`webviewHtml({surface})`+fork `main.tsx` çift-mod
  `IntakeApp`) + paylaşılan `web/src/cockpit.ts` (additive `IntakeChat`/`IntakeChatProps`/`IntakeClient`
  export). web App + Go DOKUNULMADI (ADR-0021); IntakeChat'in KENDİSİ değişmedi (Q3b'de değişecek).
  **Token DONMUŞ.** İkinci webview-bridge intake için var AMA REST-only (WS yok) → tek canlı akış korunur.
- **Doğrulama:** editör gate (typecheck×2 + eslint-0 + **vitest 254/3**: IntakePanel open/singleton +
  webviewHtml surface[intake/default/bogus] + registerConductor 12 disposable + activate 26 + newWork
  komut + esbuild) + **web gate** (typecheck + eslint-0 + vitest 163/163; barrel export) + **GERÇEK fork
  electron smoke YEŞİL**: `tabs after conductor.newWork: ["Conductor","New Work"]` — Intake KENDİ editör
  sekmesinde açıldı (çift-mod + IntakePanel + komut uçtan-uca gerçek runtime'da çalışıyor; exit 0).
- **#3 KARŞILANDI** (intake ayrı window). **#5 (CC-tarzı konuşmalı) Q3b/Q3c'ye KALDI** (dürüst). Q3a
  mevcut tek-atış IntakeChat'i kendi penceresinde sunar; konuşmalı dönüşüm follow-up.
- ADR-0044/0045/0021 + token-disiplini korunur. Görsel capstone (gerçek gateway'le intake akışı) = Q5/kullanıcı.
