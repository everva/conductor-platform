# Web cockpit — Product-Owner audit + fix plan (kullanıcı 2026-06-24)

Kullanıcı (ürün sahibi gözüyle, ekran görüntüsüyle): *"içeriği okuyamıyorum, hepsi kesilmiş,
açılınca JSON detayını göremiyorum / açılmıyor. Sana PO olarak tek tek incele dedim ama tamamıyla
boş geçmişsin. Bu ürün satışa hazır DEĞİL. Önce bu gözle tüm problemleri listele."*

## 0.0 SAHİPLİK İLKESİ (kullanıcı 2026-06-24, net talimat)
*"Ben her seferinde sana söyleyemem; SEN bunları kontrol et, benim söylediklerimden listesini çıkart."*
→ Ben PROAKTİF ürün sahibiyim: kullanıcının tek bir ekran görüntüsüne REAKTİF davranmam — **ürünün HER
yüzeyini KENDİM açıp (Playwright), gerçek veriyle gezip, TÜM problemleri ben bulup listelerim ve
düzeltirim.** Kullanıcı tek tek göstermek zorunda kalmamalı. "boş geçme" = her yüzey gerçekten açılıp
denetlenecek.

KANIT (compact-öncesi grep): Event Stream İKİ KEZ kesiyor (JS `payloadPreview` 160-char + CSS
`events.css:141 text-overflow:ellipsis`) ve raw-JSON döken TEK yüzey o (izole satış-engeli). Boş
durumlar ("No events/projects/diff/activity yet") VAR — engel değil. a11y kapsamı çoğu bileşende makul.

## 0. KÖK YANLIŞ ANLAMA (dürüst tespit)
Editörün **native VS Code tree** panelini (`editor/src/eventsTree.ts`) güzelleştirdim — okunur satır,
tıkla→JSON, severity. AMA kullanıcının **gerçekten baktığı yüzey** Command Center webview'inde gömülü
**WEB COCKPIT** (`web/src/...`). Ekran görüntüsündeki "Event stream" = `web/src/events/EventStreamView.tsx`.
O hâlâ ham `JSON.stringify(payload)` 160-char kesik (`payloadPreview`), tıkla-aç YOK. **Yani doğru
yüzeyi denetlemedim.** Bundan sonra denetim WEB COCKPIT (ürünün kendisi) üzerinden, PO gözüyle.

> ⚠️ DİSİPLİN (DEĞİŞMEZ, önceki fazlarla AYNI): UYDURMA YOK · FAKE-GREEN YOK · frozen-additive
> (ADR-0021) · her değişiklik web gate (tsc + eslint-0 + vitest) + **Playwright e2e KENDİM koşar**
> (hermetik `web/e2e/` + gerçek-gateway `web/e2e-realgw/` harness'ı KULLAN: gerçek conductor-api +
> seed → ekran görüntüsü) + CI 3-job yeşil (`gh run view`) + ledger/memory. **Görsel kanıt: her
> düzeltmeden sonra Playwright screenshot al, kullanıcıya manuel test ETTİRME.** Token DONMUŞ.
> Canlı optiway main/develop'a DOKUNMA. no isolation:worktree.

## 1. PROBLEM LİSTESİ (PO gözü — "satışa hazır mı?")
Önem: **[S]=satış-engeli · [M]=major · [P]=cila.** Durum: **[K]=kanıtlı (kod/screenshot) · [D]=doğrulanacak (compact sonrası canlı Playwright ile).**

