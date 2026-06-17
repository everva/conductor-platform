# ADR-0005 — Intake katmanı: konuşma → senaryo + holdout damıtma

## Bağlam
Kullanıcının çekirdek isteği: "benimle yazışarak task/senaryo/holdout üretmek", sonra Kontaktör bunları
otonom alsın. xirigo'da senaryolar **elle** plan dosyasına yazılıyor — bu katman xirigo'da YOK.
"Her zaman kalite" garantisi burada başlar: senaryo = makine-doğrulanabilir kabul kriteri olmazsa kapı boştur.

## Karar
**Intake/spec-damıtma platformun birinci-sınıf parçasıdır.** Konuşma çıktısı:
1. **Kabul-kriterli senaryo** (xirigo scenario-spec modeli: id, tier, lane, deps, acceptance bullets).
2. **Deterministik holdout reçetesi** — senaryoya bağlı, makine-doğrulanabilir kanıtlar (test/maestro/e2e),
   öznel LLM-skoru DEĞİL (ADR-0003 ile tutarlı).

Asistanlı: konuşma serbest-metni → yapılandırılmış senaryo+holdout'a damıtılır; insan onaylar.

## Gerekçe
Kalite kapısının çapası senaryonun (B)-formu (kabul kriteri) olmasıdır. Intake bunu zorlar.

## Sonuç
- Registry/state şeması (ADR-c) bu senaryo formatını taşımalı.
- Holdout reçetesi verify-gate (ADR-0003) tarafından koşulur.

## Durum
✅ Kapandı. Senaryo formatı **ADR-0012**'de, gizli-holdout izolasyonu **ADR-0018**'de, asistanlı damıtma
zamanlaması **ADR-0013** (Faz-1b) ile donduruldu.
