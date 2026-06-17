# ADR-0004 — Branch / merge modeli: kısa-ömürlü per-task branch

## Bağlam
xirigo bugün doğrudan `develop`'a commit ediyor (tek aktör → sorun yok). Ama: (1) insan geliştiriciler de
aynı repoda çalışacak → review-öncesi kod develop'a sızmamalı; (2) branch patlaması istenmiyor; (3) her task
merge edilmeli (sonraki bağımlı task öncekini görsün); (4) "Kontaktör branch'i develop ile güncel kalmalı".
**Tuzak (yaşandı, brief §5.2):** kalıcı tek conductor-branch → squash-divergence + sürekli güncel-tutma derdi.

## Karar
**Task başına kısa-ömürlü branch** `conductor/<proje>/<task-id>`:
1. Taze `develop`'tan çıkar (her zaman güncel — branch yaşamadığı için "geride kalma" imkânsız).
2. develop + verify-gate (ADR-0003) + CI (risk-katmanına göre).
3. Geçerse → **squash-merge develop + branch SİL**. Geçmezse → retry (max 2) → olmazsa **blocked** + bildir.
4. **Repo başına aynı anda 1 aktif** (lease cap) → çakışma/karmaşa yok.

**Kalıcı conductor-branch YOK.** İnsan↔Kontaktör çakışması: taze develop'tan çıktığı için insanın son işini
alır; çakışırsa build/test kırılır → blocked (sessiz bozuk-merge yok).

## Gerekçe
"Güncel kalma" problemini yönetmek yerine **var olmaktan çıkarır.** xirigo'nun "develop hep güncel" hızını
korur + insan-ekibi güvenliğini (review-öncesi kod sızmaz) ekler. Branch ömrü dakikalar → patlama yok.

## Sonuç
- Bağımlı task taze develop'tan çıkar → zincir korunur.
- blocked task'a bağımlılar "ready" olmaz (dep-gate).

## Güncelleme (review: O4, O2)
- **Merge tespiti:** squash-merge commit mesajına `[task:<id>]` trailer → reconcile (ADR-0010/0016) develop'ta
  grep'leyerek task'ın merge'ini DETERMİNİSTİK tespit eder.
- **retry-count:** `tasks.retry_count` (ADR-0010 DDL). retry-cap (bu ADR, max 2) ile time-backstop
  (ADR-0006/0016, ~6h) BAĞIMSIZ tetikler; hangisi önce vurursa blocked — koordinasyon ADR-0016'da.

## Durum
✅ Kapandı.
