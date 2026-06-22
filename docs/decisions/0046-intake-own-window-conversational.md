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
- **#3 KARŞILANDI** (intake ayrı window).

## Sonuçlar (Q3b — konuşmalı intake, web-forward)
- **IntakeChat tek-atış form → ÇOK-TURLU KONUŞMA'ya yeniden yazıldı** (web-only): proje seç + **mesaj
  thread'i** (her "Send" turnünü ekler) + composer (⌘/Ctrl+Enter). Her gönderimde TÜM director turn'leri
  birleştirilip mevcut tek-string `/distill`'e verilir → **bağlam turlar arası BİRİKİR** (gateway
  DEĞİŞMEDİ — frozen-additive'e bile gerek kalmadan client-side accumulation). Asistan yanıtı thread'e
  düşer: scenario varsa "Drafted N — review the plan below" + plan-preview (scenario kart + AUTHORITATIVE
  YAML editör + Approve, korundu); **422 → never-fabricate GUIDANCE bir clarifying yanıt olarak** (CC
  "varsayma, sor"). "Write spec directly" korundu. **Doğrulama:** web gate (typecheck + eslint-0 +
  **vitest 164/164**: IntakeChat 7 test incl. **çok-turlu accumulation** `distill` nth(2) birleşik-bağlam +
  422-clarify + edited-YAML-approve + 401 + write-spec) + **Playwright e2e 15/15** (dashboard intake
  Message/Send konuşmalı; intake.spec write-spec) + editör gate 254/3 (fork bundle yeni IntakeChat'le
  rebuild) + **electron smoke** (New Work tab konuşmalı IntakeChat'i mount eder, exit 0).
- **DÜRÜST KAPSAM:** Q3b "konuşmalı + bağlam-biriken + never-fabricate-clarify + plan-preview" verir.
  **LLM-üretimli SPESİFİK clarifying soruları + streaming = Q3b-full/Q3c'ye KALDI** (gateway `/distill`'in
  scenarios-or-422 yerine bir soru döndürmesi = backend LLM-prompt işi; frozen-additive `messages` alanı +
  distiller history). web App'in kendi intake'i de bu paylaşılan IntakeChat'i kullandığından konuşmalı oldu.
- ADR-0044/0045/0021 + token-disiplini korunur. Görsel capstone (gerçek gateway'le konuşmalı intake) = Q5/kullanıcı.
