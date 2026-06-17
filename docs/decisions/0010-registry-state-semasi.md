# ADR-0010 — Registry / state şeması: merkezi Postgres + repo-audit, kaynaktan-türet

## Bağlam
§5.4: çok-proje tek kaynak. Tuzak: registry ↔ gerçeklik (git/PR) drift'i. İki ayrı kavram:
**Registry** (envanter: projeler, host'lar, capability, readiness, lease) ve **task-ledger** (task durumları,
verdict, audit). ADR-0008 çok-host'ta "repo başına tek aktif lease host-ÜSTÜ olmalı" kritik kuralını koymuştu.

## Karar
**1. Registry + canlı durum + lease = merkezi Postgres.** Baştan Postgres (dosya/SQLite değil).
- Çok-host lease'i atomik çözer (`SELECT … FOR UPDATE` / advisory-lock) → ADR-0008'in host-üstü kuralı baştan sağlanır.
- **Topoloji:** merkezi TEK Postgres + her host'ta ince Go client binary. Host'a Postgres KURULMAZ; tek-binary
  kurulum kolaylığı (ADR-0007) host tarafında korunur. DB tek merkezde (davinci docker / managed).

**2. Task-ledger = HİBRİT (kullanıcı onayı).**
- Canlı durum (running/ready/blocked, lease, current-task) → **Postgres** (hızlı, sorgulanır, repo temiz).
- Kalıcı audit/journal (verdict, kim-ne-yaptı, review notları) → hedef repo'da **`.conductor/journal/`** commit'li
  (şeffaf geçmiş, geliştirici görür). ADR-0009 hibrit-reçete ile tutarlı.
- Reçete → repo'da `.conductor/` (ADR-0009).

**3. Drift önleme = kaynaktan-türet + reconcile.** Postgres yalnız **config + intent + pointer + lease** tutar.
"Task gerçekten merge oldu mu / branch durumu" gibi **runtime gerçeği her tick git/gh'den TÜRETİLİR ve
reconcile edilir** (xirigo `auto-reconcile` deseni). Postgres'te "gerçek durum" cache'lenmez → drift yapısal
imkânsız. (ADR-0003/0006 deterministik ruhu.)

## Şema taslağı (ilk hat, donmadı)
- `projects(id, repo, base_branch, readiness, recipe_pointer, governance_policy)`
- `hosts(id, name, capabilities[])`
- `leases(project_id, host_id, task_id, acquired_at)` — repo-başına-tek-aktif kısıtı host-üstü
- `tasks(id, project_id, lane, status, requires[], deps[], branch, scenario_ref)` — canlı durum
- `scenarios / holdouts` — intake çıktısı (ADR-0005; format ADR-d'de donar)
- Audit/journal → DB'de değil, repo `.conductor/journal/<task>.md`

## Gerekçe
Postgres çok-host'u baştan doğru zemine oturtur; merkezi-DB + ince-client topolojisi Go kolaylığını korur.
Hibrit ledger = hız + şeffaflık. Kaynaktan-türet = drift'i yapısal eler.

## Sonuç
- ADR-0007 güncellenir: merkezi Postgres bağımlılığı (host'a değil, merkeze).
- ADR-0008 güncellenir: Faz-1'den itibaren Postgres; "Faz-2'de merkezi'ye geç" maddesi düşer (baştan merkezi).
- StateStore soyutlaması yine tutulur (test/in-memory için), ama birincil backend Postgres.

## Güncelleme (review: Y3, Y4, Y1, O2, O5)
- **intent vs gözlemlenen (drift netliği):** Postgres'te OTORİTER olan = intent (deps, lease, scenario, config).
  Gözlemlenen runtime (task merge oldu mu, branch var mı) cache'lenmez → her tick git/gh'den TÜRETİLİR
  (ADR-0016 reconcile). `tasks.status` = türetilen/reconcile edilen alan, otoriter değil.
- **lease reaper:** `leases.acquired_at` + TTL/host-heartbeat reaper (ayrı reconcile job, ADR-0016) → crash'te
  repo sonsuz kilitlenmez. Global-cap (≤N) atomik: `INSERT ... WHERE (SELECT count(*) FROM leases) < N` / advisory-lock.
- **`lanes(name, capabilities[])` tablosu** eklendi → lane→capability (ADR-0008 Y1).
- **`tasks.retry_count`** eklendi (ADR-0004 O2).
- **conductorctl ↔ daemon:** Postgres komut tablosu (ADR-0011 control-kanalı) üzerinden (O5).
- **Faz-1a:** bu Postgres katmanı in-memory/dosya `StateStore` ile yer değiştirir (ADR-0013); Postgres = Faz-1b.

## Durum
✅ Kapandı. Şema alanları (DDL) implementasyonda kesinleşir.
