# ADR-0002 — Engine / Adapter: sabit iskelet + develop/verify reçetesi (DONDU)

## Bağlam
xirigo (DF'siz) ile DF yan yana konunca görüldü: "motor" tek şey değil. İki ayrı soyutlama var —
**geliştirme döngüsü** (kodu üreten) ve **kalite kapısı** (çıktıyı doğrulayan). Bunlar projeye göre değişir;
platform iskeleti (conductor, ledger, lease, recovery, governance, event-stream) SABİT kalır. Brief §5.1/§9.3:
arayüz provisioner'dan ÖNCE dondurulmalı.

## Karar — DONDURULDU
**Platform iskeletinin gördüğü arayüz (Go), 5 fiil** — DF özeli (ralph.sh, `.dark-factory`) sızdırılmadan:

```go
type EngineAdapter interface {
    Develop(ctx, task, workspace) (Verdict, error)       // kod üret + lokal test + commit (ADR-0004)
    Verify(ctx, verdict, workspace) (ReviewResult, error) // taze-göz review + deterministik kanıt (ADR-0003)
    Health(ctx, session) (HealthState, error)             // sentinel Katman-1 sinyali (ADR-0006)
    Events(ctx) (<-chan Event, error)                     // normalize event akışı (ADR-e)
    Control(ctx, cmd) error                               // pause | resume | abort
}
```

Şemalar (kanıt-temelli, **skor yok** — ADR-0003):
```
Verdict      { result: pass|fail|blocked, branch, commit_sha,
               checks:[{name, result, evidence}], files[], summary }   // jenerik (Y5): unit/lint/build/maestro/visual… reçeteye göre
ReviewResult { decision: pass|changes-requested|blocked, evidence[], notes }
HealthState  { phase, last_activity_ts, signal: progressing|idle|unknown }
Event        { ts, project, task, phase, kind, payload }   // tek-kaynak şema → Go+TS codegen (ADR-0007)
```

**İmplementasyon stratejisi = TEK GENEL CommandEngine.** Tek Go adaptörü, repo `.conductor/` reçetesindeki
`develop`/`verify` (vb.) **komutlarını subprocess** olarak çağırır; çıktıdan Verdict/ReviewResult (JSON) parse
eder. **Yeni proje-tipi = yeni reçete, KOD YAZILMAZ.** DF bile bir reçetedir (`develop-cmd = ralph.sh`).

**Görev dağılımı:** Task keşfi/seçimi (deps + lease + phase + readiness) = **platform** işi, engine değil.
Engine'e yalnız "şu task'ı develop/verify et" denir; *ne çalıştıracağı* reçeteden gelir (ADR-0009).

## Gerekçe
develop/verify fiilleri etrafında dondurmak (motor-implementasyonu değil) xirigo'yu, DF'yi ve gelecekteki
tipleri taşır. Tek CommandEngine = kod yazmadan yeni proje-tipi = en jenerik, en az bakım.

## Sonuç
- Provisioner ve registry bu arayüze bağlanabilir (arayüz donduğu için).
- Reçete kontratı (`develop`/`verify` komut girdileri + JSON çıktı şeması) ADR-0009 scaffolder'ın ürettiği şey.
- Event şeması ADR-e'de dondurulacak (Events fiili onu yayar).

## Güncelleme (review: Y5, Y2, K2)
- **Verdict.tests jenerikleştirildi:** sabit anahtar (`maestro_exit`/`visual_diff_pct` mobil-özel) yerine
  `checks: [{name, result, evidence}]` → proje-tipinden bağımsız; "kod yazmadan yeni kanıt türü" mümkün (jeneriklik).
- **Event/Control sahipliği:** Engine YALNIZ kendi subprocess çıktısını normalize edip platforma verir;
  Postgres'e yazma + NOTIFY + komut-tablosu tüketimi PLATFORM işi (ADR-0011). `Events()`/`Control()` bu sınırı temsil eder.
- **Develop/Verify somut mekaniği** ADR-0014'te donduruldu (performer kontratı + LLM çıktı dayanıklılığı).

## Durum
✅ Kapandı / DONDU (ADR-0014 somutlar).
