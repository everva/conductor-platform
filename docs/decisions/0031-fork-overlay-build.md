# ADR-0031 — Fork paketleme stratejisi: VSCodium-tarzı overlay build (4D-0)

## Bağlam
ADR-0027 fork'un YAPISINI sabitledi: **Code-OSS (MIT) fork + built-in webview extension + gateway reuse**; ayrı repo
`everva/conductor-editor`; rebrand HAFİF (product.json). 4D bunu PAKETLER. Açık soru: Code-OSS'u **NASIL** fork'layıp
build edip rebrand edeceğiz?
- **(a) Hard source-fork:** vscode kaynağını repoya vendor'la, üstünde geliştir. → Her upstream sürümde merge cehennemi,
  divergence birikir; ADR-0027'nin "çekirdeğe derin müdahale YOK" ilkesiyle çelişir.
- **(b) Overlay/patch build (VSCodium/Cursor modeli):** ince bir orkestrasyon reposu, build-time'da pinned Code-OSS'u
  çeker + küçük yama seti uygular + built-in extension enjekte eder + standart pipeline ile build eder.

## Karar
**Overlay build (b).** `everva/conductor-editor` = vscode kaynağını VENDOR'LAMAYAN ince bir **orkestrasyon** reposu:
- **Pinned Code-OSS tag** (`upstream.json`): build-time'da `git clone --branch <tag> microsoft/vscode`. İlk pin
  **1.122.1** (`.nvmrc` Node **22.22.1**; host'ta nvm ile eşlenir). git-lfs gerekir (asset smudge).
- **`patches/`**: kaynağa uygulanan KÜÇÜK yama seti — yalnız `product.json` rebrand + gerekli minimum. Çekirdek/chrome'a
  derin müdahale YOK (ADR-0027). Her tag-bump = bir patch-refresh.
- **Built-in extension inject**: conductor extension `conductor-platform/editor/`'de KALIR (tek kaynak); fork onu
  `vsce` ile paketleyip Code-OSS `extensions/`'a built-in olarak kopyalar (build öncesi). Sürüm pin'i `upstream.json`'da.
- **`build/` scriptleri**: `get-vscode` (clone+pin), `prepare` (patch uygula + extension inject), `build` (gulp
  `vscode-darwin-arm64`; platform kullanıcı kararı: macOS Apple Silicon önce).
- Repo KÜÇÜK kalır (scriptler + patch'ler + CI); `vscode/` çalışma ağacı **gitignore**.

## Gerekçe
- **Bakım**: overlay = minimal divergence; tag-bump = clone + patch-reapply (çatışırsa küçük refresh). Hard-fork = kalıcı
  merge yükü. VSCodium/Cursor/Windsurf hepsi bu modeli kullanıyor — kanıtlı.
- **ADR-0027 uyumu**: yama yalnız branding + inject → "rebrand HAFİF, derin müdahale yok" birebir.
- **Tek-kaynak**: extension `editor/`'de kalır; fork yalnız paketler → drift yok (ADR-0029 ruhu: web/ + editor/ tek kopya).
- **Reproducible**: tag + Node sürümü pinli; CI aynı pipeline'ı koşar.

## Kapsam (4D dalgaları)
- **4D-0**: orkestrasyon repo iskelesi + Code-OSS clone + build (mac arm64) — **toolchain doğrula** (de-risk).
- **4D-1**: extension built-in inject (vsce paketle → `extensions/`'a kopyala).
- **4D-2**: `product.json` rebrand patch (ad/ikon/ürün-bilgisi; marketplace/telemetri Code-OSS-temiz kalır).
- **4D-3**: CI/release — fork CI + paketli/imzalı artifact (en az bir platform).

## Frozen kontratlara etki
**HİÇBİRİ.** `conductor-platform`'ın Go/web/editor kaynağı dokunulmaz; fork AYRI repo. Extension kaynağı `editor/`'de
kalır (yalnız `vsce` ile paketlenir). gateway (3A) olduğu gibi reuse.

## Durum
✅ Karar (4D-0). Overlay build; pinned tag 1.122.1 / Node 22; `patches/` (rebrand) + built-in-extension-inject; macOS
Apple Silicon önce. `everva/conductor-editor` private repo rezerve edildi (kullanıcı onayı).
