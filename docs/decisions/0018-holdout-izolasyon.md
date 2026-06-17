# ADR-0018 — Holdout izolasyon mekanizması (gizli holdout nasıl gizli kalır)

## Bağlam
Review (K1): ADR-0012 "senaryolar `.conductor/scenarios/` (repo içinde)" ↔ "gizli holdout performer görmez"
ÇELİŞİYOR — performer repoyu klonlayınca holdout'u görür. Saklama/enjeksiyon mekanizması tanımsızdı. Kullanıcı
kararı: gizli holdout TUTULSUN + somutlaştırılsın (overfit/hile koruması platformun değer önermesi).

## Karar
**1. İki katmanlı test ayrımı:**
- `.conductor/scenarios/` (repo'da, versiyonlu) → yalnız **public acceptance testleri**. Performer bunları
  görür/yazar (TDD, ADR-0012 karma görünürlük "temel testler").
- **Gizli holdout testleri repo-DIŞI** → platform store (Faz-1a: ayrı yerel dizin/private ref; Faz-1b:
  Postgres veya ayrı private repo). **Develop workspace'ine ASLA yazılmaz/klonlanmaz.**

**2. Enjeksiyon (verify anında):**
- Develop, kendi worktree'sinde (ADR-0017) public testlerle çalışır; gizli holdout'u hiç görmez.
- Verify: **ayrı verify-worktree** açılır → develop çıktısı (commit) + gizli holdout testleri **geçici** enjekte
  edilir → koşulur → verify-worktree silinir. Performer bir sonraki retry'da da görmez.

**3. Doğrulama (Rule#9, negatif test):** holdout'u kasıtlı kıran bir task → merge ENGELLENMELİ (blocked).
Sessiz-yeşil olursa izolasyon kırık demektir.

## Gerekçe
Repo-dışı store + verify-zamanı enjeksiyon = "performer görmez" ile "deterministik kanıt" aynı anda sağlanır.
Çelişki çözülür. xirigo'da gizli-holdout yoktu (yeni mekanizma) → negatif-test ile kanıtlanır.

## Sonuç
- ADR-0012 `holdout_ref` bu repo-dışı store'a işaret eder.
- ADR-0010 store şeması (Faz-1b Postgres) + ADR-0017 verify-worktree.
- Faz-1a'da gizli holdout ayrı yerel dizinden enjekte edilir (Postgres'siz).

## Durum
✅ Kapandı (K1).
