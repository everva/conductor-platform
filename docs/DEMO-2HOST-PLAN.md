# İki-makineli (Linux + macOS) capability-routed çok-proje demo — PLAN

> **DURUM (güncel):** **Faz A ✅** (committed `internal/conductor/multihost_e2e_test.go`
> — gerçek-PG, iki capability-ayrı Conductor, routing-safety + routed-merge + gate-authority,
> teeth-doğrulandı; + CANLI iki-süreç koşusu `deploy/demo-2host/run-demo.sh` GEÇTİ:
> host-linux iOS işini REDDETTİ + web'i MERGE etti, host-mac iOS'u MERGE etti, çapraz-bulaşma
> yok). **Faz B ✅** runbook `deploy/demo-2host/README.md` (Linux+macOS host kurulumu).
> **Faz C** = kullanıcının gerçek host'ları (claude girişli + Playwright/maestro+Xcode). Registry
> seviyesi routing/lease/reap ayrıca `internal/conductor/twohost_test.go` ile deterministik kanıtlı.


**Amaç:** Çok-host vizyonunu CANLI kanıtla — bir **Linux** host + bir **macOS** host, AYNI
Postgres'i paylaşır; bir **web projesi** (Playwright → visual-diff gate, ADR-0023) Linux'ta,
bir **iOS projesi** (maestro UI-flow gate, ADR-0023) Mac'te otonom kodlanıp KENDİ deterministik
kapısından geçer; kokpit (editör/web) canlı gözler. Görevler **capability routing** ile doğru
makineye gider (görev.requires ⊆ host.capabilities, ADR-0008).

## SERT KISIT (mekanizma değil, ortam)
Bu nested-agent sandbox'ında `claude` **subprocess** auth'u YOK ("Not logged in") — yani
`claude -p` motoru, Agent subagent'ları ve distill BURADA koşamaz. Bu yüzden demo iki katmanlı:
- **Faz A (sandbox'ta, deterministik performer)**: routing + multi-host + multi-proje + gate
  mekanizmasını claude'suz kanıtla (e2e_test.go'nun sh-performer deseni). GERÇEK kanıt, ama LLM
  kodlamayı değil — onu zaten e2e_realclaude + Faz-1→4 kanıtlıyor.
- **Faz C (kullanıcının GERÇEK host'ları)**: gerçek claude (girişli) + gerçek Playwright (Linux)
  + gerçek maestro+Xcode (Mac) + gerçek web/iOS repo'ları. Kullanıcı runbook'u koşar.

## Faz A — Yerel deterministik kanıt (sandbox'ta yapılabilir)
1. PG ayağa: `make db-up` (veya çalışan :5433).
2. İKİ daemon, AYNI `-dsn`, farklı capability + host:
   - `conductor -host host-linux -capabilities linux,web,backend  -develop-cmd <det-performer> ...`
   - `conductor -host host-mac   -capabilities macos,ios-build,maestro -develop-cmd <det-performer> ...`
   (develop-cmd = deterministik sh-performer; claude DEĞİL — sandbox kısıtı.)
3. İKİ throwaway repo onboard:
   - **web**: `playwright.config.ts` içeren repo → scaffolder StackWeb → visual-diff recipe;
     görev `Requires: [web]` (veya playwright).
   - **iOS-şekilli**: `maestro/` + `*.xcodeproj`/`Project.swift` işaretli repo → scaffolder
     StackIOS → maestro recipe; görev `Requires: [ios-build]`.
4. Intake → doğru `Requires` ile görevler.
5. **KANIT**: web görevinin lease'ini **host-linux** alır (capability eşleşmesi), iOS görevininkini
   **host-mac**. Yanlış-host görevi ALAMAZ (requires ⊄ capabilities → lease yok). Her görev
   deterministik performer → kapı → merge (veya gate-fail → blok). Visual/maestro gate argv'leri
   yerelde STUB (gerçek imagediff/maestro yok) — gate MEKANİZMASINI (pass→merge, fail→blok)
   kanıtlar; gerçek araç-koşumu Faz C'de. (Visual-diff gate zaten `TestE2E_VisualDiffGate` ile
   e2e-test'li.)
6. Rule#9: gerçek-PG cross-process; iki daemon GERÇEKTEN ayrı süreç; lease/heartbeat gözlemlenir.

## Faz B — Runbook + script (sandbox'ta yazılır)
- `docs/RUNBOOK-2HOST.md` (veya deploy/ altına): Linux host + Mac host için ADIM ADIM —
  ne kurulur (Linux: node+Playwright, git, claude-login; Mac: Xcode+maestro, git, claude-login),
  capability'ler nasıl set edilir, gateway nasıl ayağa kalkar (token), kokpit (editör/web) nasıl
  bağlanır, web+iOS repo nasıl onboard edilir.
- compose/k8s zaten var (`deploy/k8s/` daemon+api+pg+ingress); çok-host için host başına daemon
  Deployment + capability env.

## Faz C — Gerçek iki makine (kullanıcı koşar)
Linux + Mac gerçek host'larda runbook'u uygula → gerçek claude web projesini Linux'ta, iOS'u
Mac'te otonom sürsün, kendi kapılarından geçsin, kokpitten izlensin.

## Doğrulama standardı (DEĞİŞMEZ — her madde)
spec → (gerekirse) Agent'a kodlat (frozen oku→TDD→gate yeşil→commit) → **Rule#9 BAĞIMSIZ
doğrula** (gerçek-PG, build/test-race-count1/vet/golangci, `-tags e2e` her iki mod; editör/web
Playwright/gerçek-koşu KENDİM) → push develop(+fork) → ledger+memory → CI gh run watch yeşil →
sıradaki. Sahte-yeşil ASLA; deterministik-gate TEK merge mercii; frozen-additive (ADR-0021);
yalnız subscription claude; token/secret disiplini; optiway/xirigo'ya DOKUNMA; isolation:worktree
KULLANMA.

## Ana mimari gerçekler (doğrulanmış)
- Capability routing: `statestore.go:111` (requires⊆capabilities, ADR-0008); host `Capabilities`,
  görev `Requires`.
- Scaffolder auto-detect: web=`playwright.config.*`→visual-diff gate; iOS=`maestro/`+`*.xcodeproj`/
  `Project.swift`→maestro gate (`internal/scaffolder/scaffolder.go`, ADR-0023). `CapabilityIOSBuild="ios-build"`.
- Daemon flag'leri: `-project -dsn -root -develop-cmd -host -capabilities -interval -reconcile
  -governance -sentinel -http-addr -lease-ttl -host-cap -global-cap -heartbeat` (cmd/conductor).
- Deploy: `Dockerfile`, `docker-compose.yml`, tam `deploy/k8s/` (daemon+api+pg+ingress+reconcile/
  stallcheck cronjob); `make compose-up`, `make db-up`. defaultDevelopCmd=`claude -p`.
- Kokpit: gateway `conductor-api` (token'lı HTTP/WS) → Conductor Editor.app (imzalı+notarize .dmg,
  fork `e18aeed`) VEYA web cockpit. Token SecretStorage'da, webview'e girmez.
