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

## Kapanan kararlar
| ADR | Konu | Karar özeti | Durum |
|-----|------|-------------|-------|
| [0001](0001-mimari-yon.md) | Mimari yön | Xirigo desenini (Conductor-Performer-Ledger) jeneriklerştir; DF opsiyonel reçete | ✅ Kapandı |
| [0002](0002-engine-adapter.md) | Engine/Adapter | 5-fiil arayüz DONDU (Develop/Verify/Health/Events/Control); tek genel CommandEngine (reçete komutları subprocess; yeni tip=yeni reçete, kod yok) | ✅ DONDU |
| [0003](0003-verify-gate.md) | Verify-gate | Deterministik kanıt + taze-göz review; lokal-öncelikli, CI post-merge, risk-katmanlı | ✅ Kapandı |
| [0004](0004-branch-merge-modeli.md) | Branch/merge | Kısa-ömürlü per-task branch → gate → squash-merge develop → sil | ✅ Kapandı |
| [0005](0005-intake-katmani.md) | Intake | Konuşma→senaryo+holdout damıtma birinci-sınıf parça | 🟡 Yön kapandı, format detayı açık |
| [0006](0006-sentinel.md) | Sentinel | 3-katman: deterministik taban + LLM-danışman (gri-bölge) + deterministik backstop | ✅ Kapandı |
| [0007](0007-platform-dili-go.md) | Platform dili | Go (tek binary, kolay kurulum); şema tek-kaynak → Go+TS codegen | ✅ Kapandı |
| [0008](0008-host-ve-concurrency.md) | Host & concurrency | Şema host+capability-aware; Faz-1 tek-host; lease=global cap + repo başına 1 | ✅ Faz-1 kapandı, çok-host Faz-2 |
| [0009](0009-scaffolder-onboarding.md) | Scaffolder/onboarding | Per-stack profil + asistanlı taslak+onay; reçete repoda `.conductor/`+registry pointer; readiness-gate (testsiz→önce "kalite altyapısı kur") | ✅ Kapandı |
| [0010](0010-registry-state-semasi.md) | Registry/state | Merkezi Postgres (registry+lease, host-üstü atomik) + ince Go client; canlı durum DB / audit repo'da; runtime kaynaktan-türet (drift yok) | ✅ Kapandı |
| [0011](0011-event-observability-semasi.md) | Event/observability | Postgres+LISTEN/NOTIFY gerçek-zaman; JSON Schema tek-kaynak→Go+TS codegen; phase/kind taksonomi + intervention-needed; control ters-kanal | ✅ Kapandı |
| [0012](0012-intake-format.md) | Intake format | Senaryo şeması (id/lane/tier/deps/acceptance/holdout); holdout=deterministik test (kod); karma görünürlük (temel TDD + gizli holdout); asistanlı damıtma+onay | ✅ Kapandı |

## Açık / sıradaki kararlar
**Tüm Faz-1 tasarım kararları KAPANDI (ADR 0001–0012).** Kalan:
- **Faz-1 iskelet (KOD)** — Provisioner + Registry (Postgres) + Event-şeması (JSON Schema) + Verify-gate + Resource-governor. Engine arayüzü donduğu için (ADR-0002) başlanabilir. optiway/test-proje ile uçtan uca doğrula (canlıya DOKUNMA).
- **Platform repo remote** — `everva/conductor-platform` private kurulumu (§6.2; henüz yok).
- **UI hedefi (Faz-3)** — VS Code vs web (ertelendi; şimdilik dilden-bağımsız event+control seam).

## Faz planı
- **Faz-1 (ana yapı):** Provisioner + Registry + Event-şeması + Verify-gate + Resource-governor. Tek host (davinci), lokal-dosya state. optiway/test-proje ile uçtan uca doğrula (canlıya DOKUNMA).
- **Faz-2:** Engine Adapter 2. reçete + çok-host capability routing (iOS-lane→Mac).
- **Faz-3:** Observability UI (VS Code/web), Kontaktör-buildable.

## Taşınan disiplin
Orchestrator-only (kod sub-agent'a delege; karar-notu istisna) · Rule#9 (self-report'a güvenme,
bağımsız doğrula) · subscription `claude -p` (API-key yok) · secret-leak yok · load-aware (host'u boğma)
· deploy/push onaysız yapma.
