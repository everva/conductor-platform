# ADR-0020 — Control reverse-channel & pause temsili (frozen-StateStore kısıtı altında)

## Bağlam
Gece-otonom P3 entegrasyonunda operatör kontrol döngüsünü tamamladık (P3-3): `conductorctl pause/resume`
**çalışan daemon'ı** gerçekten durdurabilmeli (ADR-0011 §4 control reverse-channel). Daha önce pause yalnız
conductorctl process'inin **in-memory** `MemoryController`'ında tutuluyordu ve `conductor.Tick`'te **hiç
pause-kontrolü yoktu** → ayrı process olan daemon'ı etkilemiyordu.

Kalıcı (PG-paylaşımlı) pause için doğal yer `Project` üzerinde bir durum alanı olurdu. **Ama frozen
StateStore bunu imkânsız kılıyor:** arayüzde `UpdateProject` **yok**; `CreateProject` `ON CONFLICT DO NOTHING`
(yani Project oluşturulduktan sonra **immutable**). Lease tek diğer proje-kapsamlı yazılabilir kayıt ama
TTL-reaper (`reconcile.ReapLeases`) tarafından süpürülüyor ve resource-governor (`ListLeases`) tarafından
eşzamanlılık bütçesi olarak sayılıyor → pause-lease sessizce kaybolur + cap tüketir (yanlış).

## Karar
1. **Pause durumu, frozen StateStore'un mutasyona izin verdiği TEK kalıcı kayıt olan `Task` üzerinden taşınır:**
   proje başına ayrılmış **marker-task** `__conductor.paused__:<projectID>` (status `paused`/`running`).
   `ProjectID` boş bırakılır → gerçek projenin `ListTasks`/status/intake/PickReady çıktısında **görünmez**;
   id öneki operatör id'leriyle (A-1/PRE-0) çakışmaz; id NUL-suz (Postgres `text` PK yasal). Tüm şekil tek
   sahip olan `conductor.StorePauser`'da kapsüllenir (daemon + CLI ortak kullanır).
2. **Conductor honor:** `Tick`'te `GetProject`'ten hemen sonra, `PickReady`/admission/lease/develop/merge'den
   **önce** `Pauser.Paused()` true ise `TickResult{Outcome: OutcomePaused}` (lease yok, iş yok). `nil Pauser`
   = asla-paused (P3-3 öncesi davranış; mevcut testler değişmez).
3. **Kontrol vokabüleri frozen `engine.Command` üzerinde isimlendirilir** (`internal/engine/control.go`):
   `ActionPause/ActionResume/ActionAbort` + `*Command()` kurucular + `ValidAction()`. `CommandEngine.Control`
   string-literal yerine bu sabitleri kullanır. Frozen `Command` tipi değişmez.
4. **Abort ertelendi:** in-flight performer iptali process-group sinyali gerektiriyor; gece-otonom risk altında
   yapılmadı. `ActionAbort`/`AbortCommand()` vokabülerde tanımlı ama uygulanmadı (follow-up).

## Gerekçe
Frozen kontratları (ADR-0002/0010 disiplini) **bozmadan** kalıcı, paylaşımlı, tersine-çevrilebilir,
lease-olmayan pause elde etmenin tek temiz yolu. Tek bir yerde (`StorePauser`) kapsüllendiği için temsil
detayı (marker-task) çağıranlara sızmaz; yarın daha temiz bir temsile geçiş lokal kalır.

## Sonuç / Doğrulama
Gerçek binary'lerle, ayrı process'ler, gerçek Postgres: `pause` → daemon `-once` `outcome=paused` (iş yok) →
`resume` → daemon ilerliyor; `status` marker'ı göstermiyor. Birim: `MarkerInvisibleToLedger`,
`PersistsAcrossFreshReader`, `Tick_Paused_IsCleanNoOp`, `NilPauser_NeverPaused`, control-vocab testleri yeşil.

## Açık bırakılan / öneri (sabah kararı)
- **Öneri:** İleride StateStore'a **toplamsal** `UpdateProject` (veya `SetProjectRunState`) eklenerek (mevcut
  imzaları bozmayan kontrollü bir genişletme) pause, marker-task yerine birinci-sınıf `Project` durumuna
  taşınabilir. Bu "frozen" disiplininin kontrollü gevşetilmesidir; kullanıcı kararı gerektirir.
- **Abort** (in-flight iptal) ayrı follow-up: kalıcı "aborting" sinyali + tick'in ctx ile onurlandırması.
