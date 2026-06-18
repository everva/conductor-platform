# ADR-0023 — Görsel-verify reçetesi (deterministik görsel-diff)

## Bağlam
Web (ve iOS, 2A-3) projeleri için kalite kapısı görsel/davranışsal doğrulama gerektirir — xirigo'nun çekirdek
gücü: claude-design referansı ↔ gerçek render 1:1. DF battı çünkü LLM'i öznel kalite-skoruna koydu. Bizim
ilkemiz: kalite kapısı yalnız DETERMİNİSTİK kanıt (ADR-0003). Soru: görsel-verify yeni bir verify-TİPİ mi,
yoksa mevcut çatıya oturur mu?

## Karar
**Görsel-verify, yeni bir verify-type DEĞİL — deterministik bir reçete GATE'idir** (komut exit-code'u).
Mevcut `CommandEngine` + `verify.Gate{Name, Argv}` modeli bunu olduğu gibi taşır; yeni soyutlama gerekmez.
- **Mekanizma = render → diff → exit-code:** (a) RENDER: web'de Playwright headless screenshot (iOS'ta maestro,
  2A-3); (b) DIFF: deterministik **piksel/threshold** karşılaştırma → exit 0 (pass) / non-0 (fail). Playwright'in
  yerleşik `toHaveScreenshot(threshold)` VEYA bağımsız bir image-diff aracı kullanılır. **Öznel-skor / LLM-judge
  YOK** (ADR-0003/0006). Eşik (threshold) reçetede açık, deterministik.
- **Referans görseller = holdout-benzeri (ADR-0018):** beklenen ekran görüntüleri repo-DIŞI (holdout store:
  `store://`/`pg://`/`private:`) VEYA versiyonlu `.conductor/` altında; verify sırasında karşılaştırılır.
  claude-design çıktısı referans olabilir (xirigo deseni). Performer referansı göremez/değiştiremez (gizli-holdout
  gibi enjekte) → görselleri "ezberleyip" sahte-yeşil yapamaz.
- **Reçete:** scaffolder "web" profili → develop (claude: design + kod) + gate'ler: build/test/lint **+ visual-diff
  gate**. Gate diff aracını çağırır; çıktı non-revealing (hangi piksel kaç → bulgu özetinde değil).
- **Araç-bağımlılığı dürüstçe:** render aracı (Playwright browser / maestro) operatör kurar; kurulu değilse gate
  deterministik FAIL (eksik-binary, fix-#1 deseni) — sessiz-skip YOK. DIFF mantığı (kararın kendisi) deterministik
  ve araçtan bağımsız doğrulanabilir (saf image-diff).

## Gerekçe
- Görsel-diff zaten "komut → exit-code"; Gate modeline tam oturur → mimari sade kalır (5-fiil arayüz frozen, yeni
  reçete = yeni config, kod yok — ADR-0002).
- Referansı holdout-benzeri repo-dışı tutmak overfit/sahte-yeşili engeller (ADR-0018 ile aynı gerekçe).
- Deterministik threshold → "1:1 görsel" iddiası öznel-skor olmadan karşılanır (xirigo kanıtı).

## Sonuç (2A-2 kapsamı)
- Deterministik **image-diff gate** (piksel/threshold, exit-code) — browser'sız offline doğrulanabilir çekirdek.
- Web reçete profili (scaffolder) + referans-görsel holdout entegrasyonu.
- Playwright render entegrasyonu (browser kuruluysa canlı; değilse image-diff gate ile offline kanıt).

## Durum
🔄 Uygulanıyor (2A-2). iOS/maestro görsel reçetesi 2A-3 (aynı gate deseni, render=maestro).
