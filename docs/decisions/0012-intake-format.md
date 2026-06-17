# ADR-0012 — Intake format: senaryo şeması + deterministik holdout (karma görünürlük)

## Bağlam
ADR-0005'in somut format detayı. "Her zaman kalite" buradan doğar: senaryo makine-doğrulanabilir kabul
kriterine dönüşmezse kapı (ADR-0003) boştur. DF'nin "gizli holdout" fikri sağlamdı ama LLM-judge ile bozulmuştu;
deterministik yaparsak doğru çalışır.

## Karar
**1. Senaryo şeması (dondu):**
```
Scenario { id, title, lane (capability—ADR-0008), tier (risk—ADR-0003),
           deps[], acceptance[], holdout_ref }
```
Yaşadığı yer: hedef repo `.conductor/scenarios/` (versiyonlu, şeffaf) + registry Postgres pointer (ADR-0010).

**2. Holdout = deterministik test/flow (kod).** Kabul kanıtı gerçek çalışan testlerdir: unit / e2e / maestro
flow → geçer/geçmez, exit-code. ADR-0003 ile birebir; öznel skor YOK. Intake bu testleri üretir/yazar.

**3. Görünürlük = KARMA.**
- Performer **temel testleri görür** (TDD/test-first → hız, doğru yöne geliştirme).
- Ek **gizli holdout** testleri performer'a **görünmez**; yalnız `verify`de koşar (ADR-0002 Verify). → overfit/hile
  koruması (performer testi "geçmek için" hile yapamaz).

**4. Intake akışı:** konuşma (serbest metin) → **asistanlı damıtma** (LLM yardımı) → yapılandırılmış senaryo +
deterministik holdout testleri → **insan onayı** (ADR-0009 deseni) → `.conductor/scenarios/`'a yazılır.

## Gerekçe
Deterministik test = reproducible kapı (DF'nin batma sebebinin tam tersi). Karma görünürlük = TDD hızı +
gizli-holdout overfit koruması. Asistanlı+onay = konuşmadan güvenilir spec.

## Sonuç
- `verify` reçetesi (ADR-0002) gizli holdout'u **ayrı** koşar; performer workspace'ine sızmaz (dosya ayrımı).
- Tier (risk) → governance (ADR-0003) merge-mode + human-merge kapısını sürer.
- lane → host capability routing (ADR-0008).

## Güncelleme (review: K1, D1)
Gizli holdout'un "repo içinde `.conductor/scenarios/`" ↔ "performer görmez" ÇELİŞKİSİ **ADR-0018**'de çözüldü:
public acceptance testleri `.conductor/scenarios/`'da (performer görür); **gizli holdout repo-DIŞI store'da**,
verify'de ayrı verify-worktree'ye geçici enjekte. `holdout_ref` bu repo-dışı store'a işaret eder.
(Takma-ad eşlemesi: "ADR-c"=0010, "ADR-d"=bu ADR (0012), "ADR-e"=0011.)

## Durum
✅ Kapandı (gizli-holdout mekaniği ADR-0018).
