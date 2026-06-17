# Conductor Platform — Design Brief

> **Bu doküman, JENERİK çok-projeli Kontaktör platformunu TASARLAMAK + İNŞA ETMEK için açılacak
> yeni oturumun "İLK OKU"sudur.** Bu oturum kakbet hook'larından uzak, temiz bir folder'da
> (`~/Documents/GitHub/conductor-platform`) çalışır. Mevcut optiway Kontaktörü AYRI ve canlı
> çalışmaya devam ediyor — ona DOKUNMA (sadece referans olarak incele).
>
> Disiplin: **hızlı karar yok.** Her tasarım kararını §5'teki tuzaklara karşı tart, kararı yaz, sonra kodla.

---

## 0. Amaç / kapsam
Tek projeye (optiway/Dark Factory) gömülü olmayan, **herhangi bir projeye Kontaktör atayabilen
jenerik bir platform** kurmak. Sonunda Devin.ai / Augment-vari **VS Code üzerinde izlenebilir** bir
katman. Çekirdek ("ana yapı") önce; izleme UI'si en son (hatta Kontaktör'e geliştirtilebilir).

Bu oturumda: **tasarım + çekirdek inşa.** Mevcut optiway FX/IC işleri ayrı (kakbet oturumunda) sürüyor.

---

## 1. Vizyon (hedef son durum)
- **Jenerik, çok-projeli, otonom kodlama Kontaktör platformu.**
- **Her proje × her Kontaktör → kendi izole çalışma alanı** (klon/dizin + config + kuyruk); orada işi yapar.
- **İzlenebilirlik:** Devin/Augment-vari, VS Code üstünde canlı izleme + müdahale (pause/approve/intervene).
- Çekirdek yapı ÖNCE; izleme UI'si SONRA (Kontaktör-buildable).

---

