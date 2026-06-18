# Karar Kaydı (Decision Log) — Conductor Platform

> **Bu dosya compact-hayatta-kalma çıpasıdır.** Konuşma sıkışsa/unutulsa bile kararlar burada.
> Her ADR kısa: Bağlam / Karar / Gerekçe / Sonuç / Durum. Tartışma geçmişi DESIGN-BRIEF.md'de.

## Tek cümlelik vizyon
Benimle yazışarak senaryo+holdout üretilen; bu task'ları **birden çok projede** otonom alıp
**standart bir kalite kapısından** geçiren (her zaman "kalite-kontrolden geçmiş" çıktı), jenerik
çok-projeli Kontaktör platformu. Çapa = **xirigo deseni** (DF değil).

## En kritik tek prensip (her şeyin kökü)
> **Kaliteyi yalnız DETERMİNİSTİK kanıtla garanti edebilirsin. Öznel LLM-skoru ASLA kalite kapısı olamaz.**
> DF bu yüzden battı (LLM-judge merge-gate → kararsız, 7-8h, terk). Xirigo bu yüzden tutuyor
> (test/maestro/visual exit-code + taze-göz review). LLM yalnız *danışman* rolünde kalır, *karar mercii* değil.

## GÜNCEL DURUM (2026-06-17 gecesi)
**Faz-1a ÇEKİRDEK TAMAM (9/9) + uçtan-uca doğrulandı** — `develop`'a merge+push, 8 paket, tüm gate'ler yeşil.
Gece-otonom çalışma + kalan iş listesi (P0/P1/P2): **[../NIGHT-AUTONOMOUS-PLAN.md](../NIGHT-AUTONOMOUS-PLAN.md)**.
Not: builder tick-bug (~1dk tick-ölümü) nedeniyle A-3..C-2 Agent'larla kodlandı (hepsi bağımsız gate'ten geçti).

