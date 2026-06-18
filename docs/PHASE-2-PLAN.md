# Faz-2 Planı — çok-host + çoklu reçete + sentinel L2 (compact-proof çıpa)

> Faz-1 (1a çekirdek + 1b + üretim sertleştirme + dogfood) bitti. Bu plan Faz-2'yi tanımlar.
> Kullanıcı kararları (2026-06-18): çok-host GERÇEK ihtiyaç (Mac+Linux); proje tipleri = Go/backend +
> web-görsel + iOS-maestro + Node/Python (hepsi); sentinel Katman-2 DAHİL; Faz-1 açıkları Faz-2'den ÖNCE.
> Mekanizma Faz-1 ile aynı: her iş için spec → Agent kodlar → orchestrator BAĞIMSIZ gate+gerçek-koşu doğrular
> (Rule#9) → squash-merge develop → push. Frozen kontratlara additive (ADR-0021); engine.go imzaları frozen.

## Hedef
Tek makinede kanıtlı platformu (a) **birden çok host'a** (capability-routing: iOS→Mac), (b) **birden çok proje
tipine** (Go/web/iOS/Node/Python reçeteleri, xirigo-tarzı görsel/maestro 1:1 dahil), (c) **gri-bölge
canlılık-zekâsına** (sentinel L2) genişletmek — determinizm ve "sahte-yeşil asla" garantisini bozmadan.

---

## ÖNKOŞUL — Faz-1.5 (küçük açıkları temizle, Faz-2'den önce)
- **1.5-a — Holdout şemaları tamamla:** `internal/holdout`'a `pg://` (Postgres-backed holdout store) + `private:`
  (ayrı git repo) Fetch impl'leri (şu an yalnız `store://` filesystem). Kabul: 3 şema da fetch+inject; deterministik
  testler; gate yeşil.
- **1.5-b — T3/T4 human-hold CANLI demo:** gerçek daemon + PG ile T3 görev → gate-pass ama governance HUMAN-hold →
  merge YOK + intervention-needed event; conductorctl ile "approve→merge" akışı (gerekirse küçük additive `approve`
  komutu). Kabul: canlı tick'te T3 held + onayla→merge, bağımsız doğrula.
- **1.5-c — GitMerger remote-push modeli:** başarılı squash-merge sonrası opsiyonel `git push origin <base>`
  (flag/config ile; gh-token auth; default kapalı=yerel). Kabul: gerçek throwaway remote'a push doğrulanır; auth
  yoksa temiz hata; push opsiyonel (yerel-merge davranışı korunur). **Yeni karar gerekebilir → ADR-0022 (deploy/push modeli).**

---

## FAZ-2 — dalgalar (bağımlılık sırasıyla)

### Dalga A — Çoklu reçete (tek-host; hızlı değer, çok-host'tan bağımsız başlar)
Reçete = CommandEngine config (develop-cmd + verify gate'leri + verify-tipi). 5-fiil arayüz frozen; "yeni tip=yeni reçete".
- **2A-1 — Reçete altyapısını tamamla:** `.conductor/config.yaml` reçetesini uçtan-uca tam pipe (develop-cmd +
  gate argv + verify-tipi); scaffolder profillerini (N-8) gerçek çalışan reçetelere bağla. **Node + Python reçeteleri**
  (build/test/lint argv — çoğu hazır). Kabul: throwaway Node + Python repo'da onboard→gerçek gate→merge, canlı.
- **2A-2 — Web görsel-diff reçetesi:** xirigo deseni — claude-design çıktısı ↔ **deterministik görsel-diff** (piksel/
  threshold exit-code) + Playwright/headless e2e. Yeni verify-tipi: görsel-kanıt Check (deterministik; ADR-0003/0018
  uyumlu, öznel-skor YOK). **Yeni karar → ADR-0023 (görsel-verify reçetesi).** Kabul: bir web görevinde görsel-diff
  fail→blocked / pass→merge, canlı.
- **2A-3 — Mobil/iOS maestro reçetesi:** maestro UI-flow + görsel-diff reçetesi. iOS-build Mac gerektirir →
  **tam çalışması Dalga B'ye (çok-host) bağımlı**; reçete + verify Dalga A'da tanımlanır, Mac-host'ta Dalga B'de koşar.
  Kabul: maestro reçetesi + deterministik UI-doğrulama tanımlı; Mac-host hazır olunca canlı.

### Dalga B — Çok-host (remote executor) — en büyük ayak
Zemin hazır: Postgres lease host-üstü "repo başına 1" atomik (N-4) + governor host-yük tavanı. ADR-0008 "merkezi registry (B)".
- **2B-0 — KARAR: remote-executor modeli → ADR-0022/ayrı ADR:** ssh-ile-uzak-komut mu, yoksa uzak host'ta
  **conductor-agent** mı (her host'ta hafif agent, merkezi PG'den lease alır)? Güvenlik/auth/workspace modeli. ÖNCE bu.
- **2B-1 — Host registry + capabilities:** `hosts` + `lanes` tabloları (host kimlik + `capabilities`; lane→`requires`);
  host kayıt + heartbeat. Kabul: çok-host registry; conformance testleri.
- **2B-2 — Capability routing:** picker `requires ⊆ host.capabilities` ile task'ı uygun host'a yönlendirir
  (lane-seviyesi, ADR-0008). Kabul: iOS-lane yalnız ios-build yetenekli host'a düşer; deterministik test.
- **2B-3 — Remote executor:** seçilen modele göre (2B-0) tick develop'ını uzak host'ta koştur (workspace/provisioner
  uzak); merkezi PG state + event. Kabul: iki-host kurulumda (yerel simüle: 2 agent) bir görev uzak host'ta develop→
  verify→merge; "repo başına 1" host-üstü kanıtlanır (iki host aynı repoya yazamaz).
- **2B-4 — Host-başına kaynak cap + governor çok-host:** global cap + host-başına cap + host-yük; Mac≠Linux kapasite.

### Dalga C — Sentinel Katman-2 (gri-bölge LLM-danışman)
- **2C-1 — Gri-bölge dedektörü + LLM-danışman:** deterministik taban "emin değilim" (process canlı AMA çıktı durdu)
  → `claude -p` danışman dar çıktı `{ilerliyor|sıkışmış|insan-gerek}` + 1 cümle → aksiyon; **Katman-3 backstop yine
  son söz** (LLM "bekle" dese de mutlak tavan bağlar). reconcile/heartbeat'e gömülü, event yayar. Kabul: gri-bölge
  senaryosunda L2 doğru sınıflar; backstop her zaman kazanır (deterministik test + stub danışman); LLM gate-dışı.

---

## Sıra / bağımlılık
1. **Faz-1.5** (a,b,c) — paralel/hızlı, Faz-2 öncesi.
2. **Dalga A** (2A-1 → 2A-2 → 2A-3) — çok-host'tan bağımsız, erken değer.
3. **Dalga B** (2B-0 KARAR → 2B-1 → 2B-2 → 2B-3 → 2B-4) — en büyük; 2A-3'ün iOS canlısı buna bağlı.
4. **Dalga C** (2C-1) — bağımsız; herhangi bir noktada paralel ele alınabilir.

## Yeni ADR ihtiyaçları
- **ADR-0022** — remote-executor modeli (ssh vs agent) + (1.5-c) remote-push/deploy modeli.
- **ADR-0023** — görsel-verify reçetesi (deterministik görsel-diff; claude-design ↔ maestro/Playwright 1:1).

## Değişmez ilkeler (Faz-1'den taşınan)
Deterministik kanıt = kalite kapısı (LLM asla karar mercii); sahte-yeşil ASLA; orchestrator BAĞIMSIZ doğrular
(Rule#9); frozen kontratlara additive; secret-leak yok; her merge gate'li; canlı optiway/xirigo'ya dokunma.

## İLERLEME KAYDI (her iş bitince güncelle)
- Faz-2 planı oluşturuldu (2026-06-18).
- **1.5-a holdout şemaları** ✅ done (b791d62, push'lu). internal/holdout: Router (şema-dispatch) + PGStore (`pg://holdouts/<id>`, table `holdouts(id,path,content)`, migration 00005) + PrivateRepoStore (`private:<repo>#<path>`, gh-token, repo-dışı klon). Daemon: -dsn→pg auto, -holdout-private-cache+CONDUCTOR_GH_TOKEN→private; backward-compat (fs-only çalışır); unconfigured-scheme→açık hata. **Bağımsız doğrulandı:** kendi docker PG'imde 5 PGStore testi (-race), private local-repo 7 testi, Router dispatch, gate+e2e+race yeşil, frozen untouched, secret-leak yok (token redact).
- **1.5-b T3/T4 human-hold approve akışı + CANLI demo** ✅ done. Held semantiği rafine: held görev artık
  `awaiting-approval` (blocked DEĞİL) statüsünde park edilir + doğrulanmış per-task BRANCH `Task.Branch`'e kaydedilir;
  branch ref klonda korunur (Cleanup yalnız worktree siler). Additive: `Task.Approved bool` (migration 00006) +
  `conductor.StatusAwaitingApproval`. `conductorctl approve --project <id> [--task <id>]` → StoreApprover (paylaşılan
  store; -dsn ile cross-process). Tick, PickReady'den ÖNCE approved+held görevi çözer: korunmuş branch'i RE-ATTACH
  eder (provisioner.WorkspaceForBranch — base'den yeni kesmez), GÜVENLİK için ucuz **re-verify** koşar (base drift'i
  yakalar; sahte-yeşil yok), sonra squash-merge eder — **develop ASLA yeniden koşmaz**. Semantik kararı:
  **re-verify-then-merge** (merge-directly yerine; base drift'e karşı Rule#9 koruması, LLM yeniden atılmaz).
  Deterministik test: hold→approve→merge, develop tam 1 kez koştu (fake + gerçek-git e2e). **Canlı demo (PG :55463,
  throwaway repo):** tick1=held (merge yok, branch korundu), `approve`, tick2=approved-merged (mergeSHA 3674435,
  base'de `[task:T3-held]` trailer, develop hit-count=1). engine.go/builder dokunulmadı; gate+e2e+race yeşil;
  golangci v2.12.0 temiz.
- **1.5-c GitMerger opt-in remote-push** ✅ done (ADR-0022). Additive: `GitMerger.WithPush(PushConfig{Enabled,Remote,GHToken})`
  + `*PushError{SHA,Remote,Err}` sinyal tipi. Başarılı squash-merge+commit SONRASI (restoreBase yalnız FAILURE'da
  koşar, etkileşim yok) push-enabled ise `git push <remote> <base>` gh-token credential-helper ile (ADR-0017 deseni,
  token argv/env'e değil yalnız helper'a; hata redact'li). **Sinyal şekli:** `SquashMerge` geçerli merge SHA döner;
  push fail'de `(sha, *PushError)` — non-nil hata + geçerli SHA = "merged-locally-but-push-failed". Tick `errors.As` ile
  yakalar (`pushFailure`), task'ı **done** bırakır (`OutcomeMerged`+SHA), `markMergedPushFailed` ile slog.Warn +
  `push-failed` event (merge-phase intervention-needed, payload: reason/merge_sha/remote/error) yayar — undo YOK,
  block YOK, sessiz-kayıp YOK (normal + approve-merge yolları aynı). Daemon: `-push`/`CONDUCTOR_PUSH` (default false) +
  `-push-remote`/`CONDUCTOR_PUSH_REMOTE` (default origin) + gh-token `CONDUCTOR_GH_TOKEN`/`GH_TOKEN`'dan (1.5-a paylaşımlı);
  başlangıçta push on/off+remote loglanır, token ASLA. **Gerçek-git testleri (bare origin + clone):** push ON →
  bare origin base merge SHA'ya ilerledi (+`[task:T-1]` trailer); push-fail (bogus remote) → merge SHA döndü, yerel base
  merge+trailer'ı korudu (undo/loss yok), `*PushError` yüzeylendi; default OFF → origin'e dokunulmadı; token-redact
  unit (helper doğrudan); tick-level push-failed → task done + event. engine.go + statestore frozen dokunulmadı;
  builder dokunulmadı; gate (build+test+vet+golangci v2.12.0) + e2e + race yeşil; gofmt temiz; token leak yok.
- **🏁 FAZ-1.5 TAMAM (1.5-a/b/c) — hepsi bağımsız doğrulandı, push'lu.** ADR-0022 ✅.
- **Dalga A — 2A-1 (reçete altyapısı + Node/Python)** ✅ done. Per-project reçete tam pipe: scaffolder additive
  `LoadRecipe(dir) (Recipe{Develop, Gates}, error)` — `.conductor/config.yaml`'ı recipeDoc'tan develop argv + sıralı
  gate argv (build/test/vet/lint, boşlar atlanır) okur; dosya yok → `ErrNoRecipe` sentinel (caller fallback); corrupt /
  gate-siz → HARD error (sahte-yeşil yok). `LoadRecipeGates` artık `LoadRecipe`'a delege eder (false,nil contract korunur).
  Daemon: `resolveRecipe(cfg)` HEM develop-cmd HEM gate'leri seçer — `-recipe-dir`+`.conductor/config.yaml` varsa
  ikisi de dosyadan (per-project performer), yoksa `-develop-cmd` flag + default gate'ler (build+test+vet). Recipe'in
  develop'ı boş VEYA inert placeholder (`echo configure-develop-command`) ise flag'e düşer (onaylanmamış draft no-op
  performer başlatamaz). Aktif kaynak loglanır (file vs flag). **Backward-compat:** `.conductor` yoksa = bugünkü
  davranış (engine `DevelopCmd` flag'den, default gate'ler). Engine.go + statestore frozen DOKUNULMADI (additive);
  tools/builder dokunulmadı. **Canlı kanıt (throwaway /tmp repo, docker PG :55671):** Node (package.json `npm test`→
  `node --test`) + Python (`pytest`, stdlib) için scaffolder draft'ı node/python profilini doğru üretti; onboard→
  deterministik develop-wrapper (kodu düzeltir+verdict JSON yayar, gerçek claude yok)→ projenin KENDİ gate'i koştu→
  yeşil→squash-merge: Node mergeSHA `c18e8f9` + `[task:node-pos]`, Python mergeSHA `3e5cdd3` + `[task:py-pos]`.
  NEGATİF (hâlâ-bozuk fix, verdict yalan-`pass`): bağımsız gate `review=changes-requested`→retry→**blocked**, merge YOK,
  develop'ta 1 commit + trailer yok (Node `node-neg`, Python `py-neg`). Gate+e2e+golangci v2.12.0+race YEŞİL; gofmt temiz;
  yeni Go dep yok; secret yok.
- **Dalga A — 2A-2 (web görsel-diff reçetesi, ADR-0023)** ✅ done. Görsel-verify = deterministik reçete GATE'i (yeni
  verify-tipi DEĞİL): render→diff→exit-code. **Deterministik çekirdek `cmd/imagediff`** (stdlib `image/png`, browser'sız,
  yeni Go dep yok): `imagediff <actual.png> <expected.png> -threshold <frac>` → exit 0 (oran<=threshold PASS) / 1 (FAIL,
  non-revealing özet: yalnız oran/threshold, hangi piksel ASLA) / 2 (eksik/oversize/boyut-uyuşmazlığı/decode = net hata
  = det. FAIL, sessiz-skip YOK). Karar saf fonksiyon (RGBA tam eşitlik; AA-toleransı=threshold); 64MP tavan. Unit:
  identical→pass, beyond→fail, within→pass, sınır==threshold→pass (inclusive), boyut-uyuşmazlığı→err, eksik→err, non-PNG→err,
  run() exit-kod kontratı + committed tiny testdata (reference/match/mismatch PNG). **Scaffolder `StackWeb`** (Node +
  `playwright.config.*`, Node'dan ÖNCE algılanır): standart Node gate'leri + 5./son **visual gate** (additive `Profile.Visual` +
  `recipeGates.visual`); `recipe.verify.visual` serialize; `LoadRecipe` beş gate'i sıralı okur. Yol konvansiyonu
  `.conductor/visual/{actual,reference}.png`, threshold 0.02. **Referans = repo-DIŞI holdout (ADR-0018 yeniden-kullanım):**
  `store://references/<id>/spec.yaml` → inject/`.conductor/visual/reference.png` verify-worktree'ye enjekte; visual gate =
  holdout-cmd `imagediff <actual> <reference>` ayrı verify-worktree'de (actual performer HEAD'inden, reference enjekte) →
  performer referansı görmez/ezberleyemez. **Deterministik e2e (browser'sız, `visual_e2e_test.go`):** web görevi gerçek
  pipeline'da (provisioner+CommandEngine+gerçek FSStore holdout+GitMerger), gerçek derlenmiş `imagediff` PATH'te — actual
  referansa eşit→visual gate exit 0→`merged` (mergeSHA, develop'ta `[task:T-visual]` trailer); actual farklı→exit non-0→
  `changes-requested`→retry-tüketildi→`blocked`, merge YOK, trailer YOK (sahte-yeşil yok). **Playwright render canlı
  doğrulandı:** `npx playwright install chromium` başardı, `file://` HTML `#box` screenshot'landı, aynı-sayfa re-render vs
  referans→PASS exit 0 / farklı sayfa→FAIL exit 1 (gerçek render→diff→exit). engine.go + statestore frozen DOKUNULMADI
  (additive); tools/builder dokunulmadı. Gate (build+test+vet+golangci v2.12.0) + e2e + race YEŞİL; gofmt temiz; yeni Go dep yok; secret yok. ADR-0023 ✅.
