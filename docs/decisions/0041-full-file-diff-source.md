# ADR-0041 — Full-file diff source: gate-time full-context patch persisted to PG + fetch endpoint (Faz-P / P2b)

## Bağlam
P2a (ADR-0040) native `vscode.diff`'i unified `patch`'ten per-file reconstruct ederek getirdi —
**backend'siz**, ama yalnız hunk+context (KindDiff event'i NOTIFY için ~8KB bounded). Kullanıcı
**P2b = full-file diff** istedi (değişmemiş bölgeler dahil, git/Claude-Code tam-dosya görünümü).
Bunun için VIEW-TIME'da her değişen dosyanın TAM base+modified içeriği gerekir.

**Mimari kısıt (kod'a-dayalı):** `cmd/conductor-api` (gateway: editör'ün konuştuğu API, PG var,
**repo YOK**) ≠ `cmd/conductor` (worker: repo/worktree). Worktree gate sonrası SİLİNİR
(`conductor.go` Cleanup defer) — ama per-task **branch ref SURVIVES** (`approve.go:24` /
`conductor.go:938`), yalnız WORKER'ın repo'sunda. Gateway repo'ya erişemez ⇒ full content'i
view-time'da re-derive EDEMEZ. Frozen-additive (ADR-0021): backend kontrat değişmez, additive.

## Seçenekler
- **(a) Gate-time full-context patch persist + fetch endpoint.** Worktree CANLIYKEN (gate'te)
  worker `git diff --unified=<huge>` ile tam-dosya bağlamlı patch üretir, PG'ye OUT-OF-BAND
  yazar (NOTIFY event'i DEĞİL); editör host-side fetch'ler + P2a reconstruct'ı reuse ederek
  full-file before/after kurar. Miss'te (404/no-token) P2a bounded patch'e graceful fallback. [seçildi]
- **(b) Full content'i KindDiff event'ine koy.** NOTIFY ~8KB bound'u keser; bounded'lığın TÜM amacı
  bu. Reddedildi.
- **(c) View-time'da re-derive.** Gateway'in repo'su yok; worker'a gateway→worker RPC eklemek
  persistence'tan daha karmaşık + worker bir API değil. Reddedildi.
- **(d) Frozen StateStore'a metot ekle.** Arayüz "FROZEN"; tüm implementer/fake'leri kırar.
  Reddedildi → bunun yerine AYRI dar seam (aşağıda).

## Karar
**(a), 4 katman, hepsi ADDITIVE:**
- **statestore (`taskdiff.go` + migration `00008_task_diffs.sql`):** yeni `TaskDiff` tipi + **AYRI
  dar `TaskDiffStore` arayüzü** (`PutTaskDiff`/`GetTaskDiff`) — frozen `StateStore` DOKUNULMAZ, hiçbir
  fake kırılmaz; iki store (PG upsert ON CONFLICT + memory) implement eder, conductor+gateway
  TYPE-ASSERT eder. `task_diffs` tablosu (project_id, task_id PK, base, branch, patch, truncated,
  updated_at); task başına bir satır, gate'te upsert.
- **worker (`differ.go` + `conductor.go`):** `GitDiffer.FullPatch` (`git diff --unified=1000000
  <base>...HEAD`, `maxFullPatchBytes=1MiB` line-boundary cap) + opsiyonel **`FullDiffer` seam** +
  `emitDiff` sonrası **`persistFullDiff`** (best-effort: Differ ALSO FullDiffer && store ALSO
  TaskDiffStore ise persist; hata/yoksa no-op). OBSERVABILITY-ONLY — tick'i ASLA değiştirmez/durdurmaz.
- **gateway (`server.go`):** `GET /projects/{id}/tasks/{task}/diff` (requireAuth) → `taskDiffDTO`;
  404 yoksa (editör bounded'a düşer), 501 store TaskDiffStore değilse. Saf store projeksiyonu, repo görmez.
- **editör (`diffContentClient.ts` + `extension.ts`):** host-side authed `DiffContentClient.fetchFullDiff`
  (token YALNIZ header; miss→undefined, throw-suz; leak-guard test). `openStoredDiff` açılışta fetch
  eder → başarıda `DiffStore.upgradeFiles(n, fullPatch)` ile entry'nin per-file reconstruct'ını TAM
  dosyaya YÜKSELTİR (content provider artık tam dosya servis eder); miss'te bounded P2a görünümü kalır.
  `fetchFull` OPSİYONEL param (runShowDiff/ForTask/Beside + registerConductor) ⇒ P2a testleri DEĞİŞMEZ.

## Sonuçlar
- **P2b (bu commit):** statestore + migration + worker + gateway + editör; hepsi additive. **Frozen
  StateStore + KindDiff event + NOTIFY bound DEĞİŞMEDİ.** Token DONMUŞ (side URI'leri + fetch header).
- **Doğrulama (TAM disiplin):** Go gate (build + vet + **golangci-lint 0** + `go test -race`) +
  **GERÇEK-PG conformance** (docker PG: migration 00008 uygulandı + `TaskDiffStoreRoundTrip` memory+PG
  yeşil) + conductor `fulldiff_test` (persist / no-FullDiffer-fallback / error-swallow) + gateway
  `taskdiff_test` (200/404/401/501) + **editör gate** (typecheck×2 + eslint-0 + **vitest 238/3**:
  7 diffContentClient[leak-guard+fallback] + 2 flow[full-upgrade+graceful-fallback] + esbuild) +
  **electron smoke YEŞİL** (VS Code 1.125.1, exit 0).
- **MIGRATION = CANLI conductor-PG'ye uygulanır → KULLANICI ops adımı** (ArgoCD/goose deploy'da).
  BEN canlı-PG'ye DOKUNMADIM; test docker-PG'de doğruladım. Editör miss'te bounded'a düştüğünden,
  deploy'dan ÖNCE de güvenli (endpoint 404 → P2a görünümü).
- **GÖRSEL capstone (kullanıcı host'u):** deploy+migration sonrası gerçek task diff'inde tam-dosya
  native yan-yana + ONAY.
- ADR-0021 / ADR-0030 (KindDiff bounded) / ADR-0040 (P2a reconstruct, reuse edilir) KORUNUR.
