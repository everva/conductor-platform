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

## L3 (opsiyonel, açık) — gateway-dağıtımlı şifreli credential-store
Editör (authed) claude token'ı gateway'in **şifreli at-rest** credential-store'una yükler; agent başlangıçta
authed FETCH eder → host'ta hiç secret-file yok; editör = TEK login noktası. Envelope-encryption/sealed +
secret-leak adversarial review ŞART. L1+L2 çalıştıktan sonra; kullanıcı kararı.
