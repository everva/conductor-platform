# ADR-0001 — Mimari yön: xirigo desenini jeneriklerştir

## Bağlam
Brief, Dark Factory'yi (DF) "kanıtlı motor, düşük risk" diye merkeze koymuştu. Ama kullanıcının
**yaşanmış gerçeği** tersi: DF'de holdout skorları tutmadı, basit bir iş 7-8 saat döndü, DF terk edildi.
Kök-neden (bağımsız incelendi): DF'nin kalite kapısı **öznel LLM-judge** (satisfaction ≥80 + holdout ≥90,
3 koşu/2-quorum) — LLM varyansı ±5-10 puan → aynı kod bazen geçer bazen kalır → kör retry loop → saatler.
Buna karşılık **xirigo DF'siz başarılı**: Conductor-Performer-Ledger üçlüsü + **deterministik** kalite kapısı
(unit/lint/build + maestro headless exit-code + görsel diff ≤%5 + taze-göz façade-reddi).

## Karar
Platformun çapası **xirigo deseni**. DF **merkez değildir**; en fazla, isteyen bir projenin seçebileceği
**opsiyonel develop/verify reçetesi**. Platform = "jeneriklerştirilmiş xirigo": tek-projeden çok-projeye,
hardcoded yollardan registry'ye.

## Gerekçe
Rule#9: brief'in "kanıtlı" dediğine değil, kullanıcının gerçek sonucuna güven. Deterministik kapı > öznel
kanaat. Sıfırdan icat yok — xirigo zaten Conductor+Ledger+recovery+heartbeat omurgasına sahip.

## Sonuç
- Tüm sonraki kararlar xirigo'dan türetilir.
- DF plugin patch'leri (ci_gated_merge, holdout-retry) artık **kritik değil** — opsiyonel reçete kalırsa kullanılır.
- Brief §6.1 ("DF+adapter") **çürüdü**; yerine bu ADR.

## Durum
✅ Kapandı.
