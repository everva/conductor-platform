# Faz-1 Planı — Adversarial Review Bulguları (kapatma takip listesi)

> 2 bağımsız reviewer (tutarlılık-lensi + implementasyon-lensi) + orchestrator bulguları, dedupe edilmiş.
> Amaç: dokümanı eksiksiz + problemsiz hale getirmek. Her bulgu kapatıldıkça Durum güncellenir.

## ✅ KAPANIŞ DURUMU — tüm bulgular kapatıldı
META→ADR-0013 (Faz-1a/1b) · K1→ADR-0018 · K2+K3→ADR-0014 · K4→ADR-0015 · K5→ADR-0016 ·
Y1→ADR-0008/0010 (lanes) · Y2→ADR-0002/0011 · Y3→ADR-0010 (intent/gözlemlenen) · Y4→ADR-0010 (reaper) ·
Y5→ADR-0002 (jenerik checks) · Y6→ADR-0003 (docker-ban) · O1→ADR-0009 (meta-holdout) · O2→ADR-0004/0010 (retry_count) ·
O3→PHASE-1-PLAN §0 (governance≠governor isim ayrımı) + T3/T4 human-merge Faz-1b'ye (§3) · O4→ADR-0004 (squash-trailer) · O5→ADR-0010 (commands) ·
O6→ADR-0017 (worktree) · O7→PHASE-1-PLAN (1b heartbeat) · D1→README (takma-ad eşleme) · D2→ADR-0005 (✅) ·
D3→ADR-0007 · D4→PHASE-1-PLAN §0 · D5→ADR-0011 (phase↔fiil) · D6→PHASE-1-PLAN §7 dalga · D7→PHASE-1-PLAN §9 (goose+dogfooding).

## META-BULGU (en önemli) — Faz-1 scope TERS SIRALI
İki reviewer da bağımsız işaret etti: altyapı (Postgres, events, intake, scaffolder) ÖNCE, **en riskli
varsayım (engine'in gerçek LLM-pipeline mekaniği + LLM çıktı güvenilmezliği) EN SONA** bırakılmış. İlk
uçtan-uca yeşil haftalarca gecikir; o gelene dek sistemin kalbi test edilmemiş kalır.
→ **Öneri: Faz-1'i ikiye böl. 1a = risk-öldüren walking skeleton** (elle senaryo+reçete, dosya/in-memory
state, tek `claude -p` develop, deterministik verify, tek task merge, macOS launchd temeli). **1b = ölçek**
(Postgres, governor-paralel, intake, scaffolder, events, codegen). [✅ KARAR ALINDI]

