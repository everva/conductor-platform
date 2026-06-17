# ADR-0011 — Event / observability şeması: Postgres+NOTIFY, JSON Schema tek-kaynak

## Bağlam
§5.5: Observability UI Faz-3'te ama şema ŞİMDİ dondurulmalı ki motor baştan tutarlı yaysın (sonradan eklemek →
tutarsız/eksik event). Devin-vari faz-faz canlı izleme + müdahale hedefi. EngineAdapter.Events() bunu yayar (ADR-0002).

## Karar
**1. Taksonomi (dondu):**
- `phase ∈ {plan, develop, test, review, verify, merge}`
- `kind ∈ {started, progress, log, diff, decision, health, pr, merge, intervention-needed}`
- `intervention-needed` = insan-kapısı sinyali (T3/T4, blocked, readiness — ADR-0003/0009). UI'da "müdahale gerek" yanar.
- Event zarfı: `{ ts, project, task, phase, kind, payload }` (ADR-0002 ile aynı).

**2. Taşıma = Postgres tablo + LISTEN/NOTIFY.** Event'ler merkezi Postgres'e (ADR-0010) yazılır; UI `LISTEN`
ile **gerçek-zamanlı push** alır. Sorgulanabilir geçmiş + tek-kaynak tutarlılığı. Çok-host'ta tek toplama noktası.

**3. Şema biçimi = JSON Schema (tek-kaynak).** Event + state tipleri JSON Schema'da tanımlanır → **Go + TS'e
codegen** (ADR-0007). İnsan-okunur, JSON payload ile doğal uyum, tip drift'i önlenir.

**4. Control kanalı (ters yön):** pause/resume/abort/approve-merge/intervene → Postgres komut tablosu + NOTIFY;
conductor tüketir (ADR-0002 Control fiili). Faz-3 UI hem event okur hem komut yazar.

## Gerekçe
Postgres+NOTIFY = ADR-0010 ile tek altyapı + gerçek-zaman + çok-host toplama. JSON Schema = en az sürtünme,
event'ler zaten JSON. Şemayı şimdi dondurmak motorun tutarlı yaymasını garanti eder.

## Sonuç
- İlk üretici = sentinel deterministik taban (ADR-0006) + scaffold readiness; sonra motor fazlarına genişler.
- Audit/journal (ADR-0010) repo'da; event-stream (canlı) Postgres'te — ikisi farklı amaç (kalıcı denetim vs canlı izleme).
- Faz-3 UI (VS Code/web) bu şema + control kanalı üstüne oturur; teknoloji seçimi ertelenmiş seam.

## Güncelleme (review: Y2, D5)
- **Sahiplik:** event'leri Postgres'e yazan + NOTIFY eden + komut-tablosunu tüketen = PLATFORM (sentinel,
  scaffolder, conductor). Engine (ADR-0002 `Events()`) YALNIZ kendi subprocess çıktısını normalize edip
  platforma verir — Postgres'i engine bilmez.
- **phase↔fiil eşlemesi:** intake→`plan`; Develop→`develop`/`test`/`diff`/`log`; Verify→`review`/`verify`;
  conductor→`pr`/`merge`; sentinel→`health`/`intervention-needed`.
- **Faz:** event-stream + codegen = Faz-1b; Faz-1a'da event'ler dosyaya/stdout'a log'lanır (ADR-0013).

## Durum
✅ Kapandı. JSON Schema dosyaları + Postgres event tablosu DDL implementasyonda yazılır.
