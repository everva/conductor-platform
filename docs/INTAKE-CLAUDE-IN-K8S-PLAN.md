# Intake distiller needs claude in k8s — design + security fork (kullanıcı 2026-06-23)

Kullanıcı: *"intake kısmında da claude instance olmalı; gerekirse k8s'e claude koyalım,
editörden login olabileyim."* Bu plan, intake'in claude-bağımlı `/distill` yolunu prod'da
çalışır hale getirmenin yollarını + **güvenlik çatallanmasını** belgeler. **Karar kullanıcının**
(account-seviyesi claude token'ının nerede durduğu = güvenlik modeli); uygulamayı onaydan sonra yaparım.

## Mevcut durum (kanıtlı)
- Editör Intake'in İKİ yolu var: (a) **"Write spec directly"** — claude-SİZ, deterministik YAML
  intake (`POST /intake`, `intake.LoadYAML`) — **prod'da ÇALIŞIYOR**; (b) **"Distill"** — sohbeti
  senaryolara çeviren claude yolu (`POST /distill`).
- Gateway zaten prod distiller olarak `intake.NewCommandDistiller()` (gerçek `claude -p`) wire'lı
  (`cmd/conductor-api/main.go:178`). `claudeRunnerWithPrompt` → `exec.LookPath("claude")` →
  `claude -p`, env `envsafe.Sanitize(os.Environ())` (CLAUDE_CODE_OAUTH_TOKEN KORUNUR, CONDUCTOR_*/GH
  strip'lenir).
- **Eksik:** gateway POD'unda (a) `claude` CLI YOK (Go scratch/distroless imajı), (b)
  CLAUDE_CODE_OAUTH_TOKEN env'de YOK. → `/distill` şu an pod'da exec-fail (claude bulunamaz).
- **L3:** account claude OAuth token gateway'in **sealed** credential store'unda (AES, PG). Tasarım
  gereği token YALNIZ geçici olarak çözülür (davinci agent fetch+os.Setenv eder). Token'ı gateway'in
  kalıcı env'ine koymak L3'ün sealed modelinden GERİ ADIM (gateway en çok maruz kalan bileşen).

## Güvenlik çatallanması (KULLANICI KARARI)
Account-seviyesi claude token'ı = kaçarsa hesap riski. Üç model:

- **Opsiyon 1 — gateway imajına claude + startup'ta env'e token.** Gateway başlangıçta kendi sealed
  claude credential'ını çözer → `os.Setenv(CLAUDE_CODE_OAUTH_TOKEN)`. Var olan distiller (os.Environ
  inherit) çalışır.
  - ➕ En basit (kod ~10 satır + imaj).  ➖ Token gateway'in KALICI env'inde (process table, tüm
    gateway kodu okur) — en yüksek maruziyet.
- **Opsiyon 2 — ayrı claude-distiller Deployment/sidecar.** Token + claude YALNIZ o pod'da; gateway
  ona HTTP ile distill çağırır.
  - ➕ Token gateway'den İZOLE; en iyi blast-radius.  ➖ Yeni servis + HTTP protokolü + gateway
    distiller'ı HTTP client'a döner (daha çok kod/operasyon).
- **Opsiyon 3 (ÖNERİLEN) — gateway imajına claude + ÇAĞRI-BAŞINA decrypt, YALNIZ subprocess env'i.**
  Gateway `os.Setenv` YAPMAZ; her `/distill`'de sealed token'ı çözüp SADECE distiller
  subprocess'inin env'ine koyar (gateway'in kendi uzun-ömürlü env'ine değil).
  - ➕ Token gateway'in kalıcı env'inde DEĞİL; yalnız kısa-ömürlü subprocess env'inde, çağrı başına.
    Claude izolasyonu (Opsiyon 2) kadar güçlü değil ama Opsiyon 1'den çok daha dar.  ➖ Claude CLI
    yine gateway imajında; `CommandDistiller`'a token-provider seam'i gerekir (kod).

## Önerilen yol (Opsiyon 3) — uygulama planı (onay sonrası)
1. **CODE (additive, test'li):** `CommandDistiller`'a opsiyonel `tokenProvider func(ctx) (string,error)`
   ekle. Set'liyse `claudeRunnerWithPrompt` subprocess env'ine `CLAUDE_CODE_OAUTH_TOKEN=<token>`
   ekler (gateway'in os.Environ'ına DOKUNMADAN). Provider yoksa davranış AYNI (frozen). Gateway
   `main.go` provider'ı credential store + sealer'dan çözecek şekilde wire eder (503 if no key,
   "no credential" → mevcut 501/502 distill mapping). Go gate + GERÇEK-PG + distiller_test.
2. **IMAGE:** gateway Dockerfile'ına `claude` CLI ekle (`npm i -g @anthropic-ai/claude-code`; Node
   gerekir → imaj boyutu artar; ya da multi-stage + node runtime). Pin sürüm.
3. **DEPLOY:** imaj build + ArgoCD; doğrula `POST /distill` editörden senaryo/clarify döndürür.
4. **EDİTÖR:** Intake "Distill" yolu zaten var; distiller-yok (501/502) durumunda "Write spec
   directly"ye zarif düşüş mesajı (E-polish; bağımsız yapılabilir).

## Dürüst sınır / şimdilik
- Bu gece UYGULANMADI: prod gateway imajı + account-token maruziyeti = kullanıcı güvenlik kararı
  gerektirir (yukarıdaki çatal). Editörde **"Write spec directly" claude-siz intake ZATEN
  çalışıyor**, yani iş yaratma BLOKE değil — yalnız sohbet→senaryo distill claude bekliyor.
- Karar verince: Opsiyon 3'ün CODE seam'ini (test'li, deploy-gated, davranış-değişmez) + imaj +
  deploy'u yaparım.
