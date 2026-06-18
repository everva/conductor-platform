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

## Uygulama (2A-2)

### Deterministik çekirdek: `cmd/imagediff`
Bağımsız, bağımlılıksız (stdlib `image/png`) PNG piksel-diff aracı — reçetenin gate
olarak çağırdığı runnable. **Browser GEREKTİRMEZ.** Kontrat:

    imagediff <actual.png> <expected.png> -threshold <frac 0..1>
      exit 0  -> farklılaşan piksel oranı <= threshold  (PASS)
      exit 1  -> oran > threshold                         (FAIL, non-revealing özet)
      exit 2  -> usage/IO/decode/boyut-uyuşmazlığı/eksik dosya (net hata = det. FAIL)

- **Karar = saf fonksiyon:** iki dosya + threshold → oran; rastgelelik yok. Piksel
  testi RGBA-bileşen tam eşitlik; anti-aliasing toleransı THRESHOLD ile ifade edilir.
- **Non-revealing:** FAIL özeti yalnız oran/threshold; hangi piksel/renk farklı — ASLA.
  Performer referansı geri-mühendislik edemez.
- **Eksik render aracı = eksik actual.png → exit 2 (net hata), sessiz-skip YOK.**
- 64 MP üstü görsel reddedilir (det. bellek tavanı).

### Web reçete profili (scaffolder)
`StackWeb` = Node + `playwright.config.*` (Node'dan ÖNCE algılanır). Profili: standart
Node gate'leri (build/test/vet/lint) + **visual gate** (5. ve SON gate):

    visual: [imagediff, .conductor/visual/actual.png, .conductor/visual/reference.png, -threshold, 0.02]

`.conductor/config.yaml` `recipe.verify.visual` alanına serialize olur; `LoadRecipe`
beş gate'i sıralı okur (build,test,vet,lint,visual). Yollar sabit konvansiyon
(`VisualActualPath`/`VisualReferencePath`): render adımı actual'ı oraya yazar, holdout
referansı oraya enjekte eder.

### Referans = repo-DIŞI holdout (ADR-0018 yeniden-kullanım)
Beklenen ekran görüntüsü gizli-holdout gibi akar: `store://references/<id>/spec.yaml`
→ `<root>/references/<id>/inject/.conductor/visual/reference.png` verify-worktree'ye
enjekte edilir. Visual-diff gate = holdout komutu: `imagediff <actual> <reference>` ayrı
verify-worktree'de koşar (actual performer HEAD'inden checkout, reference enjekte). Performer
referansı GÖRMEZ → ezberleyemez.

### Playwright render (best-effort, dökümante)
Render adımı opsiyonel/araç-bağımlı: `npx playwright` headless screenshot → actual.png,
sonra imagediff gate karşılaştırır. Bu host'ta canlı doğrulandı: `npx playwright install
chromium` başardı; `file://` HTML sayfası screenshot'landı; aynı-sayfa re-render vs referans
→ PASS (exit 0), farklı sayfa → FAIL (exit 1). Browser kurulamasa bile DETERMİNİSTİK gate
PNG'leri doğrudan vererek (browser'sız) kanıtlanır — e2e böyle koşar.

## Durum
✅ Tamam (2A-2). Deterministik image-diff gate (`cmd/imagediff`) + web reçete profili +
referans-holdout entegrasyonu; e2e: visual PASS→merge / FAIL→blocked (browser'sız). Playwright
render canlı doğrulandı.

✅ iOS/maestro görsel reçetesi (2A-3) TANIMLI: scaffolder `StackIOS` profili AYNI gate desenini
kullanır — render=maestro (`maestro test <flow>` UI-flow gate) + AYNI deterministik visual gate
(`imagediff`, web ile byte-byte aynı argv) + `xcodebuild build/test` (Mac-host, Dalga-B-canlı). Lane
`requires: ios-build` capability hook'u `.conductor/config.yaml`'a serialize olur (ADR-0008;
yönlendirme 2B-2). maestro/xcodebuild operatör-kurar; yoksa gate deterministik FAIL (sessiz-skip YOK).
**Offline kanıt:** reçete round-trip (`LoadRecipe`→build/test/maestro/visual + Requires), visual-gate
reuse (imagediff PASS/FAIL), eksik-binary→FAIL, eksik-actual→FAIL. **Dalga-B'ye ertelendi (Mac-host):**
canlı `xcodebuild test` + maestro-on-simulator (gerçek ekran-görüntüsü üretip visual gate'e besler).
