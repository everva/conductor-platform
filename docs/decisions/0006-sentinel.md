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

## Güncelleme (Faz-2 2C-1): Katman-2 ARTIK IMPLEMENTE
Gri-bölge LLM-danışman (Katman-2) `internal/sentinel` paketinde gerçeklendi ve conductor develop'a
progress-watchdog olarak bağlandı. 3-katman tek `Sentinel.Assess(signals)` kararında birleşti, **öncelik
SIRASI Katman-3 ÖNCE** olacak şekilde:
1. **Katman-3 backstop İLK kontrol** — `Elapsed >= MaxTotal` ise advisor HİÇ çağrılmadan `Kill`. Backstop
   advisor'dan ÖNCE değerlendirildiği için, "progressing" diyen bir advisor bile mutlak tavanı AŞAMAZ. DF-farkı
   burada YAPISAL: sıralama garantiler — LLM asla sonsuza bekletemez.
2. **Katman-1 taban** — taze çıktı (`SinceActivity < FreshActivity`) → advisor ÇAĞRILMADAN `Continue` (sağlıklı
   koşularda LLM yükü yok); process canlı DEĞİL + stall → `Kill` (kesinlikle ölü).
3. **Katman-2 gri-bölge (NADİR)** — canlı AMA çıktı `GraceUnsure`'dan uzun durmuş → advisor çağrılır
   (`progressing→Continue`, `stuck→Kill`, `needs_human→Escalate`). Advisor yok / advisor hatası /
   enum-dışı → **konservatif `Continue`** (Katman-3'e kadar) — ASLA spurious escalate/kill.

**Advisor seam:** `Advisor.Advise(ctx, lastOutput) (Advice, reason, error)`; tek somut `CommandAdvisor` =
`engine.runnerFunc` aynası, gerçek `claude -p --dangerously-skip-permissions` (subscription auth, key yok, sıkı
timeout) `realclaude` build-tag + `CP_REAL_CLAUDE=1` arkasında; default testlerde STUB (gate-dışı). Parse =
`ParseVerdict` aynası (balanced top-level `{...}`, SON geçerli `advice` enum'ı; malformed/enum-dışı → ERROR,
asla sahte-tavsiye). **Wiring:** F-2 abort-watcher deseni (`developWithSentinel`: develop child-ctx altında,
watcher goroutine periyodik `Assess` → `Kill`/`Escalate` child-ctx'i iptal eder → performer process-group ölür);
`Kill`→task blocked (`sentinel-killed` reason + intervention-needed event, verify/merge YOK, trailer YOK),
`Escalate`→blocked + human-needed event, `Continue`→develop -timeout'a kadar. nil sentinel/advisor → bugünkü
Katman-1+3 davranışı (geriye-uyumlu). Daemon: `-sentinel` (default kapalı) + `-sentinel-max-total` (Katman-3
tavan; 0=`-timeout`) + `-sentinel-grace` + `-sentinel-advisor` (gerçek claude, realclaude build).

## Durum
✅ Kapandı — Katman-1+3 ADR-0016 somutları; **Katman-2 gri-bölge LLM-danışman Faz-2 2C-1'de implemente**
(backstop her zaman kazanır; LLM gate-dışı).
