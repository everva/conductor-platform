# ADR-0014 — Performer kontratı + LLM çıktı dayanıklılığı

## Bağlam
İki review'ın en kritik bulgusu (K2+K3): ADR-0002 "CommandEngine reçete komutunu subprocess çağırır" diyor
ama (a) LLM-performer pipeline'ı (architect→test→dev→review) GERÇEKTE nasıl koşuyor tanımsız — "havada";
(b) `claude -p` çıktısı güvenilmez (boş/bozuk JSON, timeout, rate-limit, "Not logged in") ve bu hiç ele
alınmamış — DF'nin batma sınıfının operasyonel kardeşi.

## Karar
**1. Performer kontratı (develop_cmd / verify_cmd):**
- Reçetede tanımlı komut. **stdin:** `{scenario, public_acceptance_tests, design/code_rules, workspace_path,
  pointers}`. **stdout:** schema-forced **Verdict JSON** (prose yok; ADR-0002 şeması).
- **Pipeline performer-İÇİNDE koşar** (`claude -p` + pipeline-prompt/skill — xirigo `pipeline-run` referans:
  architect→ADR→test-first→dev→self-heal→visual/regression→internal-review). **Go orkestre ETMEZ.**
- Self-heal retry performer-içinde; max-attempt reçetede (xirigo: max 2).
- **Go engine'in işi YALNIZ:** subprocess'i doğru env'le (ADR-0015) sür + timeout + çıktıyı parse/normalize.

**2. LLM çıktı dayanıklılığı (zorunlu, DETERMİNİSTİK):**
- **JSON-extraction:** prose/markdown içinden son geçerli JSON bloğunu çek.
- **parse-fail** → `blocked: malformed-verdict` (sessiz retry YOK — DF'nin 7-fallback + kör-retry tuzağına düşme).
- **timeout / boş çıktı** → `blocked: no-verdict`.
- **"Not logged in" / auth-expiry** algıla → tüm tick'i DUR + bildir (kickstart etme — auth duvarına çarpma).
- **Sahte-yeşil ASLA** (ADR-0003): emin değilsen blocked, merge yok.

## Gerekçe
"Subprocess + JSON parse" soyutlaması ancak somut kontrat + hata-yönetimiyle çalışır. Pipeline'ı
performer-içinde tutmak xirigo'da kanıtlı (CommandEngine'i jenerik tutar). Deterministik hata-yönetimi DF'nin
operasyonel batma sebebini kapatır.

## Sonuç
- ADR-0002 imzaları AYNI kalır; bu ADR onları somutlar. `engine/` paketinin çekirdek sözleşmesi budur.
- Faz-1a'nın ilk test ettiği şey (ADR-0013): bu kontrat gerçek `claude -p` ile çalışıyor mu.

## Durum
✅ Kapandı (K2+K3).
