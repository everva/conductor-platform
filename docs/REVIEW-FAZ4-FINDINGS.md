# Faz-4 Adversarial Review — Bulgular + Düzeltmeler (2026-06-19)

Faz-4 (4A transport seam + paylaşılan cockpit; 4B extension iskele→connect/auth→postMessage köprü→webview 3B reuse;
4C-0 diff-kaynağı kararı; 4C-2 inline kontrol komutları) için **4 paralel salt-okunur review-agent** (security /
correctness / contracts-invariants / concurrency-lifecycle) + orchestrator bağımsız doğrulama. Kapsam: `editor/` tüm
ağaç + `web/src` 4A değişiklikleri + CI configleri + ADR 0027-0030. develop @ 0adc698.

## Mimari invariant'lar — HEPSİ TEMİZ (PASS)
- **Frozen kontratlar byte-untouched**: `git diff 23c4e6a..HEAD` Faz-4 aralığında **HİÇBİR `.go` dosyası değişmedi**
  (engine.go + StateStore arayüzü + EventBus arayüzü dokunulmadı; ADR-0021). Faz-4 editor/web/docs/CI-only.
- **web/ editör tarafından dokunulmadı**: `web/` değişiklikleri yalnız 4A-1/4A-2 + CI-stabilize commit'lerinden; hiçbir
  4B/4C (editor) commit'i web/'e yazmıyor. Editör `web/src/cockpit.ts`'i `@cockpit` alias ile KAYNAK okur (ADR-0029).
- **events.gen.ts tek-kaynak**: editor/'da kopya YOK; webview alias üzerinden transitive tüketir.
- **Sahte-yeşil YOK**: token-leak guard'ları her değeri serialize edip sentinel'in YOKLUĞUNU iddia eder + non-vacuous
  (önce token'ın izinli sink'lere ULAŞTIĞINI doğrular); SSRF-guard testi fetch'in ÇAĞRILMADIĞINI iddia eder.
- **ADR uyumu**: 0027 (token SecretStorage, webview'e girmez) / 0028 (barrel + eslint sınır) / 0029 (cross-dir alias,
  editör kendi React'i, web/ dokunulmaz) / 0030 (KindDiff yalnız-karar, yarım-impl yok) — hepsi uyumlu.
- **CI editor job webview'i gerçekten gate'liyor**: `typecheck` host+webview tsc (alias-çözümlü → bridge transport'ları
  FleetDashboard prop'larıyla tip-uyumsuzsa patlar) + build iki bundle üretir.

Security + correctness mercekleri: **0 CRITICAL / 0 HIGH** (token izolasyonu uçtan-uca doğrulandı; SSRF guard WHATWG
parser'a karşı ampirik test edildi — origin kaçışı yok; 4A davranış-koruması truth-table ile kanıtlandı). Tüm material
bulgular concurrency/lifecycle merceğinden geldi.

## Düzeltilen bulgular
- **[HIGH] hostBridge yinelenen `event-subscribe` id → ilk WS handle sızıntısı** (`hostBridge.ts`): id webview-üretimli
  (untrusted sınır); aynı id ile iki subscribe ilk soketi `close()`'suz map'ten düşürüyordu (fd + gateway-bağlantı
  sızıntısı + webview demux bozulması). Üretim webview'i monotonik id ürettiği için tetiklenemez, ama host untrusted
  frame'in sınırı (SSRF/token guard'ları tam bu yüzden var). **Düzeltme:** overwrite'tan önce mevcut handle'ı kapat +
  `onClose` HANDLE-KİMLİĞİYLE sil (geç kapanan eski soket yeni handle'ı evict etmesin). Regresyon testi eklendi.
- **[MED] FleetViewProvider re-resolve → önceki HostBridge sızıntısı** (`extension.ts`): VS Code bir view'ı (retain
  kapalı) gizleyip tekrar resolve edebilir; önceki bridge onDidDispose fire etmeden re-resolve olursa sızıyordu.
  **Düzeltme:** provider yaşayan `#bridge`'i tutar, re-resolve başında öncekini dispose eder. Regresyon testi eklendi.
- **[MED] webviewTransport `subscribeToMessages` unsubscribe-thunk'ı atılıyordu** (`webviewTransport.ts`): seam temizlik
  kontratı vaad ediyor ama dinleyici hiç kaldırılmıyordu (dead `removeEventListener`). **Düzeltme:** thunk yakalanır,
  bundle `dispose()` döndürür (dinleyiciyi kaldırır + uçuştaki REST'leri reject eder → host-hiç-yanıtlamaz LOW'unu da
  teardown'da kapatır). `main.tsx` artık `subscribeToMessages`'i reuse eder (DRY; seam dead-code değil). Test eklendi.
- **[LOW] hostBridge `attach()` re-entrancy**: ikinci attach ilk dinleyiciyi sızdırıyordu → `attach()` önce öncekini
  dispose eder.
- **[LOW] connection.ts bayat "cache" yorumları**: `#token` field'ı kaldırıldığından "clear/cache it" yorumları
  yanıltıcıydı → SecretStorage-merkezli ifadeye düzeltildi (davranış değişmedi).
- **[LOW] `makeNonce` Math.random → `node:crypto` randomBytes**: CSP nonce sır değil ama crypto-kaynak daha iyi hijyen
  (alfanümerik korundu → mevcut nonce-format testleri geçer).
- **[LOW] `eventTransport` referential-stability doküman notu** (`useEventStream.ts`): seam effect-bağımlılığı; her
  render'da taze obje reconnect-fırtınası yapar → "module-scope'ta bir kez kur" uyarısı eklendi.

## Ertelenen bulgular (gerekçeli — kabul edilebilir tasarım, defect değil)
- **[LOW] webviewTransport: host hiç yanıtlamazsa uçuştaki REST promise hiç settle olmaz** — webview ömrüyle sınırlı +
  artık `dispose()` teardown'da reject ediyor. Per-request timeout ertelendi (spurious-reject riski; düşük değer).
- **[LOW] fork "not connected" REST → generic unreachable (401 değil)** — savunulabilir: webview re-prompt edemez, auth
  host'ta. Köprü semantiğini değiştirmek (no-token'ı rest-response 401 yapmak) gereksiz; ertelendi.
- **[LOW/info] `isSafePath` REST path'inde query-string'e izin verir** (`/x?token=…`) — inert: REST auth header'dır,
  gateway non-WS rotalarda `?token=`'ı yok sayar; origin kaçışı yok (ampirik doğrulandı). Sadece guard adı sanitizasyon
  ima ediyor; tetiklenebilir değil.
- **[LOW] `onStateChange` statusBar dispose sonrası yazabilir** — VS Code `.text` setter dispose sonrası tolere edilen
  no-op; deactivate-restore yarışı nadir. Düşük değer; ertelendi.

## Sonuç
Çözülmemiş CRITICAL/HIGH yok. 1 HIGH + 2 MED + 4 LOW düzeltildi (3 regresyon testi); 4 LOW gerekçeyle ertelendi.
Bağımsız doğrulama (Rule#9): editör gate yeşil (host+webview tsc + eslint + **108 vitest** + iki bundle) + CI 3-job.
Mimari invariant'lar (frozen-Go, web/-editör-dokunmaz, events.gen tek-kaynak, sahte-yeşil-yok) korunuyor.
