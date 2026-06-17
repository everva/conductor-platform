# ADR-0008 — Host & concurrency: capability-aware şema, Faz-1 tek-host

## Bağlam
Çok-host gerçek bir ihtiyaç: Linux'te iOS build olmaz → iOS-özel task'lar Mac (iOS conductor) host'unda
koşmalı (**capability-based routing**). Ama bugün tek host çalışıyor; çok-host maliyetli kısmı Faz-2'ye
ertelenebilir. Faz-1 ihtiyacı: tek conductor, poll, 3-4 paralel, her projede ~1.

## Karar
**Şema gününden host+capability-aware; runtime Faz-1'de tek-host.**
- **Şema (Faz-1'de bile):** her host'a kimlik + `capabilities` (örn. davinci: `[ios-build, linux, web]`);
  her **lane**'e `requires` (örn. iOS-lane: `ios-build`). Routing kuralı: `requires ⊆ host.capabilities`.
- **Concurrency (xirigo lease):** global cap ~3-4 worker (host yük tavanı, load-41 dersi) + **repo başına 1
  aktif**. → 3-4 proje paralel, her biri 1 task.
- **Resource cap host-başına** tutulur (Faz-2'de Mac ≠ Linux kapasitesi).

**Faz-1:** tek host (davinci) executor, **lokal-dosya state**. Uzak-executor/ssh/dağıtık-event YAPILMAZ.

**Faz-2 (çok-host) — KRİTİK kural:** "repo başına tek aktif" **host-ÜSTÜ** olmalı; lease/state host-lokal
DEĞİL paylaşımlı olmalı (yoksa iki host aynı repoya yazar → develop karışır). Bu yüzden Faz-1'de bile
**state erişimi Go arayüzü arkasına** konur (impl: lokal-dosya) → Faz-2'de seçim implementasyon değişikliği,
mimari kırılma değil:
- (A) Proje→host pinleme (basit): proje bir ana host'a pinli, sadece iOS-lane başka host'a ödünç.
- (B) Merkezi registry/lease (esnek, karmaşık).

## Gerekçe
Pahalı kısım (uzak executor) Faz-2'ye; ucuz kısım (capability alanları) Faz-1'de → Faz-2 "satır ekle",
refactor değil. Lease modeli xirigo'da kanıtlı.

## Sonuç
- Go çekirdeğinde `StateStore` ve `Host`/`Lease` soyutlaması Faz-1'den itibaren olmalı.
- Capability routing lane seviyesinde (task değil) → daha az etiketleme.

## Güncelleme (ADR-0010)
State backend baştan **merkezi Postgres** seçildi → "Faz-1 lokal-dosya, Faz-2 merkezi'ye geç" maddesi DÜŞTÜ.
"Repo-başına-tek-aktif lease HOST-ÜSTÜ" kritik kuralı Postgres row-lock/advisory-lock ile **baştan** sağlanır;
Faz-2 (A pinleme vs B merkezi) ikilemi büyük ölçüde (B) lehine çözülmüş oldu. StateStore soyutlaması test için
korunur ama birincil backend Postgres. **(ADR-0013 rafine: Faz-1a in-memory/dosya StateStore, Faz-1b Postgres;
birincil backend yine Postgres. Gövdedeki "Faz-1 ... lokal-dosya state" artık "Faz-1a" demektir.)**

## Güncelleme (review: Y1)
`requires` capability **lane seviyesinde** (task değil): lane→capability eşlemesi `lanes` tablosunda tanımlı
(ADR-0010); `tasks.requires` oradan TÜRETİLİR, task'a elle yazılmaz. (Takma-ad: "ADR-c" = ADR-0010.)

## Durum
✅ Faz-1 kapandı. Çok-host executor (uzak ssh/agent) hâlâ Faz-2; state zemini Faz-1b'de merkezi Postgres (ADR-0010/0013).
