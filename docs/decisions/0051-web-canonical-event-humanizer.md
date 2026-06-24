# ADR-0051 — Web-canonical event humanizer (okunur stream + tek kaynak kararı)

## Bağlam
Kullanıcı (PO, 2026-06-24) Command Center'daki **web cockpit Event stream**'in ekran görüntüsünü
attı: her satır ham `JSON.stringify(payload)` döküyor, İKİ KEZ kesik (`fleet/format.ts
payloadPreview` 160-char + `events.css text-overflow:ellipsis`) ve detayı açmanın YOLU YOK —
*"hepsi kesilmiş, açılınca JSON detayını göremiyorum / açılmıyor."* Bu, ürünün sattığı asıl
yüzeyde bir SATIŞ-ENGELİ (`docs/WEB-COCKPIT-PO-AUDIT-PLAN.md` §1.A1/A2/A3).

İlgili olarak editörün NATIVE tarafında zaten bir insancıl-leştirici var:
`editor/src/activity.ts describeEvent` — bir olayı tek satır düz dile çevirir (kind/step/result
switch). AMA o Türkçe ("Geliştiriyor… 4dk · 2 dosya") ve editör host-tarafı dev yüzeyi (native
Events tree / status-bar "Now") için. Web cockpit (satılan ürün) bunu KULLANMIYORDU. → META X1:
"aynı şeyin iki kopyası; tek humanizer kaynağı kararı gerek."

## Karar
**Web cockpit, kendi İngilizce `describeEvent`'ine sahip ve ürün için KANONİK olan budur; native
Türkçe varyant host-tarafı dev konforu için bilerek AYRI kalır.**

- Yeni `web/src/events/eventText.ts` `describeEvent(e: Event): { text, level }` — native'in
  birebir mantıksal aynası (progress→step/elapsed/files, decision→blocked-reason / "Awaiting
  approval", intervention→"Needs your review", diff/merge/pr/log/health), ama İngilizce kopya
  (web cockpit'in tüm metni İngilizce — "Event stream", "All projects"…). PURE + token-FREE;
  yalnız olayın sınıflandırıcı alanları + gateway'in snake_case payload anahtarları
  (`elapsed_seconds`/`files_changed`/`result`/`summary`/`step`). Bilinmeyen kind → "phase / kind",
  asla crash.
- `EventStreamView` her satırı bu satıra çevirir (ham JSON değil) + satır artık bir **disclosure
  button**: tıkla → TAM, biçimli JSON (`<pre>`, pretty-printed) açılır (A2). Çöken satır ellipsis,
  JSON kendi içinde kaydırır (A3). Hiçbir şey gizlenmez — ham gerçek bir tık ötede.

**Neden tek paylaşılan modül DEĞİL:** iki yüzeyin DİLİ farklı (web=İngilizce ürün, native=Türkçe
dev). Mantığı paylaşmak için diller arası bir i18n katmanı gerekirdi; bu, iki küçük saf fonksiyon
için aşırı-mühendislik. Karar: **ürün için web kanonik**; native'i ona hizalamak yerine rolü
netleştir (host-tarafı dev konforu). İleride native İngilizce'ye geçerse `eventText.ts` paylaşılan
cockpit barrel'a (web/src/cockpit.ts) taşınıp editörde reuse edilebilir — şimdilik gerekmiyor.

Frozen-additive (ADR-0021): yalnız web/src eklendi; `payloadPreview` diğer çağıranlar için DURUYOR;
Go / token yüzeyi DOKUNULMADI.

## Doğrulama
- `web/src/events/eventText.test.ts` (8 test): her kind → düz satır + severity; snake_case payload
  okuması; truncate; bilinmeyen-kind degrade.
- `web/src/events/EventStreamView.test.tsx` (a2): satır insancıl satır gösterir (ham JSON DEĞİL) +
  tıkla → biçimli JSON açılır/kapanır (aria-expanded).
- `web/e2e/dashboard.spec.ts` (GERÇEK tarayıcı, hermetik): "Writing code · 1m · 3 files" /
  "Blocked: …" / "Needs your review" görünür; ham `"step":"developing"` YOK; tıkla → `<pre>` JSON;
  screenshot `test-results/po-event-stream-expanded.png`.
- Web gate yeşil (tsc + eslint-0 + vitest 185/185) + CI 3-job yeşil (develop `3e5cf38`).
