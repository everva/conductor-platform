# ADR-0027 — Faz-4 editör fork: Code-OSS fork + webview extension (4A-0)

## Bağlam
Faz-1/2/3 tamam: gateway (`cmd/conductor-api`, ADR-0025) + web cockpit (`web/`, ADR-0026) **fork'a köprü** olarak
hazır. Uzun-vade hedef (kullanıcının baştan koyduğu) = devin/cursor-tarzı **ajan-yönetimi editör fork'u**. Karar
(kullanıcı, 2026-06-18): **VS Code fork.** Bu ADR fork'un YAPISINI ve köprü kontratını sabitler.

## Karar
**Code-OSS (MIT) fork + agent özellikleri built-in extension/webview olarak + gateway (3A) reuse.** (Cursor/Windsurf
modeli.) Kullanıcı seçimleri:
- **Fork tabanı = Code-OSS (MIT), Microsoft-branded build DEĞİL.** Marketplace/telemetri/branding tescilli; Code-OSS
  temiz. Fork ayrı repo: **`everva/conductor-editor`**. Rebrand HAFİF (product.json: ad/ikon/ürün-bilgisi).
- **Agent UI = built-in extension + webview'ler.** Çekirdek/chrome'a derin müdahale YOK (upstream-merge bakımı ağır).
  3B React bileşenleri (fleet dashboard / event-akış / müdahale / intake-chat) **webview panel** olarak reuse edilir;
  activity-bar "Conductor" view container + status-bar + komutlar editör-native sarmalar.
- **Kod yeri:** extension + paylaşılan UI **conductor-platform'da** (`editor/`) — mevcut frontend gate (tsc/eslint/
  vitest/playwright) ile test edilir, web cockpit ile bileşen paylaşır. **`conductor-editor`** (fork) yalnız Code-OSS
  + bu extension'ı built-in bundle eder + rebrand. **Gateway yerinde kalır** (conductor-platform).
- **Auth = VS Code SecretStorage** (web'in sessionStorage'ı DEĞİL). **Extension-host gateway client'ı sahiplenir**
  (token + bağlantı config'i host'ta); **webview'ler postMessage ile host'a konuşur, token webview'e GİRMEZ** (XSS
  yüzeyinden token çıkar). Bu yüzden 3B bileşenleri **transport-agnostik** yapılır: web=doğrudan fetch/WS,
  fork=postMessage-köprüsü; AYNI bileşenler iki transport'ta da çalışır.
- **Gateway frontend-agnostik kalır (ADR-0025) → OLDUĞU GİBİ reuse.** Fork yeni bir frontend; "sıfırdan" değil.

## Gerekçe
- **Fork = kabuk/branding/bundling/default-deneyim; extension+webview = özellikler.** Bu ayrım maksimum 3B reuse +
  taşınabilirlik verir; agent UI'sı React webview olduğu için fork-bakımından bağımsız gelişir.
- **Code-OSS** lisans-temiz; Microsoft build'i fork'lamak lisans/telemetri sorunları getirir.
- **Token SecretStorage + host-sahipli client**: tarayıcı-webview'in token tutmasından (ve CSP/fetch kısıtlarından)
  kaçınır; review F5/XSS dersleriyle tutarlı.
- **Transport-agnostik bileşen**: web cockpit ve fork TEK bileşen setini paylaşır — drift yok, çift bakım yok.

## Köprü kontratı (değişmez)
- Gateway (3A) ve N-9 tipli event-client TEK doğruluk yüzeyi; fork da web de onları tüketir.
- 3B bileşenleri transport seam arkasında; yeni frontend = yeni transport impl, bileşen değişmez.

## Dalga kapsamı (PHASE-4-PLAN'da detay)
- **4A** köprü-hazırlık (conductor-platform/web): 3B bileşenlerini transport-agnostik yap + paylaşılabilir forma getir.
- **4B** VS Code extension (conductor-platform/`editor/`): iskele + SecretStorage-auth + webview/postMessage köprü +
  cockpit panelleri (3B reuse) + activity-bar/status-bar/komutlar.
- **4C** editör-native: task branch diff → native diff editor, inline approve/abort/pause komutları,
  intervention-needed → native notification. (Diff kaynağı: KindDiff event'leri VEYA daemon-tarafı additive diff
  endpoint — 4C-0'da kararlaştırılır; gateway store/bus dışında repo görmüyor.)
- **4D** fork + paketleme (`conductor-editor` ayrı repo): Code-OSS fork + build (mac/linux/win) + extension'ı built-in
  bundle + rebrand + CI/release. **AĞIR altyapı dalgası.**
- **4E** uçtan-uca: forked editör'den gerçek task (intake-chat→Kontaktör→diff review→approve→merge) + editör CI gate.

## Frozen kontratlara etki
HİÇBİRİ. engine.go + statestore + EventBus dokunulmaz. 4C-1 diff için gerekirse gateway'e ADDITIVE bir read
endpoint eklenebilir (ADR-0021 additive ilkesi; salt-okuma). 4A transport seam yalnız web/ katmanında.

## Durum
✅ Karar (4A-0). Code-OSS fork + webview extension; extension conductor-platform/`editor/` + ayrı `conductor-editor`
fork reposu; token SecretStorage; gateway reuse. Sıra: önce minör follow-up sweep → 4A.
