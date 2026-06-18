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
- **2B-5 — Reconcile runner (BAĞIMSIZ recovery job)** — 2B-3 boşluğu: host-heartbeat OwnerLive + TTL helper VAR ama ürün tarafında reconcile.ReapLeases'i koşturan bir şey YOK → ölü-host lease'i pratikte reap olmuyor. ADR-0016 (bağımsız job) → `conductor -reconcile` modu (one-shot/loop) + k8s CronJob (P4-4 stallcheck deseni).

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
- **Dalga A — 2A-3 (mobil/iOS maestro reçetesi, ADR-0023)** ✅ done (reçete TANIMLI; canlı iOS-build Dalga-B'ye ertelendi).
  Görsel-verify ile AYNI deterministik gate desenini iOS'a taşır. **Scaffolder `StackIOS`** (maestro/Xcode-driven mobil),
  Go/Rust/Python'dan SONRA, web/Node'dan ÖNCE algılanır — markerlar (biri yeterli): `maestro/` flows dizini,
  `*.xcodeproj`/`*.xcworkspace` bundle (glob), veya `Project.swift`. Detection dir/glob marker desteği additive
  (`anyDirExists`+`anyGlobMatches`+rule.globs). **Profil gate'leri (sıralı):** build/test = `xcodebuild build-for-testing`/
  `xcodebuild test` (Mac gerekir → **Dalga-B-canlı**) + **maestro UI-flow gate** `maestro test maestro/flow.yaml` (exit-code'lu
  komut-gate; Mac simülatörde canlı, Dalga-B) + **AYNI deterministik visual gate** `imagediff .conductor/visual/{actual,reference}.png
  -threshold 0.02` (web ile BYTE-BYTE aynı argv — gate reuse). maestro+visual EN SON. **Routing hook:** `Profile.Capability="ios-build"`
  → `.conductor/config.yaml` top-level `requires: ios-build` serialize (ADR-0008; lane→Mac-host yönlendirmesi 2B-2). Additive
  `recipeGates.maestro` + `recipeDoc.Requires`; `LoadRecipe` altı slotu sıralı okur (build,test,vet,lint,maestro,visual) +
  `Recipe.Requires` döner. iOS readiness probe (`hasIOSTests`): maestro/ flow (*.yaml/*.yml) VEYA Xcode `*Tests` target → READY.
  **Offline-deterministik kanıt (Xcode/maestro YOK):** (a) detection (ios-repo: maestro/ + .xcodeproj) + reçete round-trip
  (`TestLoadRecipe_IOSRoundTrip`: GenerateDraft→LoadRecipe build/test/maestro/visual gate'leri + `requires:ios-build` round-trip);
  readiness (maestro flow→READY, bare→NOT-READY+ADR-0009); (b) **visual-gate reuse** (`internal/verify/ios_recipe_test.go`: iOS
  profilinin `Visual` argv'i web ile birebir; gerçek derlenmiş imagediff ile eşit ekran→PASS / farklı→FAIL); (c) **eksik binary→
  deterministik FAIL** (PATH boş → maestro/xcodebuild gate'leri `runGate` üzerinden FAIL, sessiz-skip YOK; eksik actual.png→imagediff
  exit 2→FAIL). **Dalga-B'ye AÇIKÇA ertelendi (Mac-host):** gerçek `xcodebuild test` derleme/koşma + maestro-on-simulator canlı
  (gerçek ekran-görüntüsü üretip visual gate'e besleyen render). engine.go + statestore frozen DOKUNULMADI (additive);
  tools/builder dokunulmadı. Gate (build+test+vet+golangci v2.12.0) + e2e YEŞİL; gofmt temiz; yeni Go dep yok; secret yok.

- **Dalga B — 2B-1 (host registry + capabilities, ADR-0024 agent-per-host)** ✅ done. Agent-per-host'un EKSİK parçası =
  HOST registry (lease zaten host-üstü N-4; Task'ta Lane+Requires zaten var ADR-0008). **Additive StateStore (ADR-0021,
  mevcut imzalar değişMEDİ):** yeni `Host{ID, Capabilities []string, LastHeartbeat time.Time}` tipi + 4 metot:
  `RegisterHost(ctx, Host)` (id'ye göre UPSERT — insert veya capabilities+heartbeat overwrite; zero LastHeartbeat→live damgası),
  `HostHeartbeat(ctx, hostID, t)` (sadece LastHeartbeat ilerletir; bilinmeyen host→ErrNotFound), `GetHost(ctx, id)`
  (ErrNotFound), `ListHosts(ctx)` (id sıralı). memory.go (hosts map + cloneHost, deep-copy) + postgres.go (`INSERT ... ON
  CONFLICT (id) DO UPDATE`; capabilities jsonb, tasks.requires deseni; last_heartbeat UTC). **Migration 00007_hosts.sql**
  (goose Up/Down: `hosts(id text pk, capabilities jsonb, last_heartbeat timestamptz)`). **Daemon self-register + host-heartbeat:**
  yeni `-capabilities`/`CONDUCTOR_CAPABILITIES` (virgülle ayrık; trim+dedupe+sort → deterministik); newDaemon başlangıçta
  `RegisterHost` (host id mevcut `-host`/hostname'den; her iki backend'de — paylaşımlı PG'de cross-host registry, in-memory
  tek-proseste kendi store'una, zararsız); her tick `HostHeartbeat` (ADR-0016 DOSYA heartbeat'ten AYRI PG satırı; best-effort,
  tick'i bozmaz). **Geriye-uyum:** capabilities yoksa boş set → host yine kaydolur, sadece no-requires lane'lere uyar
  (routing 2B-2). **Stub-store güncellendi** (statestore_test stubStore additive 4 metot). **Kanıt:** conformance (memory + PG)
  4 yeni host case'i (upsert insert→update, GetHost missing→ErrNotFound, ListHosts sıra, heartbeat LastHeartbeat ilerletir +
  bilinmeyen→ErrNotFound); daemon testleri (-capabilities parse normalize + startup RegisterHost + tick heartbeat ilerletir +
  no-capabilities yolu). GERÇEK docker-PG (port 55464) conformance koşuldu YEŞİL (19/19, 4 host dahil) + teardown. engine.go +
  tools/builder DOKUNULMADI; mevcut imza değişMEDİ (additive). Offline gate (build+test+vet+golangci v2.12.0) + e2e + -race YEŞİL;
  gofmt temiz; yeni dep yok; secret yok.
- **Dalga B — 2B-2 (capability routing, ADR-0024 agent-per-host + ADR-0008)** ✅ done. Artık her agent (daemon)
  SADECE host'unun karşıladığı capability'leri isteyen task'ları çeker (pull-based): bir task bu host için ancak
  `Task.Requires ⊆ host.Capabilities` ise pickable; aksi halde SKIP edilir (capable host'a bırakılır). **Additive registry
  opsiyonu (Picker imzası DEĞİŞMEDİ):** `registry.Option` + `registry.WithCapabilities([]string)`; `NewRegistry(store,
  opts ...Option)` variadic — opsiyonsuz çağrı pre-2B-2 davranış (mevcut çağıranlar dokunulmadı). Caps trim'lenir, boş entry
  düşer, set'e normalize edilir. **PickReady'de capability-gate:** status (todo/ready) sonrası, depsDone öncesi
  `capabilitiesSatisfy(t.Requires)` filtresi; başarısızsa `continue` (sıralama korunur, hata değil — bir sonraki ready task
  değerlendirilir). **Filtre semantiği (net):** caps NON-EMPTY → `Requires ⊆ caps` ise pick; caps NİL/BOŞ → **UNCONSTRAINED**
  = bugünkü davranış (Requires'a bakmaksızın pick; capability hiç bildirmemiş tek-host setup'ları korunur — boş Requires
  zaten her host'ta pickable). "empty=unconstrained" bilinçli seçim (daha katı "empty=hiçbir şey eşleşmez" geleceğe opt-in).
  Boş Requires'lı task HER host'ta pickable (boş kümenin altkümesi her zaman sağlanır). **Daemon wiring:** `registry.NewRegistry(
  store, registry.WithCapabilities(cfg.capabilities))` — `-capabilities` (2B-1) registry'ye geçer; boş caps → unconstrained
  (e2e'ler caps'siz registry kurar → değişmedi). **Kanıt:** registry routing-matrix testi (ios-build task: capable
  [ios-build,macos]→pick / incapable [linux]→skip ErrNotFound / unconstrained→pick; boş-Requires→3 host'ta da pick;
  linux çok-task'lı projede ios task'ı skip ama no-requires task'ı pick (A-2, sıralama korunur); çok-requires partial-cap→skip;
  blank caps→unconstrained). Mevcut PickReady testleri (caps'siz) DEĞİŞMEDEN yeşil (geriye-uyum). engine.go + statestore imza +
  tools/builder DOKUNULMADI; Picker imzası değişMEDİ (routing registry config'inde); additive. Offline gate
  (build+test+vet+golangci v2.12.0) + e2e (E2E ./internal/conductor/) + `-race ./internal/registry/ ./internal/conductor/`
  YEŞİL; gofmt temiz; yeni dep yok; secret yok.

- **Dalga B — 2B-3 (çok-host kanıtı agent-as-daemon + cross-host stale-lease reaping, ADR-0024)** ✅ done. İki konu:
  (a) **Cross-host stale-lease reaping (host-heartbeat tabanlı):** PID-liveness makineler arası ANLAMSIZ; ölü bir host'un
  lease'i HOST-HEARTBEAT bayatlığıyla serbest bırakılır. Yeni ADDITIVE yardımcı `reconcile.HostHeartbeatOwnerLive(ctx, store,
  hostStale, now)` → `Config.OwnerLive` predicate'i kurar (B-3 seam'i; reconcile.Config.OwnerLive imzası DEĞİŞMEDİ).
  **Semantik (deterministik, `now` enjekte):** owner host heartbeat'i taze (`now - LastHeartbeat <= hostStale`) → LIVE
  (lease KORUNUR); bayat (`> hostStale`) → DEAD → reapable; host registry satırı YOK (GetHost ErrNotFound) → konservatif
  DEAD/unknown → reapable (kanıtlayamaz, repo'yu sonsuza pinlememeli); ErrNotFound dışı store hatası → LIVE (geçici hata
  spurious reap yapmamalı, TTL backstop yine bağlar). **TTL backstop korunur** (Config.LeaseTTL bağımsız ikinci seam).
  **TAZE host'un lease'i YANLIŞ reap EDİLMEZ** (testle kanıtlandı: TTL=0 iken bile 2h-eski ama taze-heartbeat'li host
  KORUNUR). Wiring: `reconcile.New(store, git, Config{LeaseTTL:…, OwnerLive: HostHeartbeatOwnerLive(…)})` (reconcile ZATEN
  ayrı job olarak tasarlandı — ADR-0016; daemon-içi yeni background job EKLENMEDİ, frozen-engine riski sıfır).
  (b) **İki-agent koordinasyon kanıtı (deterministik, gerçek-ağ YOK):** `internal/conductor/twohost_test.go` — TEK paylaşılan
  in-memory StateStore üstünde İKİ Registry (her biri `WithCapabilities` = o host'un caps'i, daemon Picker kurulumuyla aynı):
  host-linux `[linux,backend]`, host-mac `[ios-build,macos]`; tek proje, iki task (T-ios `Requires:[ios-build]`, T-generic
  no-requires). Assert: **(1) host-üstü single-winner lease (N-4):** iki agent AYNI repoyu eşzamanlı lease'ler →
  TAM 1 kazanır, diğeri ErrLeaseHeld (çift-yazım yok, tek lease satırı; N-4 concurrent testinin agent-seviyesi aynası).
  **(2) capability routing (2B-2):** T-ios SADECE host-mac'te pickable; host-linux skip eder (T-generic'i alır; T-generic
  done olunca host-linux NOTHING/ErrNotFound, host-mac T-ios). **(3) cross-host reap (2B-3):** host-mac ölü (bayat
  heartbeat) lease tutarken → host-heartbeat OwnerLive lease'i serbest bırakır; TAZE host'un (host-linux başka repoda)
  lease'i KORUNUR; reap sonrası capable host repoyu yeniden lease'ler. **reconcile unit testleri:** stale-host reaped,
  unknown-host reaped (konservatif), fresh-host kept (TTL=0). engine.go + statestore imza + tools/builder DOKUNULMADI;
  yalnız ADDITIVE (yeni exported `HostHeartbeatOwnerLive` + iki yeni test dosyası). Offline gate (build+test+vet+golangci
  v2.12.0) + e2e (E2E ./internal/conductor/) + `-race ./internal/reconcile/ ./internal/conductor/ ./internal/registry/`
  YEŞİL; gofmt temiz; yeni dep yok; secret yok.
- **Dalga B — 2B-4 (host-başına kaynak cap + governor çok-host, ADR-0024 + ADR-0008)** ✅ done. Governor'a PER-HOST
  eşzamanlı-task cap'i eklendi: bir host KENDİ kapasitesini aşmasın (Mac≠Linux). Global cap (tüm host'larda toplam
  eşzamanlılık) KORUNUR; repo-per-1 + host-yük tavanı KORUNUR. **Cap mantığı:** `governor.Config`'a `HostCap int` +
  `HostID string` (hangi host'um) eklendi; `Admit` zaten global cap için yaptığı TEK `ListLeases` okumasından bu-host'un
  aktif lease'lerini (`l.HostID == g.cfg.HostID`) sayar (ekstra store round-trip YOK), `thisHostActive >= HostCap` ise
  yeni `ReasonDenyHostCap = "deny:host-cap"` ile reddeder. **Kontrol sırası (deterministik, dokümante + testli):**
  repo-busy → global-cap → host-cap → host-load (daha spesifik/yapısal limit, gürültülü çevresel olana kazanır).
  **Geriye-uyum:** `HostCap <= 0` → per-host cap KAPALI (sadece global+repo+load, bugünkü davranış); New HostCap'i
  defaultlamaz (non-positive = açık "sınırsız"). Mevcut governor/conductor/e2e testleri DEĞİŞMEDİ. **Daemon wiring:**
  `-host-cap`/`CONDUCTOR_HOST_CAP` (int, default 0 = sınırsız) + daemon'ın host id'si governor `HostID`'ye geçirilir;
  cap set ise loglanır. **Testler (governor_test, deterministik):** bu-host cap'te (host-A 2 aktif, HostCap=2) →
  DenyHostCap; cap altında → admit; BAŞKA host'un lease'leri host-A cap'ine SAYILMAZ (host-B dolu olsa bile host-A
  serbest); mixed-host yalnız bu-host sayılır; HostCap=0/negatif → host-cap'te asla reddetmez; precedence (repo-busy
  host-cap'i, global-cap host-cap'i, host-cap host-load'u yener — iki limiti birden tetikleyen vakalarla assert).
  engine.go + statestore imza + tools/builder DOKUNULMADI; yalnız ADDITIVE (yeni Reason + iki yeni Config alanı +
  Decision alanları + yeni test). Offline gate (build+test+vet+golangci v2.12.0) + e2e (E2E ./internal/conductor/) +
  `-race ./internal/governor/ ./internal/conductor/` YEŞİL; gofmt temiz; yeni dep yok; secret yok.