## Kapanan kararlar
| ADR | Konu | Karar özeti | Durum |
|-----|------|-------------|-------|
| [0001](0001-mimari-yon.md) | Mimari yön | Xirigo desenini (Conductor-Performer-Ledger) jeneriklerştir; DF opsiyonel reçete | ✅ Kapandı |
| [0002](0002-engine-adapter.md) | Engine/Adapter | 5-fiil arayüz DONDU (Develop/Verify/Health/Events/Control); tek genel CommandEngine (reçete komutları subprocess; yeni tip=yeni reçete, kod yok) | ✅ DONDU |
| [0003](0003-verify-gate.md) | Verify-gate | Deterministik kanıt + taze-göz review; lokal-öncelikli, CI post-merge, risk-katmanlı | ✅ Kapandı |
| [0004](0004-branch-merge-modeli.md) | Branch/merge | Kısa-ömürlü per-task branch → gate → squash-merge develop → sil | ✅ Kapandı |
| [0005](0005-intake-katmani.md) | Intake | Konuşma→senaryo+holdout damıtma birinci-sınıf parça | ✅ Kapandı (format 0012, izolasyon 0018) |
| [0006](0006-sentinel.md) | Sentinel | 3-katman: deterministik taban + LLM-danışman (gri-bölge) + deterministik backstop | ✅ Kapandı |
| [0007](0007-platform-dili-go.md) | Platform dili | Go (tek binary, kolay kurulum); şema tek-kaynak → Go+TS codegen | ✅ Kapandı |
| [0008](0008-host-ve-concurrency.md) | Host & concurrency | Şema host+capability-aware; Faz-1 tek-host; lease=global cap + repo başına 1 | ✅ Faz-1 kapandı, çok-host Faz-2 |
| [0009](0009-scaffolder-onboarding.md) | Scaffolder/onboarding | Per-stack profil + asistanlı taslak+onay; reçete repoda `.conductor/`+registry pointer; readiness-gate (testsiz→önce "kalite altyapısı kur") | ✅ Kapandı |
| [0010](0010-registry-state-semasi.md) | Registry/state | Merkezi Postgres (registry+lease, host-üstü atomik) + ince Go client; canlı durum DB / audit repo'da; runtime kaynaktan-türet (drift yok) | ✅ Kapandı |
| [0011](0011-event-observability-semasi.md) | Event/observability | Postgres+LISTEN/NOTIFY gerçek-zaman; JSON Schema tek-kaynak→Go+TS codegen; phase/kind taksonomi + intervention-needed; control ters-kanal | ✅ Kapandı |
| [0012](0012-intake-format.md) | Intake format | Senaryo şeması (id/lane/tier/deps/acceptance/holdout); holdout=deterministik test (kod); karma görünürlük (temel TDD + gizli holdout); asistanlı damıtma+onay | ✅ Kapandı |
| [0013](0013-faz1a-faz1b-bolunmesi.md) | Faz-1a/1b bölünmesi | Walking-skeleton (1a: dosya-state, tek claude -p, en riskli varsayımı önce test) → ölçek (1b: Postgres, governor, intake, events) | ✅ Kapandı |
| [0014](0014-performer-kontrati-llm-dayaniklilik.md) | Performer kontratı + LLM dayanıklılık | develop_cmd stdin/stdout kontratı; pipeline performer-içinde; malformed/no-verdict/not-logged-in→blocked; sahte-yeşil asla | ✅ Kapandı |
| [0015](0015-macos-deployment-runtime.md) | macOS deployment/runtime | FDA-scoped binary, oauth-token (keychain ulaşılmaz), explicit PATH, caffeinate, launchd, preflight | ✅ Kapandı |
| [0016](0016-liveness-recovery-somut.md) | Liveness/recovery (somut) | mkdir-lock, stale-steal, progress-aware watchdog (mtime), orphan-sweep, BAĞIMSIZ auto-reconcile job; Katman-2 → 1b | ✅ Kapandı |
| [0017](0017-workspace-modeli.md) | Workspace modeli | Per-project clone + per-task worktree + cleanup; gh-token (deploy-key yasak); verify-worktree | ✅ Kapandı |
| [0018](0018-holdout-izolasyon.md) | Holdout izolasyon | Public test repo'da, gizli holdout repo-DIŞI store; verify-worktree'ye geçici enjekte; negatif-test ile kanıt | ✅ Kapandı |
| [0019](0019-builder-light-conductor.md) | İnşa aracı (builder) | xirigo'dan port light-conductor (`tools/builder/`); Go-gate; Faz-1a'yı otonom kodlar; ADR-0015/0016 canlı test; olgunlaşınca dogfood | ✅ Kapandı → **EMEKLİ** (ürün daemon'ı aştı; `tools/builder/DEPRECATED.md`, dogfood=`docs/DOGFOOD.md`) |
| [0020](0020-control-pause-temsili.md) | Control/pause temsili | Pause+control vokabüleri (engine.Command); marker-task ilk temsil **→ ADR-0021 ile Project-durumuna taşındı (supersede)**; abort follow-up | ✅ Kapandı (pause temsili 0021'e taşındı) |
| [0021](0021-statestore-additive-genisletme.md) | StateStore additive genişletme | "Frozen"=imza-bozan yasak ama **additive serbest** (kullanıcı onayı); UpdateProject eklendi; pause birinci-sınıf Project.Paused durumu (marker-task emekli); abort=Task.AbortRequested | ✅ Uygulandı (F-1 pause, F-2 abort) |
| [0022](0022-merge-push-deploy-modeli.md) | Merge-sonrası push/deploy | Opt-in `-push` (default kapalı=yerel); gh-token auth; push-fail→done-kalır + açık event (sahte-yeşil/sessiz-kayıp yok); remote-executor ayrı ADR (2B-0) | ✅ Uygulandı (1.5-c) |
| [0023](0023-gorsel-verify-recetesi.md) | Görsel-verify reçetesi | Görsel-diff = deterministik reçete GATE'i (render→diff→exit-code, threshold), yeni verify-type DEĞİL; referans=holdout-benzeri repo-dışı; öznel-skor yok | ✅ Uygulandı (2A-2 web, 2A-3 iOS) |
| [0024](0024-remote-executor-agent-per-host.md) | Remote-executor (çok-host) | **Agent-per-host** (kullanıcı kararı): daemon her host'ta; merkezi-PG'den capability-uyan lease; iş yerelde; ssh/uzak-komut YOK; host-üstü lease zaten N-4'te | ✅ Uygulandı (Dalga B) |
| [0025](0025-api-gateway.md) | API gateway (Faz-3) | **Ayrı `cmd/conductor-api` servisi** (kullanıcı kararı): read+control; daemon DEĞİŞMEZ; veri paylaşılan store/bus; kontrol mevcut conductor seam reuse (store-yansıması, daemon'a doğrudan-komut YOK); REST+WS, bearer-auth; frontend-agnostik (fork köprüsü) | ✅ Uygulandı (DALGA 3A: 3A-0..3A-4 tamam) |
| [0026](0026-frontend-stack.md) | Frontend stack (Faz-3) | **React+Vite+TS**, `web/` izole araç zinciri; event tipleri N-9 codegen (drift yok); auth=bearer-token (sessionStorage); deterministik frontend gate = tsc+eslint+vitest+Playwright (Go gate felsefesi) | ✅ Karar (3B-0); iskele başlıyor |

## Durum
**Tüm Faz-1 tasarım kararları KAPANDI (ADR 0001–0018)** + 2 bağımsız adversarial review'ın 25 bulgusu kapatıldı
([REVIEW-FINDINGS.md](../REVIEW-FINDINGS.md)). Repo: github.com/everva/conductor-platform (private).
- **Sıradaki (KOD):** [PHASE-1-PLAN.md](../PHASE-1-PLAN.md) → **Faz-1a walking skeleton**, **builder light-conductor
  ile otonom** (ADR-0019). Sıra: (1) builder'ı xirigo'dan port (`tools/builder/`) → (2) ÖN-ADIM: Go kontratlarını
  dondur (tek performer+onay) → (3) Dalga A: statestore+registry+engine → B: provisioner+verify+sentinel+macOS → C: entegrasyon+uçtan-uca.
- **UI hedefi (Faz-3)** — VS Code vs web (ertelendi; event+control seam hazır).

**Takma-ad eşlemesi** (eski ADR metinlerinde): ADR-c = ADR-0010, ADR-d = ADR-0012, ADR-e = ADR-0011.

## Faz planı
- **Faz-1a (walking skeleton):** dosya-state + tek `claude -p` + deterministik verify + tek task merge + macOS launchd.
  En riskli varsayımı (engine mekaniği + LLM dayanıklılık + holdout izolasyon) önce test eder. (ADR-0013)
- **Faz-1b (ölçek):** Postgres + resource-governor (paralel) + intake + scaffolder + events + governance-policy + heartbeat.
- **Faz-2:** Engine 2. reçete + çok-host capability routing (iOS-lane→Mac) + sentinel Katman-2.
- **Faz-3:** ✅ TAMAM — API gateway (`cmd/conductor-api`, ADR-0025) + web cockpit (`web/`, ADR-0026: dashboard/
  event-akış/müdahale/intake-chat) + uçtan-uca capstone (UI-origin task→Kontaktör develop/verify/merge→cockpit gözler).
  Detay: [../PHASE-3-PLAN.md](../PHASE-3-PLAN.md). Gateway+web fork'a köprü (Faz-4 reuse eder).
- **Faz-4 (sıradaki):** editör fork (devin/cursor-tarzı ajan-yönetimi) — gateway'i + web bileşenlerini reuse eder.

## Taşınan disiplin
Orchestrator-only (kod sub-agent'a delege; karar-notu istisna) · Rule#9 (self-report'a güvenme,
bağımsız doğrula) · subscription `claude -p` (API-key yok) · secret-leak yok · load-aware (host'u boğma)
· deploy/push onaysız yapma.