## 2. Mevcut KANITLI varlıklar (sıfırdan icat etme — BUNLARIN ÜSTÜNE kur)
1. **Dark Factory motoru** (plugin): `~/.claude/plugins/cache/everva/dark-factory/1.3.3/`
   - Akış: select-task → architect → tester → developer → reviewer → holdout(3×, quorum 2/3, ≥90) → governance(tier) → ship.
   - Config-okur: proje başına `.dark-factory/{config.yaml, backlog.md, impl-prompt.md, holdouts/}`. optiway+xirigo'da kanıtlı.
   - **Bu oturumda eklenen patch'ler (davinci-local plugin kopyasında):**
     - `scripts/run-task.sh`: **holdout retry-on-empty** (`run_holdout_once`, 0-byte JSON → retry; sahte-green üretmez) + **layer'ı backlog `Layer` kolonundan oku** (issue yoksa).
     - `lib/ship.sh`: **`ci_gated_merge`** (tüm check pass + 0 pending olana dek poll, sonra `gh pr merge --squash --delete-branch`; pending/fail'de ASLA merge — yapısal). `ship_auto`+`ship_review` bundan geçer; `DF_SELF_MERGE_ALL_TIERS=true`.
   - ⚠️ Bu patch'ler **plugin kopyasında, version-controlled DEĞİL** → platform bunları upstream'e (everva/dark-factory) taşımalı veya kendi engine-fork'unda tutmalı.
2. **conductor-sentinel**: repo `everva/conductor-sentinel` (+ `~/Documents/GitHub/conductor-sentinel`, davinci `~/conductor-sentinel`).
   - Canlılık nöbetçisi: S1-S6 sinyaller, FSM (HEALTHY/BUSY/NEAR_DONE/STALE/COMPLETED), near-done-protect (asla-kill), Faz-1 LLM judge (recommend-only), Faz-2/3 remediation (DRY-RUN, gated, live yapısal-erişilemez).
   - **`factory_guarded.sh`**: ralph + sentinel co-launch (event stream `sentinel-events.jsonl`).
   - **`chain_runner.sh`** (bu oturumda eklendi): Model-2 unattended loop — her slot `git checkout -B <branch> origin/<base>` (freshness) → pending-task var mı → factory_guarded; PATH export; MAX_TASKS scope guard.
   - **Sentinel event stream = izlenebilirliğin İLK veri kaynağı.**
3. **optiway workspace tarifi (KANITLI per-project kurulum)** — davinci `~/optiway-conductor`:
   - Dedicated klon (kullanıcı çalışma klonundan izole) + branch `conductor/optiway`.
   - **Auth:** origin HTTPS + `gh auth setup-git` (gh-token writable). ⚠️ Deploy-key read-only çıktı — KULLANMA.
   - `.dark-factory/` develop'ta (config 80/80, impl-prompt FAZ 10.9 backlog-completion + FAZ 11b refs-sync-self-resolve, başla-exemption `DF_SESSION_ID`).
   - **Kanıtlandı:** IC1+IC2 otonom implement+self-merge (#921/#922); IC3 impl temiz (hang-fix sonrası) + holdout-retry canlı.
4. **Governance/güvenlik öğrenimleri (platform politikasına dönüştür):**
   - **CI-gate:** free-plan'de branch-protection enforce edilemez → merge'i explicit poll ile gate'le (yeşil değilse merge yok).
   - **holdout-retry:** yüklü makinede `claude -p` boş döner → retry (false-block önler).
   - **human-merge zorunlu:** auth/credential, destructive (DROP TABLE), migration, RBAC. (Risk-skorlama dosya-yoluna BAKMIYOR — bug; fix yerine 80/80 config + human-merge marker kullandık.)
   - **Yük yönetimi:** çok eşzamanlı CI/claude -p → load patlaması (load 41 yaşadık; remote-control daemon swap'i tüketip impl'i hang'letti). Platform host kaynağını throttle ETMELİ.
   - **PATH gotcha:** non-login shell'de `claude` (`~/.local/bin`) + `pnpm` (`~/.local/share/pnpm/bin`) PATH'te değil → unattended wrapper export etmeli.

---

## 3. Hedef mimari — 4 çekirdek bileşen
```
        ┌─────────────────────────────────────────────┐
        │   Observability UI (VS Code, Devin-vari)     │  ← Faz 3 (en son; Kontaktör-buildable)
        │   event stream tüketir + control API         │
        └───────────────▲───────────────┬─────────────┘
                         │ events        │ control(pause/approve)
        ┌────────────────┴───────────────▼─────────────┐
        │   PLATFORM ÇEKİRDEĞİ ("ana yapı" — Faz 1)     │
        │   • Workspace Provisioner                     │
        │   • Conductor Registry + Config modeli        │
        │   • Observability Event Stream (normalize)    │
        │   • Merge Governance policy engine            │
        │   • Concurrency/Resource governor             │
        └───────────────┬───────────────────────────────┘
                         │ Engine Adapter (Faz 2)
        ┌────────────────▼──────────────┬───────────────┐
        │  DF-ralph engine (kanıtlı)     │  future engine │
        └────────────────────────────────┴───────────────┘
                         │ per-project workspace
        ┌────────────────▼──────────────────────────────┐
        │  ~/optiway-conductor   ~/xirigo-conductor  ... │
        └────────────────────────────────────────────────┘
```
**Bileşenler:**
1. **Workspace Provisioner** — `<repo, base-branch, host>` → izole klon + gh-token auth + `.dark-factory/` scaffold + chain_runner. (optiway'de ELLE yaptığımız adımların otomatiği.)
2. **Conductor Registry + Config modeli** — projeler/Kontaktörler/durum tek kaynak. Motor proje bilmez; registry bilir.
3. **Engine Adapter** — ince/kararlı arayüz; platform DF-ralph'i sürer, ileride başka tip de.
4. **Observability Event Stream** — sentinel-events + run.log + PR/merge → tek normalize şema (UI'siz CLI'dan da izlenebilir; sonra UI tüketir).

---

## 4. İnşa sırası
- **Faz 1 (ana yapı):** Provisioner + Registry + Event-şeması + Merge-governance + Resource-governor. Tek bir mevcut projeyle (optiway) uçtan uca doğrula.
- **Faz 2:** Engine Adapter — 2. proje (xirigo as-is; sonra farklı-tip) onboard. Adapter arayüzü Faz-1'de tasarlanır, Faz-2'de ikinci implementasyonla doğrulanır.
- **Faz 3:** Observability UI (VS Code, Devin-vari) — event-stream + control-API stabilse Kontaktör'e geliştirtilebilir.

---

## 5. DİKKATLİ TASARIM AJANDASI (her birini tart, kararı YAZ — kullanıcının asıl isteği)
1. **Engine Adapter arayüzü** — Minimum fiiller: `discover_tasks()→[task]`, `run_task(task,ws)→{status,branch,pr,artifacts}`, `health(session)→state`, `events()→stream`, `control(pause/resume/abort)`. **TUZAK:** DF özelinin (ralph.sh, `.dark-factory` yolları) platforma sızması → 2. motor eklenemez. Arayüzü provisioner'dan ÖNCE dondur.
2. **Workspace modeli** — full-clone vs worktree vs shared-object (disk↔hız); auth (gh-token-helper KANITLI ✓ / deploy-key read-only ✗); branch modeli (tek-conductor-branch-reset vs **feature-branch-per-task** [doğal oluştu + eşzamanlı PR'da daha temiz] — KARAR ver, feature-branch'e meyilli); freshness (her task base'den reset). **TUZAK:** read-only-key + tek-branch-squash-divergence (yaşadık) — fix'leri göm.
3. **Project onboarding / scaffolder** — JENERİK'in zor kısmı: config'siz repo → üret. Stack-algıla (package.json/go.mod) → impl-prompt (build/test/lint komutları) + gate'ler + layer-map + governance defaults. **TUZAK:** hiçbir şeye uymayan aşırı-jenerik şablon VEYA proje-başına elle. KARAR: per-stack profil kütüphanesi + ilk scaffold'da insan-review.
4. **Config/Registry şeması** — tek kaynak (platform repo `conductors/` dizini veya DB). project{repo,base,host,engine,governance-policy,gates}; conductor{workspace,status,current-task,queue}. **TUZAK:** registry↔gerçeklik (git/PR durumu) drift'i → runtime durumu KAYNAKTAN (git/gh/sentinel) TÜRET, registry'de yalnız config+pointer tut.
5. **Event / Observability şeması** — UI sonra ama şemayı ŞİMDİ tasarla (motor tutarlı yaysın). Devin-modeli: task→fazlar(plan/test/code/review/holdout/ship), her faz event'i (started/progress/diff/log/decision/health/pr/merge/**intervention-needed**). **TUZAK:** sonradan eklemek → tutarsız/eksik event. KARAR: normalize event şeması (jsonl + control kanalı); ilk üretici = sentinel-events; motor fazlarına genişlet.
6. **Merge Governance policy engine** — öğrendiğimizi deklaratif yap: CI-yeşilse low-risk auto-merge; **human-merge ZORUNLU** auth/credential/destructive(DROP)/migration/RBAC; CI-gate (explicit poll). **TUZAK:** hassas değişikliği sessiz auto-merge (migration-auto-ship defect'ini yakaladık). KARAR: per-project + per-tier + **per-path pattern** (`schema.prisma`,`migrations/`,`auth`,`roles.guard`) → merge-mode.
7. **Concurrency + Resource governor** — YÜK DERSİ (load 41; remote-control swap-tükenmesi→hang). KARAR: host başına max-eşzamanlı-Kontaktör, kaynak cap'leri, API/token bütçesi (paylaşımlı), zamanlama/kuyruk, **CI-fırtınası tetikleme** (batch-push'u çalışan run'a denk getirme). **TUZAK:** host'u boğmak → claude -p hang/empty (yaşadık).
8. **Host / runtime modeli** — davinci (always-on) host#1; jenerik = çok-host. Unattended daemon (systemd) per-conductor; PATH/env doğruluğu (claude+pnpm gotcha); reboot'a dayan. **TUZAK:** non-login-shell PATH (yaşadık).
9. **Observability UI (VS Code)** — Faz 3. Webview = event-stream + control-API (pause/resume/intervene/approve-merge). **TUZAK:** şema stabilleşmeden UI. Şimdi sadece seam'i ayır.
10. **Safety / guardrails = platform politikası** — sentinel (liveness/near-done-protect/dry-run-remediation) + CI-gate + holdout-retry + human-merge gate'leri + secret-leak-yok + Rule#9 (bağımsız-doğrula). Ad-hoc'tan platform-enforced'a taşı.

---

## 6. Açık kararlar (inşa oturumunda netleştir)
1. **Motor:** DF'yi jenerik motor olarak TUT + adapter (öneri) vs sıfırdan yeni motor. → DF+adapter (kanıtlı, düşük risk; jeneriklik motor değil platformda).
2. **Platform repo:** `everva/conductor-platform` (bu folder) — provisioner+registry+adapter+(sonra)VS Code eklentisi. Remote kur (private).
3. **İzleme hedefi:** VS Code eklentisi (webview, Devin-vari) — onay? (web UI alternatifi var.)
4. **Dil/stack:** platform kendi neyle yazılsın? (TS/Node — VS Code eklentisiyle aynı ekosistem; veya Python — sentinel Python.) KARAR.
5. **DF patch'lerinin evi:** everva/dark-factory upstream'e mi taşınsın (kalıcı), yoksa platform kendi engine-fork'unu mu tutsun?
6. **Host stratejisi:** davinci-only mi, çok-host soyutlaması mı (en baştan)?

---

## 7. Taşınan disiplin/kısıtlar
- **Orchestrator-only** (ana session kod yazmaz, sub-agent'a delege; planlama/memory istisna).
- **Rule #9** (self-report'a güvenme; kritikleri bağımsız re-run/adversarial-review — bu oturumda gerçek bug yakaladı: chaining-defect, migration-auto-ship, IC2-hang).
- **subscription `claude -p`** (ANTHROPIC_API_KEY YOK).
- **Secret-leak yok** (slack_webhook 0600 gitignored; gh-token credential-helper'da).
- **CI-gate güvenliği** (pending/fail'de merge yok); **human-merge** hassas-değişiklikte; **load-aware** (host'u boğma); **deploy/push'u onaysız yapma** (hassas).

---

## 8. Referanslar (yollar/repolar — inceleme için, DOKUNMA)
- DF engine: `~/.claude/plugins/cache/everva/dark-factory/1.3.3/` (+ davinci kopyası, patch'li).
- Sentinel: `everva/conductor-sentinel` · `~/Documents/GitHub/conductor-sentinel` · davinci `~/conductor-sentinel`.
- optiway canlı Kontaktör: davinci `~/optiway-conductor` (branch `conductor/optiway`, `.dark-factory/` develop'ta). **Çalışıyor — DOKUNMA.**
- Queue clone (FX işleri): davinci `/tmp/optiway-queue`.
- kakbet memory (bu session'ın geçmişi): `~/.claude/projects/-Users-atakan-Documents-GitHub-kakbet/memory/optiway-import-cache-task.md` + `conductor-sentinel-plan.md`. (Yeni oturum kakbet memory'sini OTO-YÜKLEMEZ — gerekirse elle oku.)

---

## 9. İlk oturum çıktısı (hedef)
1. §6 açık kararları kapat (kullanıcıyla).
2. §5 ajandasının her maddesi için kısa karar-notu (`docs/decisions/`).
3. Engine Adapter arayüzünü dondur.
4. Faz-1 iskeleti: Provisioner + Registry + Event-şeması (optiway ile uçtan uca doğrula — mevcut çalışana DOKUNMADAN, yeni bir test-projesi veya read-only).
**Kod YAZMADAN önce kararları yaz. Acele yok.**