### A. Event Stream — `web/src/events/EventStreamView.tsx` (EKRAN GÖRÜNTÜSÜ)
- **A1 [S][K]** Satırlar ham `JSON.stringify` 160-char kesik (`fleet/format.ts payloadPreview`) →
  okunamaz çöp: `{"checks":[{"evidence":"exit 0",…`. İnsan-okur ifade YOK. (native tree'deki
  `describeEvent` mantığı web'e taşınmalı — paylaşılan veya web kopyası.)
- **A2 [S][K]** Detayı görmenin YOLU YOK — satır tıklanabilir değil, JSON detay paneli/modalı yok.
  ("açılınca göremiyorum / açılmıyor"). → satır tıkla → genişle / yan-panel / modal: tam, biçimli JSON.
- **A3 [M][K]** Sağ taraf taşıyor/kesik (`{"re…`) — layout overflow, wrap/responsive yok.
- **A4 [M][D]** İnsan-okur satır (zaman + sade fiil + severity renk) yok — board/native ile tutarsız.
- **A5 [P][D]** "Resolve" butonu (intervention) + "Pause/Clear/open/9" başlık kontrolleri — çalışıyor
  mu, anlaşılır mı, klavye/erişilebilir mi.
- **A6 [P][D]** Filtreler (All projects/task/All phases/All kinds/intervention-only) — çalışıyor mu,
  keşfedilebilir mi.

### B. Command Center / Board — `web/src/fleet/CommandCenter.tsx`, `fleet/board.ts`
- **B1 [D]** Kart okunabilirliği: diff boyutu/host/faz net mi; "needs review" yeterince öne çıkıyor mu.
- **B2 [D]** Boş durumlar (proje yok / görev yok / bağlantı yok) anlamlı mı yoksa boş mu.
- **B3 [D]** Bulk-approve / scope-clear / "New work" akışları net + güvenli mi.

### C. Session View — `web/src/session/SessionView.tsx`
- İçinde inline diff (sv-diff-*) + verdict + timeline VAR (görece iyi). Doğrulanacak:
- **C1 [D]** Büyük diff okunabilirliği (kaydırma, satır sarma, dosya başlıkları).
- **C2 [D]** Verdict (machine-proven) yeterince öne çıkıyor mu; replay (ok-tuşu/timeline) anlaşılır mı.
- **C3 [D]** Spec/acceptance gösterimi; "no scenario" boş durumu.

### D. Intake — `web/src/intake/` (IntakeChat/ScenarioCard/QuestionCard)
- **D1 [M][D]** Distill (claude) prod'da claude-yok → muhtemelen 502; hata zarif mi, "Write spec
  directly"ye yönlendiriyor mu (claude-siz yol çalışıyor — bkz `docs/INTAKE-CLAUDE-IN-K8S-PLAN.md`).
- **D2 [D]** Spec-first YAML editörü kullanılabilirliği; doğrulama hataları net mi.

### E. Global / shell / ürün-olgunluğu
- **E1 [D]** Hata mesajları (gateway unreachable / 401 / 500) anlaşılır + eyleme dönük mü.
- **E2 [D]** Loading/skeleton durumları; ani boşluk/"flash" var mı.
- **E3 [D]** Bağlantı durumu + reconnect görünürlüğü (web tarafında).
- **E4 [D]** Responsive/taşma (A3 gibi) genel mi; dar webview'de kırılıyor mu.
- **E5 [D]** Erişilebilirlik (rol/aria/klavye/odak), tutarlı tasarım dili (tokens/spacing/renk).
- **E6 [D]** TokenGate / ilk açılış (web) — boş/yönsüz mü.

### META — İKİ paralel "events" yüzeyi (ürün kararı)
- **X1 [M]** Web cockpit Event stream + editör native Events tree = aynı şeyin İKİ kopyası; kullanıcı
  Command Center'da WEB olanı görüyor. Karar: web cockpit kanonik; native tree humanize'i (yaptığım)
  ya web'e hizalanmalı ya da rolü netleşmeli. **describeEvent mantığını tek kaynağa indir** (web'de de
  insan-okur + expand), aksi halde çift bakım + tutarsızlık.

## 2. SIRA (compact sonrası; her madde: oku→Playwright screenshot→düzelt→Playwright doğrula→commit→CI)
1. **GROUNDING:** her web yüzeyini gerçek-gateway harness (`npm run e2e:realgw`) + seed ile aç,
   screenshot al, A–E'yi madde madde KANITLA/güncelle (D→K). Kullanıcıya güncel listeyi ver.
