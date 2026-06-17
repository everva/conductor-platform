# ADR-0006 — Sentinel: 3-katmanlı (deterministik + LLM-danışman + deterministik backstop)

## Bağlam
Kullanıcı sentinel'i "tam tutmak" istiyor (LLM-judge dahil, akıllı olsun) ama DF gibi "işin içinden
çıkılmaz" / takılan olmasın. DF battı çünkü LLM'i **kalite kapısına (karar mercii)** koydu. xirigo recovery
**tamamen deterministik** (auto-reconcile, watchdog) — bu yüzden takılmıyor.

## Karar
LLM'i **canlılık teşhisine (danışman)** koy, karar merciine DEĞİL. 3 katman:

| Katman | Ne yapar | LLM? |
|--------|----------|------|
| 1. Deterministik taban (her kontrol) | process canlı mı, lock/lease geçerli mi, çıktı akıyor mu → kesin sinyal kesin aksiyon (restart, orphan-kill) | ❌ |
| 2. Gri-bölge teşhisi (NADİR) | sadece taban "emin değilim" derse (çıktı yok ama process yaşıyor): LLM son log'a bakar → `{ilerliyor \| sıkışmış \| insan-gerek}` + 1 cümle | ✅ danışman |
| 3. Deterministik backstop (her zaman) | mutlak tavan (task-tipi bazlı, örn. 6h) → kill+blocked+bildir. LLM "bekle" dese bile bağlar | ❌ |

## Gerekçe
- "Akıllı" = Katman 2 (LLM derlemeyi-mi-hang-mi ayırt eder; saf watchdog edemez).
- "Takılmaz" = Katman 3 (LLM sistemi asla sonsuza bekletemez — DF'nin eksiği buydu).
- "Karmaşık değil" = LLM nadir çağrılır (load yok) + dar çıktı (skor yok → öznellik yok).
- DF farkı tek cümle: DF LLM'i merge-gate'e koydu (battı); biz canlılık-danışmanına koyuyoruz, son söz deterministik.

## Sonuç
- Sentinel ayrı karmaşık servis değil; liveness+recovery platform iskeletine gömülü, event yayar (observability ilk veri kaynağı).
- LLM-judge çağrısı `claude -p` subprocess (Go'dan).

## Güncelleme (review: K5)
Katman-1 (deterministik) ve Katman-3 (backstop) SOMUT mekanizmaları **ADR-0016**'da operasyonelleştirildi
(atomik mkdir-lock, stale-lock steal, progress-aware watchdog [mtime], orphan-sweep, **BAĞIMSIZ** auto-reconcile
job). Katman-2 (LLM-danışman gri-bölge) **Faz-1b**'ye ertelendi (ADR-0013) — walking skeleton'da deterministik
taban+backstop yeter.

## Durum
✅ Kapandı (ADR-0016 somutlar).
