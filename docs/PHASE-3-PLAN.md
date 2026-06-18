# Faz-3 Planı — İnsan Cockpit'i (web cockpit + API gateway; fork'a köprü) [compact-proof çıpa]

> Faz-1 (1a+1b+P3+P4+dogfood) ve Faz-2 (Faz-1.5 + Dalga A/B/C + adversarial review + R-1/R-2/R-3 fix)
> TAMAM, reviewed, hardened, **gerçek pilotla canlı kanıtlandı** (Kontaktör `conductorctl hosts`'u otonom
> üretti → cc34297). Bu plan Faz-3'ü tanımlar. develop push'lu, 20 paket gate+e2e+race yeşil.

## Karar (kullanıcı, 2026-06-18): Web cockpit + API gateway — fork'a köprü
Uzun vade **editör fork'u** (devin/cursor-tarzı ajan-yönetimi) hedef; AMA şimdi web cockpit + API gateway ile
başlıyoruz çünkü **çöp değil, fork'a giden zorunlu köprü:** (1) **API gateway kalıcı** — web cockpit, ileride
fork, CLI/TUI hepsi onu kullanır; (2) web frontend **taşınabilir** — fork'lar panelleri webview ile render eder,
React/TS bileşenler + N-9 tipli event-client fork'a gömülür; atılan tek şey küçük web kabuğu. Fork'u şimdi
yapmak erken/riskli (çalışan cockpit'i kullanmadan devasa bakım). Faz-4 = fork, gateway'i reuse eder.

## Mekanizma (Faz-1/2 disiplini — DEĞİŞMEZ)
Her iş: orchestrator spec yazar → **Agent kodlar** (TDD, gate yeşil, frozen kontratlara ADDITIVE, sahte-yeşil
ASLA) → orchestrator **BAĞIMSIZ gate + gerçek-koşu doğrular (Rule#9, self-report'a güvenme)** → squash-merge
develop → push. Mimari karar → önce **ADR**. engine.go + statestore imzaları frozen (additive, ADR-0021).
Secret-leak yok. Canlı optiway(`~/optiway-conductor`)/xirigo'ya DOKUNMA. Yalnız subscription `claude -p`.
Yalnız `develop`'a merge+push. Her dalga sonu bağımsız doğrulama + İLERLEME KAYDI güncelle.

## Yeni ADR ihtiyaçları
- **ADR-0025** — API gateway: ayrı servis (`cmd/conductor-api`) mı daemon-genişletme mi (öneri: AYRI servis —
  read+control; daemon'lar işi yapar); REST + WebSocket; auth (bearer token); events/control/state seam eşlemesi;
  fork-köprü kontratı (frontend-agnostik API).
- **ADR-0026** — Frontend stack + auth/session: React+Vite+TS, N-9 TS event tipleri, token auth, frontend gate
  (tsc + eslint + vitest + Playwright). UI repo'da (`web/` veya `ui/`), kendi reçetesiyle (scaffolder web profili).

---

## DALGA 3A — API gateway (Go; kalıcı temel)
- **3A-0 — KARAR ADR-0025** (ÖNCE): ayrı `cmd/conductor-api` servisi; REST+WS; bearer-token auth; statestore/
  events/control paketlerini reuse; frontend-agnostik kontrat. Güvenlik: read-mostly + control; daemon'a yazma
  yok (control mevcut store/seam üzerinden).
- **3A-1 — Read API (REST):** `GET /projects`, `/projects/{id}/tasks`, `/hosts` (capabilities+heartbeat-age),
  `/status`, `/events?project=&since=`. Bearer-token. Tipli JSON. Kabul: auth'lu endpoint'ler StateStore'dan
  doğru veri döndürür; deterministik testler + (skip-gated) real-PG.
- **3A-2 — Live events (WebSocket):** `/ws` → events bus'a (PG LISTEN/NOTIFY, N-9) abone → tarayıcıya tipli
  event push; proje-filtresi. Kabul: docker-PG ile bir event publish → WS client alır (bağımsız doğrula).
- **3A-3 — Control API (REST POST):** `onboard/intake/pause/resume/abort/approve` (conductorctl mantığı/store +
  controller seam reuse), auth-gated, aynı deterministik aksiyonlar. Kabul: her control endpoint store'u doğru
  değiştirir; T3 approve→merge canlı (gateway üzerinden).
- **3A-4 — Gateway paketleme:** Dockerfile + k8s (Deployment+Service+Ingress, P4 deseni; non-root; secret=token/DSN).
  Kabul: docker build + kustomize/kubectl dry-run valid.

## DALGA 3B — Web cockpit (React/TS frontend, gateway üzerinden)
- **3B-0 — KARAR ADR-0026 + iskele:** React+Vite+TS + N-9 generated TS event tipleri + token auth + frontend gate
  (tsc/eslint/vitest/playwright) → `.conductor` web reçetesi. Kabul: `web/` iskele, gate yeşil, gateway'e bağlanır.
- **3B-1 — Filo dashboard:** projeler/host'lar/task'lar canlı (REST + WS); status/lease/outcome/heartbeat-tazeliği.
- **3B-2 — Canlı event akışı:** WS-tabanlı event feed (phase/kind; **intervention-needed** vurgulu).
- **3B-3 — Müdahale:** T3/T4 **approve**, pause/resume, abort — UI'dan control API ile.
- **3B-4 — Intake chat:** yazışma → distiller (claude -p, sunucu-tarafı) → önerilen senaryo+holdout → insan
  review+onay → ledger'a. "Yazışarak senaryo üret" ön kapısı (kullanıcının çekirdek vizyonu).

## DALGA 3C — Uçtan-uca + sertleştirme
- **3C-1 — Cockpit e2e demo:** UI'dan gerçek bir task sür (intake-chat→senaryo→Kontaktör'ü izle→approve→merge);
  auth sertleştirme; cockpit'in kendi CI gate'i (Go + frontend). Kabul: uçtan-uca canlı, bağımsız doğrulanır.

## Sıra / bağımlılık
3A (gateway, frontend-bağımsız temel) → 3B (frontend, gateway'e bağımlı) → 3C. 3A-0/3B-0 kararları kendi
dalgalarının başında. Faz-2 minör follow-up'ları (aşağıda) paralel/araya alınabilir.

## Faz-4 köprü (fork — sonra)
Gateway (3A) olduğu gibi reuse; web bileşenleri (3B) fork panellerine (webview) taşınır; fork = "yeni frontend",
"sıfırdan" değil. Bu plan boyunca API'yi frontend-agnostik tut.

## Faz-2'den taşınan minör follow-up'lar (blocker DEĞİL; fırsat olunca)
- Gerçek-claude *advisor* runner smoke + private-repo *auth-fail* testi (2 düşük-riskli kapsam boşluğu).
- Sentinel Layer-1 zengin process-liveness probe (şu an Signal-wire + backstop; "clearly-dead→Kill" prod'da inert).
- iOS/maestro CANLI koşum gerçek Mac-host'ta (reçete+routing hazır; infra bekliyor).
- Recipe argv tam sandbox (S-3; şu an doküman + shell-argv uyarısı + R-2 secret-containment).

## İLERLEME KAYDI (her iş bitince güncelle)
- Faz-3 planı oluşturuldu (2026-06-18).
- ✅ **3A-0 — ADR-0025 API gateway kararı** (2026-06-18): ayrı `cmd/conductor-api` servisi; read+control; daemon
  DEĞİŞMEZ; veri paylaşılan store/bus; kontrol mevcut conductor seam reuse (store-yansıması, daemon'a doğrudan
  komut YOK); REST+WS, bearer-auth; frontend-agnostik (fork köprüsü). Frozen kontratlara etki: yok (salt tüketim).
- ✅ **3A-1 — Read API (REST)** (2026-06-18, commit `5f3daf7`): yeni ayrı `cmd/conductor-api` servisi —
  `GET /projects`, `/projects/{id}/tasks` (404 unknown), `/hosts` (caps+heartbeat-yaşı), `/status` (filo
  aggregate: projects/hosts/leases), `/healthz`, `/readyz`; bearer-token auth (env-only `CONDUCTOR_API_TOKEN`,
  constant-time, empty→başlamayı reddet); saf-okuma frozen StateStore üzerinden (sıfır ekleme). **Kapsam notu:**
  `/events?since=` historical REST 3A-1'den ÇIKARILDI → EventBus'ta geçmiş-sorgu seam'i yok; doğal yeri 3A-2
  (live events), orada additive bir event-reader seam'iyle birlikte gelecek (sessiz düşürme değil, bilinçli sıra).
  Bağımsız doğrulama (Rule#9): frozen dokunulmadı + gate (build/vet/golangci-0/fresh-race) + **canlı cross-process**
  (conductorctl→docker-PG yazdı, conductor-api aynı DSN'den gerçek veri okudu) + auth(401/200) + empty-token-refuse
  + secret-leak=0, hepsi kanıtlandı.
- ✅ **3A-2a — additive EventReader seam** (2026-06-18, commit `9100e73`): frozen `EventBus` DOKUNULMADAN yeni
  `events.EventReader` arayüzü + `ListEvents(filter, since, limit)` — PostgresBus gerçek SQL (events tablosu,
  idx_events_project_ts; parameterized) + MemoryBus bounded retained-history (additive `history`/`WithHistoryCap`).
  Default/Max limit 200/1000; since-inclusive; earliest-limit (sayfalama için since-ilerlet). Bağımsız doğrulama
  (Rule#9): frozen bus.go/event.go diff=∅, gate (build/vet/golangci-0/race) + **real-PG ListEvents** (6 alt-test:
  proje/task/intervention filtre, since-cutoff, limit-earliest, closed) kendi koştum → PASS.
- ✅ **Yan-bulgu düzeltme** (commit `8135d80`): `TestRunCheck_ExitCodes` time-bomb'ı — FRESH heartbeat sabit geçmiş
  tarihte (12:00 UTC) damgalanıyordu, 13:00 UTC sonrası STALE'e dönüp gate'i kırıyordu. `time.Now()`'a çevrildi
  (ürün runCheck doğruydu; salt test fixture hatası). Agent'ın "pre-existing+alakasız" raporu bağımsız doğrulandı.
- Sıradaki: **3A-2b** (conductor-api `/ws` WebSocket live-push + `GET /events?project=&task=&since=&limit=` REST).
