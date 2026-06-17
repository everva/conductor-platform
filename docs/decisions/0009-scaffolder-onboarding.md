# ADR-0009 — Scaffolder / onboarding: per-stack profil + asistanlı + readiness-gate

## Bağlam
Jenerik'in en zor kısmı (§5.3): config'siz bir repoyu Kontaktör'e hazırlamak. Brief tuzağı: aşırı-jenerik tek
şablon (hiçbir şeye uymaz) VEYA proje-başına elle (jeneriklik ölür). xirigo'da reçete elle yazılmış
(DESIGN-RULES, CODING-RULES, RUNTIME, scenarios) — bunu jeneriklerştirmek gerek.

## Karar
Scaffolder bir repo için şunu üretir: stack-tespit → develop reçetesi (build/test/lint + pipeline) →
verify reçetesi (deterministik kanıtlar, ADR-0003) → governance defaults (risk-katmanı + per-path human-merge)
→ workspace (klon + gh-token + branch, ADR-0004).

**1. Reçete evi = HİBRİT.** Reçete hedef repo içinde `.conductor/` dizininde yaşar (versiyonlu, şeffaf,
geliştirici görür/değiştirir — xirigo `docs/blueprint` deseni). Platform **registry** yalnız *pointer* tutar
(proje + repo + nerede + durum). Şeffaflık + merkezi keşif birlikte.

**2. Onboarding = asistanlı taslak + insan onayı.** Platform stack-algılar → **per-stack profil
kütüphanesi**nden (node-react, go-api, ios-swift…) uygun profili seçer → repoya özgü doldurur (asistanlı,
LLM yardımıyla ama çıktı deterministik komutlar) → **insan ilk seferde onaylar** → onaylı reçete `.conductor/`'a
yazılır. Yanlış/zayıf reçete riskini ilk kapıda keser.

**3. Readiness-gate.** Repoda güvenilir deterministik kapı kurulamıyorsa (test/e2e/maestro yok), Kontaktör'ün
**ilk task'ı = "kalite altyapısı kur"** (test/e2e/maestro iskeleti). Deterministik kapı oluştuktan SONRA özellik
geliştirmeye geçilir. "Her zaman kalite" daha ilk projede çökmesin diye.

## Gerekçe
Per-stack profil = jeneriklik + isabet dengesi. Asistanlı+onay = hız + güvenlik. Readiness-gate = ADR-0003'ün
(deterministik kanıt şart) ön-koşulunu garanti eder; testsiz repoyu reddetmek yerine kaliteye taşır.

## Sonuç
- **Registry (ADR-c) bir projenin `readiness` durumunu tutmalı** (bootstrap gerekli mi / kuruldu mu).
- **Bootstrap task** kavramı: normal task'lardan önce gelen, kalite-altyapısı kuran özel task.
- Per-stack profil kütüphanesi platformda yaşar (kapsam = implementasyon detayı; ilk hedef stack'ler kullanıcı
  projelerinden: ios-swift, node/medusa, next-web).

## Durum
✅ Kapandı.