## KRİTİK (kod öncesi şart)
| # | Bulgu | Kaynak | Kapatma | Durum |
|---|-------|--------|---------|-------|
| K1 | **Gizli holdout çelişki + mekanizma yok**: ADR-0012 "repo `.conductor/scenarios/`" ↔ "performer görmez" çelişiyor; performer repoyu klonlayınca görür. Saklama/enjeksiyon tanımsız. Ayrıca xirigo'da YOK (kanıtsız). | R1#1,#3 · R2#6 · orch | Holdout repo-DIŞI (registry/ayrı); verify'de geçici izole workspace'e enjekte; `.conductor/`'da yalnız public test. ADR-0012/0010 + yeni mekanizma. [✅ KARAR: tut + repo-dışı izolasyon (ADR-0018)] | ✅ Kapatıldı |
| K2 | **EngineAdapter.Develop mekaniği havada**: `develop_cmd` tek `claude -p` mi, çok-adım pipeline mi? Pipeline (architect→test→dev→review) kim/nerede? scaffolder mı yazıyor? | R2#1 · orch A1 | "Performer kontratı" ADR'si: develop_cmd stdin(senaryo+pointer)/stdout(Verdict JSON) kontratı; pipeline performer-içinde (`claude -p`+pipeline-prompt/skill, xirigo `pipeline-run` referans); self-heal kim sayar. ADR-0002 genişlet. | ✅ Kapatıldı |
| K3 | **LLM çıktı güvenilmezliği** (DF'nin batma sınıfının operasyonel kardeşi): boş/bozuk JSON, timeout, rate-limit, "Not logged in" hiç ele alınmamış. | R2#2 · orch A2 | Engine zorunlu: JSON-extraction (prose içinden son blok), parse-fail→`blocked:malformed`, timeout→`blocked:no-verdict`, not-logged-in→tüm tick DUR+bildir. Sahte-yeşil ASLA. ADR-0002/0003. | ✅ Kapatıldı |
| K4 | **macOS launchd gerçeği atlanmış**: FDA-scope (Documents'a erişim), oauth-token (keychain launchd'den ulaşılmaz → `claude setup-token` long-lived dosya), PATH export (launchd boş PATH), caffeinate, RunAtLoad/KeepAlive. "Tek binary kopyala-çalıştır" macOS'te YANLIŞ. | R2#3 | Yeni "deployment/runtime" ADR + plan bölümü; xirigo plist + conductor-tick.sh birebir port. | ✅ Kapatıldı |
| K5 | **Watchdog/lock/orphan/bağımsız-reconcile somut değil**: atomik mkdir-lock (macOS flock yok), stale-lock steal (ölü PID), progress-aware watchdog (mtime — aktif build'i öldürme), orphan-sweep (claude init'e reparent → çift-yazan orchestrator), BAĞIMSIZ auto-reconcile (conductor'a gömülü değil ayrı job). | R2#4 · orch | ADR-0006 somutlaştır + plan: reconcile AYRI launchd job (conductor loop'una gömme — conductor ölünce reconcile ölmesin). xirigo auto-reconcile.py kontrol listesi. | ✅ Kapatıldı |

## YÜKSEK
| # | Bulgu | Kaynak | Kapatma | Durum |
|---|-------|--------|---------|-------|
| Y1 | **capability `requires` kaynağı kopuk**: ADR-0008 "lane seviyesinde" ↔ DDL `tasks.requires`. Lane→capability eşlemesi nerede tanımlı? | R1#2 | `lanes` tablosu veya recipe'de lane→capability; `tasks.requires` türetilmiş. ADR-0008/0010/0012 hizala. | ✅ Kapatıldı |
| Y2 | **Event/Control sahipliği çelişik**: ADR-0002 `Events()`/`Control()` engine fiili ↔ ADR-0011 platform yazar/tüketir (sentinel+scaffolder üretir). | R1#7 | Engine yalnız kendi subprocess çıktısını normalize edip platforma verir; Postgres yazma+NOTIFY+komut-tablosu platform işi. ADR-0002/0011 netleştir. | ✅ Kapatıldı |
| Y3 | **drift prensibi ↔ Postgres durum**: ADR-0010 "runtime cache'lenmez, git'ten türet" ↔ `tasks.status` Postgres'te (runtime cache). | R1#6 | intent (deps/lease/scenario = otoriter) vs gözlemlenen (merge/branch = git'ten türet) ayrımını ADR-0010'da net çiz. | ✅ Kapatıldı |
| Y4 | **lease reaper/stale/global-cap atomik yok**: crash'te repo sonsuz kilitli (reaper yok); global-cap≤4 atomik insert tanımsız. | R2#5 | `leases.acquired_at` + TTL/heartbeat reaper (xirigo "DROP stale lease"); global-cap atomik (`INSERT...WHERE count<N`/advisory-lock). ADR-0010. | ✅ Kapatıldı |
| Y5 | **Verdict sabit-anahtar ↔ jeneriklik**: `{maestro_exit, visual_diff_pct}` mobil-özel; backend reçetesi için anlamsız; "kod yazmadan yeni kanıt türü" imkansız. | R1#11 | `Verdict.tests` → jenerik `checks:[{name,result,evidence}]`. ADR-0002/0011. | ✅ Kapatıldı |
| Y6 | **docker-ban / migration lokal-imkansız**: ADR-0003 "lokal container" ↔ xirigo RUNTIME.md docker BANNED (thermal); migration-safety lokal deterministik doğrulanamıyor. | R2#8 | ADR-0003'e: container-gerektiren gate'ler lokal-imkansız → atla+işaretle veya remote-runner. macOS gerçeği. | ✅ Kapatıldı |

