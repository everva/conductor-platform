# ADR-0053 — k8s intake distiller: subscription claude (Opus 4.8), per-call sealed token (Option 3)

## Bağlam
Kullanıcı (2026-06-24): intake'in claude-bağımlı `/distill` yolu prod'da (k8s gateway pod'u)
çalışmıyordu — pod'da `claude` CLI yok + `CLAUDE_CODE_OAUTH_TOKEN` env'de yok
(`docs/INTAKE-CLAUDE-IN-K8S-PLAN.md`). Güvenlik çatalı kullanıcıya bırakılmıştı. **Kullanıcı kararı:**
*"claude subscription kullanacağız, davinci'de yaptığımız gibi, Opus 4.8 olacak"* — yani API key
DEĞİL, subscription OAuth token (zaten kurulu L3 credential modeli) + Opus 4.8 model.

## Karar
Plan'ın **Opsiyon 3'ü** uygulandı (en dar maruziyet, gateway imajında claude):

1. **Subscription, API-key YOK** — distiller zaten `claude -p` (subscription auth); değişmedi.
2. **Per-call sealed-token (Option 3 token disiplini):** `internal/intake/distiller_real.go` —
   `ClaudeDistillerConfig{TokenProvider, Model}` + `NewCommandDistillerWithClaude`. `TokenProvider`
   set'liyse `claudeEnv` çözülen `CLAUDE_CODE_OAUTH_TOKEN`'ı YALNIZ subprocess env'ine ekler
   (`os.Setenv` YOK → gateway'in uzun-ömürlü env'i dokunulmaz). Provider hata → distill başarısız
   (sessiz kimliksiz çağrı YOK). nil provider → env'in kendi auth'u (frozen = davinci-yerel).
3. **Opus 4.8 model:** `claudeArgs` → `claude -p --model <model>`. Gateway `cfg.distillModel` =
   `CONDUCTOR_DISTILL_MODEL` (default **`claude-opus-4-8`**, override edilebilir). "" → model
   flag'siz (subscription default). Model seçimi deploy config'inde (davinci recipe gibi).
4. **Gateway wiring:** `main.go claudeTokenProvider(store, sealer)` — `cs.GetCredential(
   "CLAUDE_CODE_OAUTH_TOKEN")` → `sealer.Open` (agent'ın `handleAgentGetCredential` ile AYNI yol).
   store CredentialStore değilse / sealer nil ise → nil provider (frozen fallback).

**Neden Option 3:** account-seviyesi token gateway'in kalıcı env'inde DEĞİL (process table / tüm
gateway kodu okuyamaz); yalnız çağrı-başına, kısa-ömürlü subprocess env'inde. Opsiyon 1'den (kalıcı
env) çok daha dar; Opsiyon 2'den (ayrı pod) basit. Token DİSİPLİNİ: provider plaintext'i yalnız
distiller subprocess env'ine taşır; log/return/webview'e GİRMEZ.

Frozen-additive (ADR-0021): `NewCommandDistiller()` davranışı AYNI (boş config); web/editör/Go-frozen
dokunulmadı; yeni migration YOK (var olan credential store'u reuse eder).

## KALAN (bu ADR yalnız CODE seam'i kapsar)
- **IMAGE:** gateway Dockerfile'ına `claude` CLI (Node + `@anthropic-ai/claude-code`, pinned).
- **DEPLOY:** imaj build + ArgoCD + `CONDUCTOR_DISTILL_MODEL` (opsiyonel) → editörden `/distill`
  doğrula. (Outward-facing prod — kullanıcı haberdar.)

## Doğrulama
- `internal/intake/distiller_real_test.go`: `claudeArgs` (model on/off/blank); `claudeEnv`
  (nil-provider env değişmez; token YALNIZ subprocess env'inde + `os.Environ` mutasyon YOK = Option
  3 invariant; boş token eklenmez; provider hata propagate); iki constructor seam.
- `cmd/conductor-api/distiller_token_test.go`: `claudeTokenProvider` seal→PutCredential→fetch+decrypt
  round-trip; nil-sealer → nil provider; eksik credential → hata.
- Go gate GREEN (build+vet+golangci 0+gofmt) + `-race` temiz (intake + conductor-api).
