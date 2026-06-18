# ADR-0022 — Merge sonrası remote-push / deploy modeli

## Bağlam
GitMerger başarılı squash-merge'i **yalnız sahip-klonun yerel base branch'ine** uyguluyor; gerçek remote'a
(`origin`) **push YOK** (Faz-1 boyunca dogfood/test güvenliği için kasıtlıydı). Paylaşımlı bir GitHub remote'a
gerçek üretimde, merge edilen işin yayılması için push gerekir. Ayrıca Faz-2 çok-host'ta (state merkezi PG)
remote, host-üstü tek doğruluk kaynağıdır → push birinci-sınıf olur. (PHASE-2-PLAN 1.5-c.)

## Karar
**Opt-in remote-push, default KAPALI (yerel-merge davranışı korunur — geriye dönük uyumlu).**
- **Flag/env:** `-push` / `CONDUCTOR_PUSH` (bool, default false) + `-push-remote` / `CONDUCTOR_PUSH_REMOTE`
  (default `origin`). Açıkken: başarılı squash-merge sonrası `git push <remote> <base>` çalışır.
- **Auth:** gh-token credential helper (ADR-0017 deseni; deploy-key yasak). Token env'den (`CONDUCTOR_GH_TOKEN`/
  `GH_TOKEN`); asla loglanmaz/commit'lenmez; hatalar redact'li.
- **Push-fail davranışı (KRİTİK — sahte-yeşil değil, sessiz-kayıp değil):** merge ZATEN yerelde indi; push
  başarısızsa task **done kalır AMA** açık bir `push-failed` event/log yayılır (merge-edildi-ama-remote-güncellenmedi).
  Tick "merged" döner; push hatası ayrı sinyaldir. Yerel base ref ilerlemiştir, bir sonraki tick yeniden merge
  ETMEZ (iş bitti) → başarısız push **operatör görünürlüğü** gerektirir. Otomatik push-retry/monitor Faz-2'ye
  (üretim sertleştirme) bırakılır; bu ADR yalnız "push fail sessizce kaybolmaz" garantisini verir.
- **Atomiklik:** push, merge'in ardından ayrı adımdır; merge-yerel + push-fail-event modeli, "merge yapılmadı
  ama push denendi" tutarsızlığını önler (önce yerel merge, sonra push).

## Gerekçe
- Default-kapalı → klon/dogfood/test güvenli (gerçek remote'a yanlışlıkla yazmaz; Faz-1 davranışı aynen sürer).
- Opt-in → üretimde paylaşımlı remote'a yayılım.
- Push-fail'i task'ı düşürmeden görünür kılmak: merge zaten yerelde geçerli (Rule#9: deterministik gate'ten
  geçti), push bir teslim adımı; başarısızlığı yutmak (sessiz-kayıp) veya merge'i geri almak (boşa-iş) yanlış olur.

## Kapsam dışı (sonraki)
- **Remote-executor modeli** (ssh vs conductor-agent) ayrı bir karardır (PHASE-2-PLAN 2B-0) → **ayrı ADR**
  (Dalga B başında). Bu ADR yalnız merge-sonrası push/deploy'u kapsar.
- Otomatik push-retry / "needs-push" kuyruğu / deploy-pipeline tetikleme → Faz-2 üretim sertleştirme.

## Sonuç
- GitMerger'a opt-in push adımı (additive); daemon `-push`/`-push-remote`/gh-token wiring.
- Default davranış (yerel-merge) değişmez; mevcut testler/e2e yeşil kalır.

## Durum
✅ Uygulandı (1.5-c). GitMerger'a opt-in push (additive `WithPush(PushConfig{Enabled,Remote,GHToken})`);
başarılı squash-merge sonrası `git push <remote> <base>` gh-token credential-helper ile (redact'li).
Push-fail sinyali: `SquashMerge` geçerli merge SHA + `*PushError` döner (merge yerelde durur, geri alınmaz);
tick `errors.As` ile yakalar, task'ı **done** bırakır ve `push-failed` event'i (merge-phase
intervention-needed) + slog.Warn yayar (sessiz-kayıp yok, sahte-yeşil yok). Daemon wiring:
`-push`/`CONDUCTOR_PUSH` (default false) + `-push-remote`/`CONDUCTOR_PUSH_REMOTE` (default origin),
gh-token `CONDUCTOR_GH_TOKEN`/`GH_TOKEN`'dan (1.5-a ile paylaşımlı, asla loglanmaz). Default-OFF
yerel-merge davranışı byte-identik korunur; mevcut testler/e2e yeşil.
