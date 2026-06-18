# ADR-0026 — Frontend stack + auth/session + frontend gate (3B-0)

## Bağlam
Faz-3 DALGA 3B web cockpit'i (insan paneli) gateway'in (ADR-0025) üstüne oturacak. Karar: hangi frontend
stack, nerede yaşar, event tipleri nasıl drift'siz tutulur, auth/session nasıl, ve **deterministik frontend
kalite kapısı** ne (Go gate'inin frontend karşılığı — çünkü en kritik prensip: kaliteyi yalnız deterministik
kanıtla, ADR `decisions/README` §en-kritik-prensip; bu cockpit'in kendi kapısına da uygulanır).

## Karar
**Stack = React + Vite + TypeScript; repo'da `web/`; event tipleri N-9'dan codegen; auth = bearer token
(session'da); frontend gate = tsc + eslint + vitest + Playwright.**

- **Stack:** React 18 + Vite (dev server + build) + TypeScript (strict). Gerekçe: en yaygın, fork-webview'lere
  taşınabilir (PHASE-3-PLAN köprü gerekçesi), zengin ekosistem, Playwright/vitest ile birinci-sınıf test. Durum
  yönetimi başta minimal (React Query/SWR-tarzı bir fetch + WS hook'u; ağır global-store gerekmez).
- **Konum:** `web/` dizini, kendi `package.json`'ı + kendi araç zinciri. Go modülünden İZOLE (Go `go build ./...`
  `web/`'i görmez; node_modules `.gitignore`'da). Kendi `.conductor` web reçetesiyle Kontaktör tarafından da
  geliştirilebilir (scaffolder web profili; gate = aşağıdaki frontend gate).
- **Event tipleri (drift YOK):** `cmd/eventgen` (N-9) zaten Go `internal/events/event.go`'dan TS üretiyor
  (`types.gen.ts`). Cockpit BUNU tek-kaynak alır: build/CI öncesi `eventgen -out web/src/types/events.gen.ts`
  ile üretilir (committed + "DO NOT EDIT"). Phase/Kind/Event tarayıcıda Go ile birebir aynı; tip kayması imkânsız.
- **API client:** gateway'in (ADR-0025) REST + WS yüzeyine tipli ince bir client (`web/src/api/`): REST
  `Authorization: Bearer`, WS `/ws?...&token=` (tarayıcı header koyamaz). Tek frontend-agnostik kontrat;
  fork da aynı client'ı reuse eder.
- **Auth/session:** tek bearer token (gateway `CONDUCTOR_API_TOKEN`). UI'da bir token-giriş ekranı → token
  `sessionStorage`'da (sekme kapanınca silinir; localStorage DEĞİL — XSS yüzeyini daraltır), her REST isteğine
  header + WS'e query olarak eklenir. mTLS/OIDC/çok-kullanıcılı oturum SONRAYA ertelendi (önce çalışan tek-token,
  ADR-0025 ile hizalı). Token asla log'a/DOM'a basılmaz. (Prod'da TLS zorunlu — ADR-0025 ingress notu.)
- **Frontend gate (deterministik — merge yetkisi):** `tsc --noEmit` (tip), `eslint` (lint), `vitest` (birim),
  `Playwright` (e2e/akış). Hepsi exit-code'lu; Go gate'iyle aynı felsefe (öznel-skor yok). Bu, `web/`'in
  `.conductor` reçetesinin verify komutları olur + ayrı bir CI job'ı (3C). Görsel-regresyon gerekirse ADR-0023
  görsel-diff reçetesi (deterministik image-diff) reuse edilir, yeni-tip değil.

## Gerekçe
- **Köprü değeri:** React/TS bileşenleri + tipli event-client Faz-4 fork webview'lerine taşınır; atılan tek şey
  ince web kabuğu (PHASE-3-PLAN). Stack seçimi bu taşınabilirliği maksimize eder.
- **Drift'siz tipler:** event taksonomisi zaten Go tek-kaynak + codegen (N-9); cockpit onu tüketince client/server
  ayrışamaz — Faz-1/2'deki "şema tek-kaynak→codegen" disiplininin frontend'e doğal uzantısı.
- **Deterministik frontend kapısı:** cockpit'in kendisi de "kalite-kontrolden geçmiş çıktı" ilkesine tabi;
  tsc/eslint/vitest/playwright exit-code'ları LLM-skoru değil → platformun çekirdek prensibiyle tutarlı.
- **İzolasyon:** `web/` ayrı araç zinciri Go gate'ini/binary'lerini hiç etkilemez; node yalnız frontend CI'da.

## Sonuç — DALGA 3B kapsamı (bu kararla netleşti)
- **3B-0 (bu ADR) + iskele:** `web/` Vite+React+TS iskelesi, `events.gen.ts` codegen, tipli API client + WS hook,
  token-giriş + frontend gate (tsc/eslint/vitest/playwright) yeşil, gateway'e bağlanır.
- **3B-1** filo dashboard (projeler/host/task canlı: REST + WS), **3B-2** canlı event akışı (intervention vurgulu),
  **3B-3** müdahale (approve/pause/resume/abort → control API), **3B-4** intake-chat (yazışma→distiller→senaryo+
  holdout→insan onay→ledger; kullanıcının çekirdek vizyonu).

## Frozen kontratlara etki
HİÇBİRİ. Frontend yalnız gateway'in (ADR-0025) HTTP/WS yüzeyini ve N-9 codegen çıktısını TÜKETİR. Go tarafı
(engine.go + statestore + events) DOKUNULMAZ; tek olası ek `eventgen`'in `-out` ile web'e de yazması (zaten var olan
bayrak, additive kullanım).

## Durum
✅ Karar verildi (3B-0). React+Vite+TS · `web/` · N-9 codegen tipleri · bearer-token (sessionStorage) ·
gate=tsc+eslint+vitest+playwright. İskele (3B-0 kod) başlıyor.
