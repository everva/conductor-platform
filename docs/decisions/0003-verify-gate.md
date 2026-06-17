# ADR-0003 — Verify-gate: deterministik kanıt, lokal-öncelikli, risk-katmanlı

## Bağlam
"Her zaman kalite" ancak makine-doğrulanabilir kanıtla garanti edilir. DF öznel LLM-skoruyla başarısız oldu.
Ayrıca CI'ı merge-öncesi kapı yapmak (DF/optiway) yavaş + load patlaması (load-41) üretiyor.

## Karar
Kalite kapısı iki parça:
1. **Deterministik kanıt:** unit/lint/build yeşil + (UI) maestro headless exit-code 0 + görsel diff ≤%5 +
   e2e. Skor değil, geçer/geçmez.
2. **Bağımsız taze-göz review:** kodu yazmayan ayrı agent → **façade-reddi** (her interaktif eleman gerçek
   state/API binding'ine izlenir), design/coding kuralları.

**Öznel LLM-skoru ASLA gate değildir** (en fazla advisory).

**CI konumu:** Kapı **lokal** koşar (exit-code anında, remote-poll yok). CI = **post-merge güvenlik ağı**
(bloklamaz; kırılırsa fix-task/alert). Lokal ortamı CI'a yakın tut (aynı komut/runtime; gerekiyorsa lokal
container).

**Risk-katmanı:**
- Düşük risk (normal feature/UI) → lokal-gate yeter, CI post-merge. **Hızlı.**
- Yüksek risk (auth, **migration**, money/payment, RBAC) → CI **pre-merge zorunlu** + insan-onayı.

## Gerekçe
Deterministik = reproducible = "her zaman kalite". Lokal-gate = hız + load-storm yok. Risk-katmanı =
hız↔güvenlik dengesi; migration-auto-ship defect'inin panzehiri.

## Sonuç
- Per-path pattern listesi (schema.prisma, migrations/, auth, roles.guard) governance'ta tanımlanır.
- "yeşil değilse merge yok" kuralı korunur ama gate lokal kanıttır, CI değil.

## Güncelleme (review: Y6)
xirigo RUNTIME.md dersi: macOS'te **lokal Docker BANNED** (thermal crash). "Gerekiyorsa lokal container" satırı
macOS'te gerçekçi değil → container-gerektiren gate'ler (migration-safety, integration) **lokal-imkansız**:
işaretle-ve-atla (pre-prod'a ertele) veya remote-runner. Yüksek-risk migration lokal deterministik
doğrulanamıyor — bu kabul edilir ve **görünür** kılınır (sessiz atlama yok).

## Durum
✅ Kapandı. (Per-path pattern detayı governance ADR'sinde genişleyecek.)
