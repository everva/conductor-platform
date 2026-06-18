# ADR-0021 — StateStore kontrollü genişletme: UpdateProject + pause'un Project-durumuna taşınması

## Bağlam
ADR-0020'de pause, frozen StateStore'un Project-mutasyonuna izin vermemesi (no `UpdateProject`,
`CreateProject` = `ON CONFLICT DO NOTHING`) yüzünden kapsüllenmiş bir **marker-task** ile temsil edildi.
Çalışıyor ama dolaylı: pause durumu birinci-sınıf bir kavram değil, sorgulanabilir/observable değil.
Kullanıcı (2026-06-18) **frozen'ın kontrollü gevşetilmesine onay verdi**.

## Karar
1. **"Frozen" disiplini = imza-BOZAN değişiklik yasak; ADDITIVE genişleme serbesttir.** StateStore arayüzüne
   mevcut metot imzalarını bozmayan yeni metotlar eklenebilir (mevcut çağıranlar etkilenmez).
2. **`UpdateProject(ctx, Project) error`** StateStore arayüzüne eklenir (memory + postgres impl). Postgres'te
   gerçek `UPDATE` (ON CONFLICT-insert değil). Bilinmeyen proje → `ErrNotFound`.
3. **Project'e pause/run-state temsili eklenir** (ör. `Paused bool` veya `RunState` alanı) + postgres migration
   (`00003_*`, `projects` tablosuna kolon; `CreateProject` default `running`).
4. **Pause artık Project durumunda.** `conductor.StorePauser` marker-task yerine `UpdateProject`/`GetProject`
   üzerinden çalışır; marker-task temsili kaldırılır. Conductor.Tick pause-check Project durumundan okur.
   `/status` ve events pause durumunu yansıtabilir (birinci-sınıf, observable).
5. **Abort** (ADR-0020 follow-up) ayrı ADR/uygulamada ele alınır; control vokabüleri (engine.Command
   ActionPause/Resume/Abort) korunur.

## Gerekçe
Marker-task kapsüllüydü ama dolaylı ve sorgulanamazdı. Project-durumu birinci-sınıf, atomik (UpdateProject),
gözlemlenebilir ve gelecekteki proje-seviye durum (governance-mode override, vb.) için doğru yer. Additive
genişleme "frozen" amacını (imza kararlılığı) bozmaz; yalnızca yüzeyi büyütür.

## Sonuç
ADR-0020'nin "öneri" kısmı uygulanır; marker-task workaround'ı emekliye ayrılır. Frozen kontratları bundan
sonra **additive** genişleyebilir; imza-bozan değişiklik hâlâ yasak ve ADR gerektirir.
