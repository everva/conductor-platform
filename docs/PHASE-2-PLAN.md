# Faz-2 Planı — çok-host + çoklu reçete + sentinel L2 (compact-proof çıpa)

> Faz-1 (1a çekirdek + 1b + üretim sertleştirme + dogfood) bitti. Bu plan Faz-2'yi tanımlar.
> Kullanıcı kararları (2026-06-18): çok-host GERÇEK ihtiyaç (Mac+Linux); proje tipleri = Go/backend +
> web-görsel + iOS-maestro + Node/Python (hepsi); sentinel Katman-2 DAHİL; Faz-1 açıkları Faz-2'den ÖNCE.
> Mekanizma Faz-1 ile aynı: her iş için spec → Agent kodlar → orchestrator BAĞIMSIZ gate+gerçek-koşu doğrular
> (Rule#9) → squash-merge develop → push. Frozen kontratlara additive (ADR-0021); engine.go imzaları frozen.

## Hedef
Tek makinede kanıtlı platformu (a) **birden çok host'a** (capability-routing: iOS→Mac), (b) **birden çok proje
tipine** (Go/web/iOS/Node/Python reçeteleri, xirigo-tarzı görsel/maestro 1:1 dahil), (c) **gri-bölge
canlılık-zekâsına** (sentinel L2) genişletmek — determinizm ve "sahte-yeşil asla" garantisini bozmadan.

---

## ÖNKOŞUL — Faz-1.5 (küçük açıkları temizle, Faz-2'den önce)
- **1.5-a — Holdout şemaları tamamla:** `internal/holdout`'a `pg://` (Postgres-backed holdout store) + `private:`
  (ayrı git repo) Fetch impl'leri (şu an yalnız `store://` filesystem). Kabul: 3 şema da fetch+inject; deterministik
  testler; gate yeşil.
- **1.5-b — T3/T4 human-hold CANLI demo:** gerçek daemon + PG ile T3 görev → gate-pass ama governance HUMAN-hold →
  merge YOK + intervention-needed event; conductorctl ile "approve→merge" akışı (gerekirse küçük additive `approve`
  komutu). Kabul: canlı tick'te T3 held + onayla→merge, bağımsız doğrula.
- **1.5-c — GitMerger remote-push modeli:** başarılı squash-merge sonrası opsiyonel `git push origin <base>`
  (flag/config ile; gh-token auth; default kapalı=yerel). Kabul: gerçek throwaway remote'a push doğrulanır; auth
  yoksa temiz hata; push opsiyonel (yerel-merge davranışı korunur). **Yeni karar gerekebilir → ADR-0022 (deploy/push modeli).**

---

## FAZ-2 — dalgalar (bağımlılık sırasıyla)

### Dalga A — Çoklu reçete (tek-host; hızlı değer, çok-host'tan bağımsız başlar)
Reçete = CommandEngine config (develop-cmd + verify gate'leri + verify-tipi). 5-fiil arayüz frozen; "yeni tip=yeni reçete".
- **2A-1 — Reçete altyapısını tamamla:** `.conductor/config.yaml` reçetesini uçtan-uca tam pipe (develop-cmd +
  gate argv + verify-tipi); scaffolder profillerini (N-8) gerçek çalışan reçetelere bağla. **Node + Python reçeteleri**
  (build/test/lint argv — çoğu hazır). Kabul: throwaway Node + Python repo'da onboard→gerçek gate→merge, canlı.
- **2A-2 — Web görsel-diff reçetesi:** xirigo deseni — claude-design çıktısı ↔ **deterministik görsel-diff** (piksel/
  threshold exit-code) + Playwright/headless e2e. Yeni verify-tipi: görsel-kanıt Check (deterministik; ADR-0003/0018
  uyumlu, öznel-skor YOK). **Yeni karar → ADR-0023 (görsel-verify reçetesi).** Kabul: bir web görevinde görsel-diff
  fail→blocked / pass→merge, canlı.
- **2A-3 — Mobil/iOS maestro reçetesi:** maestro UI-flow + görsel-diff reçetesi. iOS-build Mac gerektirir →
  **tam çalışması Dalga B'ye (çok-host) bağımlı**; reçete + verify Dalga A'da tanımlanır, Mac-host'ta Dalga B'de koşar.
  Kabul: maestro reçetesi + deterministik UI-doğrulama tanımlı; Mac-host hazır olunca canlı.

### Dalga B — Çok-host (remote executor) — en büyük ayak
Zemin hazır: Postgres lease host-üstü "repo başına 1" atomik (N-4) + governor host-yük tavanı. ADR-0008 "merkezi registry (B)".
- **2B-0 — KARAR: remote-executor modeli → ADR-0022/ayrı ADR:** ssh-ile-uzak-komut mu, yoksa uzak host'ta
  **conductor-agent** mı (her host'ta hafif agent, merkezi PG'den lease alır)? Güvenlik/auth/workspace modeli. ÖNCE bu.
- **2B-1 — Host registry + capabilities:** `hosts` + `lanes` tabloları (host kimlik + `capabilities`; lane→`requires`);
  host kayıt + heartbeat. Kabul: çok-host registry; conformance testleri.
- **2B-2 — Capability routing:** picker `requires ⊆ host.capabilities` ile task'ı uygun host'a yönlendirir
  (lane-seviyesi, ADR-0008). Kabul: iOS-lane yalnız ios-build yetenekli host'a düşer; deterministik test.
- **2B-3 — Remote executor:** seçilen modele göre (2B-0) tick develop'ını uzak host'ta koştur (workspace/provisioner
  uzak); merkezi PG state + event. Kabul: iki-host kurulumda (yerel simüle: 2 agent) bir görev uzak host'ta develop→
  verify→merge; "repo başına 1" host-üstü kanıtlanır (iki host aynı repoya yazamaz).
- **2B-4 — Host-başına kaynak cap + governor çok-host:** global cap + host-başına cap + host-yük; Mac≠Linux kapasite.

### Dalga C — Sentinel Katman-2 (gri-bölge LLM-danışman)
- **2C-1 — Gri-bölge dedektörü + LLM-danışman:** deterministik taban "emin değilim" (process canlı AMA çıktı durdu)
  → `claude -p` danışman dar çıktı `{ilerliyor|sıkışmış|insan-gerek}` + 1 cümle → aksiyon; **Katman-3 backstop yine
  son söz** (LLM "bekle" dese de mutlak tavan bağlar). reconcile/heartbeat'e gömülü, event yayar. Kabul: gri-bölge
  senaryosunda L2 doğru sınıflar; backstop her zaman kazanır (deterministik test + stub danışman); LLM gate-dışı.

---

## Sıra / bağımlılık
1. **Faz-1.5** (a,b,c) — paralel/hızlı, Faz-2 öncesi.
2. **Dalga A** (2A-1 → 2A-2 → 2A-3) — çok-host'tan bağımsız, erken değer.
3. **Dalga B** (2B-0 KARAR → 2B-1 → 2B-2 → 2B-3 → 2B-4) — en büyük; 2A-3'ün iOS canlısı buna bağlı.
4. **Dalga C** (2C-1) — bağımsız; herhangi bir noktada paralel ele alınabilir.

## Yeni ADR ihtiyaçları
- **ADR-0022** — remote-executor modeli (ssh vs agent) + (1.5-c) remote-push/deploy modeli.
- **ADR-0023** — görsel-verify reçetesi (deterministik görsel-diff; claude-design ↔ maestro/Playwright 1:1).

## Değişmez ilkeler (Faz-1'den taşınan)
Deterministik kanıt = kalite kapısı (LLM asla karar mercii); sahte-yeşil ASLA; orchestrator BAĞIMSIZ doğrular
(Rule#9); frozen kontratlara additive; secret-leak yok; her merge gate'li; canlı optiway/xirigo'ya dokunma.

## İLERLEME KAYDI (her iş bitince güncelle)
- Faz-2 planı oluşturuldu (2026-06-18).
- **1.5-a holdout şemaları** ✅ done (b791d62, push'lu). internal/holdout: Router (şema-dispatch) + PGStore (`pg://holdouts/<id>`, table `holdouts(id,path,content)`, migration 00005) + PrivateRepoStore (`private:<repo>#<path>`, gh-token, repo-dışı klon). Daemon: -dsn→pg auto, -holdout-private-cache+CONDUCTOR_GH_TOKEN→private; backward-compat (fs-only çalışır); unconfigured-scheme→açık hata. **Bağımsız doğrulandı:** kendi docker PG'imde 5 PGStore testi (-race), private local-repo 7 testi, Router dispatch, gate+e2e+race yeşil, frozen untouched, secret-leak yok (token redact).
- Sıradaki: 1.5-b (T3 human-hold canlı demo + approve akışı).