2. **A (Event Stream) — SATIŞ ENGELİ ÖNCE:** A1+A2+A3 → web'de insan-okur satır (describeEvent'i web'e
   getir) + tıkla→genişle tam biçimli JSON + overflow fix. Playwright: satır okunur + expand açılıyor
   + screenshot. (A4–A6 ardından.)
3. **B → C → D → E** sırayla, her biri Playwright-doğrulamalı + screenshot.
4. **X1** ürün kararı: tek event-humanizer kaynağı.
5. ADR + memory + ledger.

## 2.5 GROUNDING SONUÇLARI (2026-06-24, Playwright ile her yüzey GERÇEKTEN açıldı)
Hermetik Playwright + zengin mock veri ile HER yüzey gerçek tarayıcıda açılıp screenshot'landı
(`test-results/po-*.png`). Bulgular ([D]→[K] netleşti):

- **A (Event Stream) — ✅ FIXED + DOĞRULANDI (develop `3e5cf38`, CI yeşil).** A1 insancıl satır
  (`eventText.ts describeEvent`) + A2 tıkla→biçimli JSON (`<pre>`) + A3 overflow fix. Screenshot
  `po-event-stream-expanded.png`: "Writing code · 1m · 3 files", kırmızı "Blocked: …", "Needs your
  review", açık satırda pretty-JSON. Satış-engeli kalktı. (X1 = ADR-0051.)
- **B (Board) — [K] LARGELY SOLID, blocker YOK.** "N tasks need your review" banner + running/
  needs-review/blocked sayaçları + 4 lifecycle kolonu + per-kart Review CTA çalışıyor. **DÜZELTME
  (dürüstlük):** ilk bakışta "needs-review 2 ama 1 kart" sandım → Playwright assertion ile KANITLA:
  W-1 (awaiting-approval) + I-block (blocked) İKİSİ DE render oluyor (yanlış-okuma). Kart metni dim/
  düşük-kontrast AMA bu KASITLI de-emphasis (Needs Review sarı vurgulu, eylem gerektirmeyen kolonlar
  soluk). PO taste kararı → kullanıcı onayı olmadan değiştirilmez.
- **C (Session) — [K] DEVIN-GRADE, blocker YOK.** SPEC (acceptance + holdout), VERIFIER VERDICT
  "MERGE-READY" hero + per-gate checks, ACTIVITY timeline (zaten insancıl: "Review · diff ready" /
  "verdict pass" / "Develop · progress"), DIFF (+6/-0 inline patch), Approve & merge / Abort.
- **D (Intake) — [K] mutlu-yol SOLID.** Konuşma thread (YOU/ASSISTANT), önerilen plan kartı +
  hidden-holdout, Intake YAML editörü, "Write spec directly" (claude-siz yol) + "Approve & add to
  ledger". KALAN minor: D1 claude-down (502) hata-yolu görseli doğrulanmadı (error-route testi
  gerek) — düşük öncelik, kullanıcı k8s-claude güvenlik kararı bekliyor.
- **E (empty) — [K] decent.** "No tasks" (dashed icon) + "No events match" boş durumları var.
  Minor: 0-proje board'unda onboarding CTA yok ("+ New work" var ama yönlendirme zayıf).

**SONUÇ:** Tek gerçek satış-engeli (A) FIXED+verified+CI-yeşil. B/C/D/E büyük ölçüde sağlam;
kalanlar ya kasıtlı tasarım (B dim) ya minor polish (D1/E) ya da kullanıcı-kararına bağlı (D1
claude-k8s). Görsel taste değişiklikleri (kontrast vb.) kullanıcı PO onayı olmadan yapılmaz.

## 3. NOTLAR
- Gerçek veri/gerçek gateway için `web/e2e-realgw/` harness'ı KULLAN (bu oturumda kuruldu: onboard→
  intake→lease → board/stream gerçek conductor-api'ye karşı, mock'suz). Editör-native değişiklikler
  (tour/badge/native-stream) DURUYOR ama bunlar web cockpit DEĞİL — karıştırma.
- "Tek tek incele" = her yüzeyi GERÇEKTEN aç (Playwright), screenshot'a bak, problem listele, düzelt,
  tekrar screenshot. Boş geçme.
