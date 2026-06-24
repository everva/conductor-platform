# Faz-S — auto-holdout: chat → scenario+holdout → review → approve → store → gate runs it (2026-06-24)

Kullanıcı: *"holdout senaryolarını da intake'de yazışarak otomatik oluştur, ben review edip
onaylatıp başlatayım."* + full-green demo. Yani intake sohbeti senaryoyu VE onun gizli holdout
testini üretsin; insan ikisini de review edip onaylasın; onayda holdout store'a yazılsın; agent
geliştirsin; gate o holdout'u koşsun; held → insan merge'e karar.

## Grounding bulgusu (Explore, kanıtlı) — DÜRÜST
- Holdout backend'leri (FSStore store://, PGStore pg://, PrivateRepoStore private:) YALNIZ `Fetch`
  yapıyor — **hiçbir WRITE yolu yok** (`internal/holdout/*`).
- **Gateway-mediated PROD'da holdout HİÇ ÇALIŞMIYOR:** agent `noopHoldout` kullanıyor
  (`internal/agent/executor.go`) → gizli holdout enjekte edilmiyor; `GET /agent/holdout` endpoint'i
  YOK (planlı ama yapılmamış, PROD-GATEWAY-MEDIATED-PLAN §G). → Prod "yeşil" = yalnız public gate'ler,
  gizli holdout DEĞİL. (Faz-S bunu da düzeltir.)
- `holdouts` tablosu VAR (`00005_holdouts.sql`: id,path,content bytea, PK(id,path)) → PGStore prod
  yolu; yeni migration GEREKMEZ.
- Distiller yalnız holdout REF'i öneriyor, test BODY'sini üretmiyor (`distiller_real.go`); fence'ler
  `<<<SCENARIOS..SCENARIOS>>>` / `<<<QUESTIONS..QUESTIONS>>>`.
- verify.Holdout = `{Name string; Files map[string][]byte}` (verify.go); gate enjekte edip komut koşar.

## Tasarım kararı: minimal-ripple
Write-path SOMUT `PGStore` üstünde (frozen `verify.HoldoutStore`=Fetch interface'i DEĞİŞMEZ → FSStore/
Router/noopHoldout/mock'lar dokunulmaz). Gateway PUT(Store)+GET(Fetch) için doğrudan `*holdout.PGStore`
kullanır (kendi pool'undan). Backend = **pg://** (gateway-mediated'e uygun; fs mount yok).

## Artımlar (her biri verified + CI-yeşil + frozen-additive)
- **S1 — PGStore.Store** ✅ hedef: `Store(ctx,id,files)→"pg://holdouts/<id>"` (per-file UPSERT,
  path sanitize). GERÇEK-PG round-trip testi (store→Fetch).
- **S2 — Gateway endpoints:** `PUT /holdouts/{id}` (authed, body {files:{path:base64}} → Store) +
  `GET /agent/holdout?ref=` (authed, Fetch → {files}). Gateway PGStore'u kendi pool'undan kurar
  (DSN varsa; yoksa 501). Go gate + GERÇEK-PG.
- **S3 — Agent gizli holdout'u gateway'den çeker:** `noopHoldout` → `gatewayHoldoutStore`
  (agentclient.GetHoldout → GET /agent/holdout). Artık holdout PROD'da koşar. Agent testi (fake gw).
- **S4 — Distill holdout body üretir:** `<<<HOLDOUT..HOLDOUT>>>` fence + `ParseHoldout` +
  DistillResult `holdout` taşır (opsiyonel; yoksa eski davranış). distillPrompt/clarifyPrompt'a
  opsiyonel blok. Go gate.
- **S5 — Editör review+approve-stores:** Intake önerilen holdout testini gösterir → review →
  Approve: `PUT /holdouts/{id}` + `POST /intake`. Editör vitest + Playwright (KENDİM).

## Güvenlik notu (ADR-0018)
Gizli holdout performer'dan GİZLİ kalmalı. claude hem holdout'u hem kodu üretirse "bağımsızlık"
zayıflar — güvence İNSAN REVIEW'i (kullanıcı kararı). Holdout, develop'tan SONRA izole verify-worktree'ye
enjekte edilir; performer subprocess'i ASLA görmez (yerel modelle aynı). Token/secret disiplini,
holdout içeriği loglanmaz.

## Demo (S1–S5 sonrası): optiway, T3 held → chat→senaryo+holdout otomatik→review→approve→agent→gate
holdout yeşil→held→izle→MERGE ETME→branch temizle.
