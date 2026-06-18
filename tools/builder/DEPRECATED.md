# ⚠️ DEPRECATED — bu bootstrap aracı emekliye ayrıldı (2026-06-18)

`tools/builder/` (xirigo'dan port bash+python light-conductor, ADR-0019) conductor-platform'u **ürün daha
yokken** kodlamak için bir **bootstrap** aracıydı. O görev tamamlandı: Faz-1a + Faz-1b çekirdeği + üretim
sertleştirme bitti ve **ürün daemon'ı (`cmd/conductor`) artık aynı işi yapıyor** — otonom tick (gerçek
`claude -p` develop → bağımsız deterministik verify + repo-dışı gizli holdout → squash-merge), pause/resume,
abort — hepsi gerçek binary'lerle canlı doğrulandı.

## Neden tamir edilmedi (tick ~1dk-ölüm bug'ı)
Kök-neden statik analizle çıkmadı: progress-watchdog `FREEZE_LIMIT=1200`s (20dk), ana launchd
`StartInterval=300`s + KeepAlive yalnız anormal-çıkışta, reconcile `StartInterval=180`s — **hiçbiri ~1dk'da
ateşlemiyor.** Gerçek teşhis canlı launchd + enstrümantasyon gerektiriyordu (önceki oturumları
dengesizleştiren riskli, ortam-hassas iş). Neredeyse-obsolete bir araca yatırım yerine ADR-0019'daki planlı
emeklilik tetiklendi.

## Bunun yerine ne kullanılmalı
**Dogfood = ürün daemon'ı.** conductor-platform'u (veya başka bir repo'yu) `conductorctl` ile onboard et,
senaryo+holdout gir, `conductor` daemon'ını çalıştır. Rehber: **`docs/DOGFOOD.md`** ve **`docs/DEPLOY.md`**.

## Durdurma / temizlik (zaten durdurulmuş)
```sh
launchctl bootout gui/$(id -u)/com.conductor-platform-builder 2>/dev/null || true
launchctl bootout gui/$(id -u)/com.conductor-platform-builder.reconcile 2>/dev/null || true
rm -f ~/.conductor-platform-builder/PAUSE   # (gereksiz; job'lar zaten unload)
```

Script'ler **referans/tarihçe** olarak korunuyor (xirigo-port deseni, ADR-0015/0016'nın canlı prototipiydi);
silinmedi ama **çalıştırılmamalı**.