## ORTA
| # | Bulgu | Kaynak | Kapatma | Durum |
|---|-------|--------|---------|-------|
| O1 | **bootstrap-task tavuk-yumurta**: kalite altyapısı kuran task'ın kendi holdout'u ne? | R1#10 | meta-holdout ("test komutu var+yeşil") veya verify-muaf özel akış. ADR-0009. | ✅ Kapatıldı |
| O2 | **retry-cap ↔ time-backstop koordinasyonsuz + DDL'de retry-count yok** | R1#9 · orch | `tasks.retry_count`; ADR-0004 retry-cap ↔ ADR-0006 backstop önceliği tek yerde. | ✅ Kapatıldı |
| O3 | **yüksek-risk/human-merge akışı PHASE-1'de yok**; "governor"(concurrency) vs "governance"(risk-policy) isim çakışması | R1#8 | Plana T3/T4 human-merge doğrulama adımı veya açık ertele-notu; isim ayır (governance-policy ≠ resource-governor). | ✅ Kapatıldı |
| O4 | **reconcile ↔ squash-merge tespiti**: squash sonrası task'ın merge'i nasıl deterministik bulunur | orch B2 | Squash commit-trailer `[task:<id>]` standardı + reconcile grep. ADR-0004/0010. | ✅ Kapatıldı |
| O5 | **conductorctl ↔ daemon iletişimi tanımsız** | orch C1 | Postgres komut tablosu (ADR-0011 control-kanalı) üzerinden. | ✅ Kapatıldı |
| O6 | **workspace clone vs worktree kararsız**; disk/cleanup/gh-token kaynağı | R1 · R2#9 · orch C3 | Yeni workspace ADR: worktree (xirigo deseni) + retention/cleanup + gh-token kaynağı. | ✅ Kapatıldı |
| O7 | **bağımsız heartbeat/liveness yok**: event-stream conductor'ın kendi verisi; conductor ölünce durur | R2#11 | Faz-1b: bağımsız heartbeat dosyası + harici stall-alert (xirigo deseni). | ✅ Kapatıldı |

## KÜÇÜK (doküman hijyeni)
| # | Bulgu | Kaynak | Durum |
|---|-------|--------|-------|
| D1 | ADR-c/d/e takma-adları → gerçek numara (0010/0012/0011) | R1#5 | 🔴 |
| D2 | README "hepsi kapandı" ↔ ADR-0005 hâlâ 🟡 | R1#4 | 🔴 |
| D3 | ADR-0007 "protobuf veya JSON" ↔ ADR-0011 JSON donduruldu | R1#14 | 🔴 |
| D4 | `recipes/` (platform profil-şablon) vs `.conductor/` (üretilmiş reçete) terminoloji ayrımı | R1#13 | 🔴 |
| D5 | phase↔fiil eşleme tablosu yok (plan/develop/test kim üretir) | R1#12 | 🔴 |
| D6 | §5 lineer sıra ↔ §6 dalga (events sırası) çelişkisi | R1#15 | 🔴 |
| D7 | migration aracı (goose/atlas) + dogfooding notu (platform kendini conductor'la geliştirmez) | orch C4 | 🔴 |

## Reviewer notları
- Tutarlılık notu: **6.5/10** (üst-mimari güçlü; kavramdan-mekanizmaya geçişte boşluk).
- Fizibilite notu: **5/10** (yön 8/10, Faz-1 uygulama-hazırlığı 4/10; kalp + operasyonel omurga tanımsız).
- Ortak teşhis: xirigo'yu "çapa" ilan ettik ama xirigo'nun SOMUT operasyonel çözümlerini (conductor-tick.sh,
  auto-reconcile.py, plist) içselleştirmedik; sadece kavramları (ADR-0006 "3 katman") soyut aldık.
