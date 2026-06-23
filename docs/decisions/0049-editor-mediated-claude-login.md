# ADR-0049 — Editör-aracılı claude login (taşınabilir OAuth token, xirigo modeli)

## Bağlam
Kullanıcı (2026-06-23): *"davinci'deki — daha doğrusu çalışacak conductor'daki — login'i editör üzerinden
yapamaz mıyım? Xirigo login gibi."* Gateway-mediated prod yolunda (ADR-0048) performer host'ta (davinci)
`claude -p` çalışır ve claude auth gerektirir. Şimdiye kadarki tek yol: SSH ile davinci'ye girip interaktif
`claude` login (tarayıcı/oauth, host-bağımlı keychain). Bu kullanışsız ve birden çok host'ta tekrarlanır.

**GROUNDING (xirigo `~/.xirigo/get-token.sh`):** xirigo `claude setup-token` çalıştırır → **taşınabilir,
uzun-ömürlü** OAuth token (`sk-ant-oat…`, bir yıl, host-bağımlı DEĞİL) → 0600 dosya → her headless tick bunu
kullanır. **Doğrulandı (Claude Code resmi dökümanı):** `claude setup-token` token'ı yalnızca terminale yazar
(hiçbir yere kaydetmez); headless `claude -p` onu **`CLAUDE_CODE_OAUTH_TOKEN`** env değişkeninden okur; token
taşınabilir (A makinesinde üret, B host'ta env ile kullan). Editör zaten gateway bearer'ını
`ConnectionManager`+VS Code SecretStorage ile yönetiyor (token webview'e girmez — ADR-0027).

## Karar
**Claude auth'u editörde bir kez mint et, taşınabilir token olarak conductor agent'(lar)ına ver.** İki faz:

- **L1 (editör-only):** `conductor.login` komutu entegre terminalde `claude setup-token` çalıştırır
  (tarayıcı oauth → CLI token'ı YAZAR), kullanıcı token'ı **password input**'a yapıştırır, token
  **SecretStorage**'a `CLAUDE_OAUTH_TOKEN_KEY` altında yazılır. `ConnectionManager` ADDITIVE genişler
  (gateway-token akışı + `#state` DOKUNULMAZ; ayrı `storeClaudeToken`/`hasClaudeToken`/`clearClaudeToken`,
  **getter YOK**). `conductor.logout` token'ı unutur.
- **L2 (host tüketimi):** agent süreç env'inde `CLAUDE_CODE_OAUTH_TOKEN` → develop subprocess'ine
  `command_engine.execRunner` üzerinden geçer (`Sanitize(os.Environ())+env`). `envsafe` denylist'i bu
  değişkeni **KORUR** (yalnız GH_TOKEN/GITHUB_TOKEN/CONDUCTOR_* strip'lenir). Agent, performer `claude` iken
  token yoksa **uyarır** (yalnız varlık kontrolü; değer asla okunmaz/loglanmaz). env.example + runbook
  editör-mint→host-env yolunu birincil (SSH'siz) olarak belgeler; interaktif login fallback.

## Token disiplini (SECURITY-CRITICAL)
Claude OAuth token **hesap-seviyesi** erişimdir; sızıntı = hesap ele geçirme. At-rest yalnız
SecretStorage(editör)/0600-file(host)/k8s-secret; in-transit yalnız authed kanal; **ASLA**
webview/log/postMessage/git/process-table'da. `setup-token` stdout'u biz YAKALAMAYIZ (kullanıcı kopyalar);
mesaj/log/sendText token echo'lamaz. Leak-guard testleri (connection.test + extension.test); emitted bundle
taraması yalnız input placeholder string içerir, token değil; webview bundle temiz.

## Sonuç
- L1 `de3b5c4` (editör; gate + GERÇEK fork electron smoke + CI 3-job yeşil).
- L2 `da0a8ea` (envsafe regression test + agent startup hint + env.example/runbook; Go gate + CI 3-job yeşil).
- **Frozen-additive (ADR-0021):** web/src + Go daemon yolları + gateway-token akışı dokunulmadı; gate sole
  merge authority kaldı; agent PG'ye dokunmaz.

## L3 ✅ SEVK — gateway-dağıtımlı şifreli credential-store (kullanıcı kararı: "L3'ü yap")
Editör (authed) claude token'ı gateway'in **şifreli at-rest** store'una yükler; agent başlangıçta authed FETCH
eder → host'ta hiç secret-file yok; editör = TEK login noktası. 4 additive katman (her biri CI 3-job yeşil):
- **L3a `8f4de59`** — `internal/credstore` (AES-256-GCM Seal/Open; fresh crypto/rand nonce; Open GCM-tag doğrular,
  hata opak; `KeyFromBase64`/`New` fail-closed) + statestore `CredentialStore` seam (Memory+PG, **yalnız
  ciphertext+nonce** persist; frozen StateStore dokunulmadı) + migration `00009_agent_credentials`. GERÇEK-PG
  conformance.
- **L3b `9588a3e`** — gateway `PUT/GET/DELETE /agent/credentials/{kind}` (hepsi requireAuth). Opsiyonel
  `*credstore.Sealer` (`CONDUCTOR_CREDENTIAL_KEY` k8s-secret'ten, base64-32B). **FAIL-CLOSED:** key yok → sealer
  nil → PUT/GET **503** (asla plaintext); non-empty-invalid key → HARD startup error. Token yalnız body/header,
  **asla loglanmaz** (decrypt-fail dahil; serverError op+shape-only). agentclient `PutCredential`/`GetCredential`
  (404→found=false)/`DeleteCredential`.
- **L3c `68a32c0`** — agent başlangıçta `ensureClaudeAuth`: env-CLAUDE_CODE_OAUTH_TOKEN öncelikli; yoksa
  gateway'den FETCH → `os.Setenv` (L2 wire; envsafe KORUR) → host'ta secret-file YOK, token yalnız süreç
  belleğinde. Bulunamazsa/hata non-fatal warn (değer asla loglanmaz). Non-claude performer → no-op.
- **L3d `1509e17`** — editör `conductor.pushCredential` (SecretStorage'dan claude token oku → authed upload) +
  `conductor.removeCredential`. Host-side `CredentialClient` (gateway bearer yalnız header, claude token yalnız
  body; CredentialResult kapalı enum, secret taşımaz). Token webview'e GİRMEZ (host-only; webview bundle taraması
  0 credential-ref). Editör gate + GERÇEK fork electron smoke.

**SECRET-LEAK ADVERSARIAL REVIEW ✅ (3 bağımsız mercek — crypto/at-rest · transit/logging · editör/webview):
HEPSİ TEMİZ, 0 crit/high/med/low.** Plaintext store'a girmez; nonce taze; tüm rotalar authed; hiçbir log/error
token/key/nonce taşımaz (decrypt-fail dahil); fail-closed (key-yok→503, invalid-key→hard-fail); editör akışı
host-only, getter yok, iki anahtar ayrı. Nit'ler severity-none (PUT body zaten `decodeJSONBody` ile capped; `kind`
parametreli değer, loglanmaz).

**KALAN (kullanıcı ops):** gateway'e `CONDUCTOR_CREDENTIAL_KEY` (base64 32-byte rastgele) k8s-secret olarak ver
(yoksa L3 endpoint'leri 503=fail-closed, L1+L2 env-yolu çalışmaya devam eder). Sonra editörde Log In → Push to
Gateway; agent'lar otomatik fetch eder.
