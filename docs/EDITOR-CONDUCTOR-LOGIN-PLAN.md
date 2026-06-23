# Editör-aracılı Conductor login (xirigo modeli) — plan

Kullanıcı (2026-06-23): *"davinci'deki — daha doğrusu çalışacak conductor'daki — login'i editör üzerinden
yapamaz mıyım? Xirigo login gibi, editör üzerinde conductor'lara login olabilmek istiyorum."* Yani SSH'le
davinci'ye girip `claude` login etmek yerine, **editörden tek tıkla conductor agent'larının auth'unu sağlamak.**

> ⚠️ DİSİPLİN (Q3c/Faz-G ile AYNI): UYDURMA YOK · frozen-additive (ADR-0021) · HER task Go gate
> (build+vet+golangci-0+`-race`) + GERÇEK-PG (gerekirse) + editör gate (tsc×2+eslint-0+vitest+esbuild) +
> GERÇEK fork electron smoke + CI 3-job yeşil (`gh run view`) + ledger/ADR. **Bu PLAN; impl compact sonrası.**
> **EK DİSİPLİN — bu feature TAMAMEN SECRET'lerle ilgili → token-disiplini #1 KISIT:** claude OAuth token
> hesap-seviyesi erişimdir; at-rest yalnız SecretStorage(editör)/0600-file(host)/k8s-secret, in-transit yalnız
> authed kanal; **ASLA log/webview/postMessage/git/process-table'da.** Sızıntı = hesap ele geçirme.

## §0 — GERÇEK KAYNAK (grounding)
- **xirigo modeli** (`~/.xirigo/get-token.sh`): `claude setup-token` çalıştırır → tarayıcı oauth → **taşınabilir
  uzun-ömürlü token** (`sk-ant-oat…`) → `~/.xirigo/oauth-token` (chmod 600) → her headless tick bunu kullanır.
  **KRİTİK:** claude auth host-bağımlı keychain DEĞİL; `setup-token` ile taşınabilir bir token mint edilir → bir
  kez (tarayıcılı makinede) üretilip HERHANGİ bir host'ta (davinci) env ile `claude -p`'ye verilebilir.
- **conductor agent** (`cmd/conductor-agent`): develop-cmd `claude -p`. envsafe.Sanitize CONDUCTOR_*/GH_TOKEN
  strip eder ama **claude subscription auth'u (oauth) KORUR** (distiller_real.go yorumu). → agent env'inde claude
  oauth token DURABİLİR.
- **editör mevcut auth** (`editor/src/connection.ts` `ConnectionManager` + `GATEWAY_TOKEN_KEY`): "connect"
  akışı gateway token'ını alır → probe eder → **VS Code SecretStorage**'a yazar (token webview'e GİRMEZ; ADR-0027).
  Yani editör ZATEN bir secret-yöneten login akışına sahip; bu feature onu **claude OAuth token + agent provizyon**
  ile genişletir.

## §0.1 — DOĞRULANACAK (impl ilk adımı; uydurma yok)
- `claude setup-token`'ın ÜRETTİĞİ token'ın headless `claude -p`'ye geçtiği KESİN env değişkeni
  (`CLAUDE_CODE_OAUTH_TOKEN` muhtemel) — **claude-code-guide skill'i / gerçek claude CLI ile teyit et.**
- `envsafe.Sanitize`'in bu env değişkenini KORUDUĞU (strip etmediği) — `internal/envsafe` testiyle teyit.
- xirigo tick'lerinin ~/.xirigo/oauth-token'ı claude'a tam olarak NASIL verdiği (env mi, --token mı) — referans.

## §1 — Hedef
Editörde **"Conductor'a Login"** akışı: (1) claude OAuth token'ını mint et (`claude setup-token`, tarayıcı) +
güvenli sakla, (2) gateway token'ını yönet (mevcut connect), (3) bu kimlikleri çalışacak **conductor agent(lar)ına
provizyon et** — kullanıcı bir daha SSH'le claude login yapmasın. Xirigo'daki "editörde login" deneyiminin conductor
karşılığı.

## §2 — Mimari seçenek (provizyon; impl-fazında karar, plan'da öneri)
İki secret var: **gateway-token** (CONDUCTOR_AGENT_TOKEN) + **claude-oauth-token**. Agent ikisine de ihtiyaç duyar.
Agent UZAK (davinci) olduğundan editör→agent provizyon yolu kritik:
- **A) Token-file (xirigo-faithful, en basit):** editör token'ı mint eder + saklar; setup-script/runbook agent
  host'unun env-file'ına yazar (`CLAUDE_CODE_OAUTH_TOKEN`, 0600). Minimal; editör "mint + göster/SCP" yardımcısı.
- **B) Gateway-dağıtımlı (gateway-mediated felsefesiyle uyumlu, EN TEMİZ ama en büyük):** editör (authed) claude
  token'ını **gateway'in şifreli credential-store**'una yükler; agent başlangıçta kendi claude token'ını gateway'den
  **authed FETCH** eder. Editör = tek login noktası; gateway tüm conductor'lara dağıtır. Tam "xirigo-gibi editör
  login". AMA: gateway'de secret-at-rest şifreleme + credential endpoint + agent fetch = çok-katman; secret-store
  güvenliği kritik (envelope encryption / sealed-secret / KMS).
- **ÖNERİ:** L1+L2 (A) ile başla (minimal, xirigo-faithful, hızlı kullanılır); L3 (B) temiz follow-up.

