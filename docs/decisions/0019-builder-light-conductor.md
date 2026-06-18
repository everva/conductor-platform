# ADR-0019 — İnşa aracı: xirigo'dan port edilmiş builder light-conductor

## Bağlam
conductor-platform'u (Go ürünü) kodlamak için otonom bir inşa döngüsü isteniyor. PHASE-1-PLAN D7 "dogfooding
paradoksu" notu: ürün henüz YOK, kendini kendiyle inşa edemez. Çözüm: ürünü değil, **ayrı + kanıtlı xirigo
desenini** inşa aracı olarak kullan.

## Karar
**Builder = xirigo deseninden port edilmiş light conductor** (bash + python; conductor-platform DEĞİL).
- **Yer:** script'ler repo'da `tools/builder/` (versiyonlu: conductor-tick + auto-reconcile + plist + senaryo/holdout);
  çalışma durumu (lock, ledger, log, oauth-token) `~/.conductor-platform-builder/` (xirigo blueprint↔`.xirigo` ayrımı).
- **Kalite kapısı (Go, deterministik — ADR-0003):** her task `go build ./... && go test ./... && go vet ./... &&
  golangci-lint run` geçmeli (exit-code). Senaryo-başına holdout = o paketin testleri.
- **Görev kaynağı:** PHASE-1-PLAN Faz-1a Dalga A/B/C adımları → elle senaryo + holdout (ADR-0013).
- **ÖN-ADIM (ADR-0014/§7):** paylaşılan Go kontratları (`EngineAdapter`/`Verdict`/`ReviewResult`/`StateStore`
  interface+struct) builder'ın İLK task'ı olarak **tek-performer + insan onayı** ile dondurulur (paralel sapma önlenir).
- **Operasyonel temel CANLI test:** ADR-0015 (FDA/oauth-token/PATH/caffeinate/launchd) + ADR-0016 (mkdir-lock/
  stale-steal/progress-watchdog/orphan-sweep/bağımsız reconcile) builder'da birebir uygulanır → bu ADR'ler gerçek koşuda doğrulanır.
- **Emeklilik:** conductor-platform olgunlaşınca builder, ürünün kendisiyle değiştirilir (gerçek dogfood).

## Gerekçe
Kanıtlı xirigo deseni → düşük risk, hızlı başlangıç (port). Paradoks YOK (builder ayrı araç). Go-gate saf
deterministik (UI/maestro derdi yok). Builder aynı zamanda Faz-1a'nın canlı prototipi → ADR-0015/0016 bedavaya test edilir.

## Sonuç
- PHASE-1-PLAN D7 güncellenir (inşa aracı = builder, normal-CI değil).
- Builder, ürünün scope'una dahil değil; `tools/builder/` ayrı tutulur.

## Durum
✅ Kapandı. **EMEKLİYE AYRILDI (2026-06-18, kullanıcı onayı):** bootstrap görevi tamamlandı — ürün daemon'ı
(`cmd/conductor`) artık otonom tick'i (claude -p develop → bağımsız verify+holdout → squash-merge) gerçek
binary'lerle kanıtlanmış şekilde yapıyor (İş A/A.1). Builder'ın ~1dk tick-ölüm bug'ı kök-nedeni statik analizle
çıkmadı (watchdog 20dk / launchd 300s / reconcile 180s — hiçbiri ~1dk'da ateşlemiyor) ve canlı launchd
enstrümantasyonu riskli; neredeyse-obsolete bir araca yatırım yerine line-19'daki planlı emeklilik tetiklendi.
`tools/builder/` referans/tarihçe olarak korunuyor (bkz. `tools/builder/DEPRECATED.md`); dogfood artık ürün
daemon'ıyla yapılıyor (bkz. `docs/DOGFOOD.md`).
