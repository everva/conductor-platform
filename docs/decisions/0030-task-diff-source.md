# ADR-0030 — Editör task-diff kaynağı: conductor-emitted KindDiff event (4C-0)

## Bağlam
4C-1 (editör-native diff) task branch'inin diff'ini VS Code native diff editor'de göstermeli. AMA: gateway
(`cmd/conductor-api`) yalnız paylaşılan **store/bus**'ı yansıtır — git repo'sunu/worktree'sini GÖRMEZ (ADR-0025).
Diff yalnız **host'taki git state'inde** yaşar (provisioner worktree'leri + branch commit'leri). İnceleme bulguları:
- `events.KindDiff` ("diff") taksonomide **TANIMLI ama hiç EMIT EDİLMİYOR** (rezerve edilmiş kind).
- Daemon'da git-diff üretimi yok (engine develop/merge'de git çalıştırır ama diff yüzeye çıkmaz).
- Olaylar **conductor** (`internal/conductor/conductor.go`, `c.emit(...)`) tarafından emit edilir — KindStarted/
  KindMerge/KindDecision/KindInterventionNeeded lifecycle noktalarında. **engine.go DEĞİL** (engine frozen; conductor değil).

## Seçenekler
- **(a) conductor KindDiff event emit eder** — rezerve kind'ı + mevcut event pipe'ı (bus→gateway→bridge→webview)
  reuse. conductor frozen DEĞİL → additive emit (ADR-0021). Yeni endpoint YOK. Diff büyük olabilir → boyut sınırı gerek.
- **(b) gateway additive diff endpoint** — ama gateway repo görmüyor; daemon'ın diff'i store/bus'a koyması VEYA
  host-tarafı yeni servis gerek. Derin cross-component; yeni yüzey.
- **(c) editör-host yerel git** — extension host `git diff` çalıştırır. Editör+repo CO-LOCATED olmalı + branch yerelde
  erişilebilir olmalı (daemon kendi clone'unda branch açıyor). Remote fork modelini kırar; kırılgan.

## Karar
**(a) — conductor, review/merge noktasında task branch diff'ini hesaplar ve bir `KindDiff` event'i emit eder; diff
SINIRLI (bounded).** Mevcut EventBus → gateway → 4B-2 köprü → webview pipe'ından akar; editör (4C-1) bunu native
diff view'de render eder. **Yeni gateway endpoint YOK; engine.go DOKUNULMAZ** (conductor emit eder, additive).

### Boyut sınırı (kritik)
Diff'ler büyük olabilir; PG `LISTEN/NOTIFY` payload limiti ~8KB, WS frame'leri de şişmemeli. KindDiff payload'u
**bounded**: diff-stat (değişen dosyalar + +/−) + **capped unified diff** (örn. ilk ~N satır/dosya başına) +
`truncated:true` işareti. Bu, insan-onay incelemesinin yaygın durumunu (neyin değiştiğini gör) karşılar. Tam-diff
gerekirse: SONRA additive bir gateway+daemon "full diff" read-endpoint eklenebilir (ADR-0021 additive; salt-okuma) —
ama bounded-diff yetersiz kanıtlanana dek YAPMA (spekülatif değil).

### Payload şekli (öneri, 4C-1'de kesinleşir)
`KindDiff` payload: `{ task, branch, base, files:[{path, status, additions, deletions}], patch:"<capped unified diff>",
truncated:bool }`. Phase = review (insan-gate anı) veya merge-öncesi.

## Etkilenmeyenler / bağımsız işler
- **4C-2 inline komutlar** (approve/abort/pause/resume): mevcut control API'yi (4B-2 köprü / host authed client)
  kullanır — diff-kaynağından BAĞIMSIZ, bu kararı beklemez.
- **4C-3 bildirim/status**: mevcut event stream'deki `intervention-needed`'i kullanır — bağımsız.
Bu yüzden 4C uygulama sırası: 4C-0 (bu karar) → 4C-2 (komutlar, bloksuz) + 4C-3 (bildirim) → 4C-1 (conductor KindDiff
emit + native render; daemon'a additive dokunur).

## Frozen kontratlara etki
HİÇBİRİ. engine.go + statestore arayüzü + EventBus imzaları DOKUNULMAZ. `KindDiff` zaten taksonomide. conductor'ın
yeni emit'i additive davranış (ADR-0021). Gateway/bridge/webview diff'i mevcut event tipi olarak taşır (events.gen.ts
tek-kaynak; Diff zaten Kind union'ında).

## Durum
✅ Karar (4C-0). Diff kaynağı = conductor-emitted bounded `KindDiff` event, mevcut pipe reuse. Sıra: 4C-2 komutlar +
4C-3 bildirim (bloksuz) → 4C-1 native diff (conductor emit + editör render).