## §3 — FAZLAR (her faz gate+CI yeşil; token-disiplini #1)
**Faz L1 — editörde claude-token mint (editör-only)** [ADR-0049]
- `editor/src/` yeni `conductor.login` komutu: `claude setup-token`'ı bir VS Code terminal/process ile çalıştırır
  (tarayıcı açılır) → token'ı yakalar (kullanıcı yapıştırır VEYA stdout parse) → **SecretStorage**'a yazar
  (`CLAUDE_OAUTH_TOKEN_KEY`). Token webview'e/log'a/postMessage'a GİRMEZ (mevcut gateway-token disiplini aynen).
  ConnectionManager'ı genişlet (claude-token slot'u). Editör gate + electron smoke. **Hiçbir Go yok.**
- Doğrulama: connection.test'e claude-token store/clear + leak-guard testleri; emitted-bundle token içermez denetimi.

**Faz L2 — agent claude-token tüketimi + provizyon (host)** [ADR-0049]
- §0.1 teyidi: agent env'ine `CLAUDE_CODE_OAUTH_TOKEN` (doğru isim) ekle; envsafe-KORUR testi. `cmd/conductor-agent`
  develop subprocess'ine claude token'ı geçer (env; develop-cmd `claude -p` onu kullanır). Go gate.
- `deploy/conductor-agent/` runbook + env.example güncelle (CLAUDE_CODE_OAUTH_TOKEN; "editörde mint et → buraya koy").
  Editör "token'ı agent host'una yaz" yardımcısı (opsiyonel SCP) VEYA kullanıcı kopyalar. **claude login artık SSH
  gerektirmez — editörde mint, env'e koy.**

**Faz L3 (opsiyonel/temiz) — gateway-dağıtımlı credentials** [ADR-0049/sonraki]
- gateway: authed `POST /agent/credentials` (editör yükler, ŞİFRELİ at-rest) + `GET /agent/credentials` (agent
  başlangıçta authed fetch). Secret-store: envelope-encryption/sealed. Agent claude token'ı ENV yerine gateway'den
  alır → host'ta hiç secret-file yok. Editör = TEK login; tüm conductor'lar gateway'den auth. Frozen-additive +
  GERÇEK-PG + secret-leak adversarial review ŞART. (Büyük; L1+L2 çalıştıktan sonra.)

## §4 — Frozen-additive + güvenlik ÇİZGİSİ
- Mevcut gateway-token connect (ConnectionManager) DOKUNULMADAN genişler (claude-token additive slot). Go agent
  env-okuma additive. Mevcut testler kırılmaz.
- **Token-disiplini (SECURITY-CRITICAL):** claude OAuth token at-rest yalnız SecretStorage/0600/k8s-secret;
  in-transit authed; webview/log/postMessage/git/process-table'da ASLA. setup-token çıktısı log'lanmaz. L3'te
  at-rest şifreleme şart. Her faz leak-guard testi + (L3'te) adversarial secret-review.
- **GOTCHA (Faz-Q/G'den):** vitest `vi.fn<typeof fetch>()` · cross-dir dep · `gh run view` (watch yanıltır) ·
  self-hosted runner backlog · fork inject SONRASI `codesign --force --deep --sign -` + `open -n` · editör menü-komut
  package.json'da DEKLARE.

## DURUM
- Plan yazıldı; xirigo (`claude setup-token`→taşınabilir oauth token) + editör ConnectionManager + agent envsafe
  grounded. PROD gateway-mediated sistem ZATEN canlı (develop @ `236fac3`; [[conductor-prod-gateway-mediated]]) —
  bu feature onun login-DX'ini editöre taşır. ADR-0049 (`docs/decisions/0049-...`).
- **§0.1 DOĞRULANDI (uydurma yok):** (1) headless env değişkeni = **`CLAUDE_CODE_OAUTH_TOKEN`** (Claude Code resmi
  dökümanı; `setup-token` token'ı yalnız terminale yazar, taşınabilir/bir-yıl). (2) `envsafe.Sanitize` bu değişkeni
  **KORUR** (denylist; `internal/envsafe/envsafe.go:48` + test).
- **L1 ✅ SEVK `de3b5c4`** (editör-only; CI 3-job yeşil): `conductor.login`/`logout` (ADR-0049) — entegre terminalde
  `claude setup-token` → password input → SecretStorage `CLAUDE_OAUTH_TOKEN_KEY`. `ConnectionManager` ADDITIVE
  (gateway-token akışı + `#state` DOKUNULMADI; ayrı store/has/clear, **getter YOK**). `LoginVscodeApi` ayrı dar
  yüzey (DiffVscodeApi gibi). Leak-guard testleri (connection.test + extension.test) + emitted-bundle taraması
  (yalnız placeholder string) + **GERÇEK fork electron smoke** (VS Code 1.125.1, komutlar register, N1 startup yeşil).
- **L2 ✅ SEVK `da0a8ea`** (CI 3-job yeşil): performer `CLAUDE_CODE_OAUTH_TOKEN`'ı tüketir — wire ZATEN vardı
  (`command_engine.execRunner` = `Sanitize(os.Environ())+env`, envsafe KORUR); bu commit pinler+belgeler:
  `TestSanitize_PreservesClaudeOAuthToken` (L1↔L2 regression), agent startup WARN (claude performer + token-yok;
  varlık-only, değer asla loglanmaz) + `usesClaudePerformer`+main_test, env.example + runbook editör-mint→host-env
  birincil/SSH'siz yol. Go gate (build+vet+golangci-0+`-race`).
- **→ L1+L2 İŞLEVSEL TAMAM**: editörde tek-tık mint → host env → SSH'siz claude login. **KALAN (kullanıcı):**
  davinci'de `conductor-agent.env`'e token'ı koy (veya editör helper); **L3 opsiyonel — kullanıcıya soruldu.**
