# Plan — Enhance: gerçek canlı ilerleme (streaming) + idle tespiti

## Amaç
Intake "✨ Geliştir" butonuna basınca STATİK "Talebin kodu incelenerek detaylandırılıyor…" yerine,
claude'un GERÇEK aktivitesini canlı yansıt (Claude Code'daki tool-use akışı gibi):
- "📖 Okunuyor: apps/web/.../shift-table.tsx"
- "🔎 Aranıyor: serviceCompany"
- "🤔 Düşünülüyor…"
- "✍️ Spec yazılıyor…"
+ IDLE tespiti: 15/20/30 sn yeni aktivite yoksa → "⏳ 20 saniyedir işlem yok".

KURAL: GERÇEK veri (claude'un gerçek event'lerinden), uydurma/fake-hızlı DEĞİL. Akış claude'un
kendi hızında; bir sinyal yoksa idle göster (sahte satır akıtma yok).

## Mevcut durum (bunun üstüne ekleniyor — hepsi CANLI)
- Enhance backend+agent CANLI (develop 76d6a73/e2d9d74): POST /enhance → agent claude-read-only
  optiway klonu → Türkçe spec; editor client.enhance create+poll (GET /enhance/{job} 2.5sn).
- Agent: `internal/agent/enhance.go` runEnhanceClaude = `claude -p --dangerously-skip-permissions`
  BLOKLU (cmd.Run, sonunda stdout) — ŞU AN ARA İLERLEME YOK. Burayı stream'e çevireceğiz.
- statestore: EnhanceJob{ID,ProjectID,RoughSpec,Status,Result,Error,CreatedAt} + EnhanceStore seam
  (`internal/statestore/enhance.go`, migration 00010). Progress alanı YOK → ekleyeceğiz.
- Gateway: `cmd/conductor-api/enhance.go` (control POST/GET + agent claim/result).
- Frontend: `web/src/intake/IntakeChat.tsx` runEnhance + `web/src/api/client.ts` enhance(create+poll).
- Editör: live-polling backbone (37ba3c1) CANLI+inject'li; generic rest bridge enhance'i proxy'ler.
- Referans desen: distiller'ın STREAMING runner'ı `internal/intake/distiller_real.go`
  claudeStreamRunnerWithPrompt (stdout satır-tarama + onLine) + `/distill/stream` SSE +
  `web` distillStream onProgress — enhance streaming bunu taklit edebilir.

## Faz-0 — ARAŞTIRMA ✅ DOĞRULANDI (claude 2.1.185, yerel canlı probe, 2 koşu)
Komut: `claude -p --output-format stream-json --verbose --dangerously-skip-permissions` (stdin=prompt).
`--verbose` ZORUNLU (stream-json + print modu). `--dangerously-skip-permissions` ile BİRLİKTE çalışır
(EXIT=0, izin sormadı). NDJSON: her satır bir JSON event.

EVENT ŞEMASI (gözlemlenen sıra: system/init → rate_limit_event → assistant/user çiftleri →
system/thinking_tokens → … → result):
- `{"type":"system","subtype":"init"}` — oturum başlangıcı (yok say).
- `{"type":"rate_limit_event"}` — yok say.
- `{"type":"assistant","message":{"content":[ <block>… ]}}` — asıl sinyal kaynağı. block.type:
  - `tool_use`: `{type,name,input}` — DOĞRULANAN input anahtarları:
    · `Read`  → `input.file_path`            → "📖 Okunuyor: <basename>"
    · `Grep`  → `input.pattern` (+output_mode) → "🔎 Aranıyor: <pattern>"
    · `Glob`  → `input.pattern`               → "🔎 Aranıyor: <pattern>"
    · `Bash`  → `input.command`               → grep/rg/find içeriyorsa "🔎 Aranıyor", yoksa "⚙️"
      (NOT: claude grep'i çoğu kez NATİF Grep yerine `Bash grep -rl …` ile yapıyor — ikisini de
       ele al; probe-1'de Bash grep, probe-2'de zorlanınca natif Grep/Glob kullandı).
  - `text`: `{type:"text",text}` — ara muhakeme → "🤔 Düşünülüyor…".
- `{"type":"user", …}` — tool_result (tool çıktısı geri dönüyor) → progress için YOK SAY.
- `{"type":"system","subtype":"thinking_tokens"}` → "🤔 Düşünülüyor…".
- `{"type":"result","subtype":"success","is_error":false,"result":"<TAM final metin>","num_turns",
  "duration_ms"}` — **KRİTİK: final spec `.result` ALANINDA gelir, stdout birikiminde DEĞİL.**

⇒ KOD ETKİSİ (Faz-1): `runEnhanceClaude` artık stream-json parse edip spec'i `result.result`'tan
almalı (bugünkü "trim'lenmiş stdout döndür" değişiyor — distiller'ın ham-satır stream deseni enhance'e
UYMAZ; distiller düz metin satırı yansıtır, biz yapılandırılmış tool-use event'i istiyoruz).

TRANSPORT KARARI: **(A) enhance_jobs satırına son-progress + progress_at yaz, editor GET poll'unda oku**
— SEÇİLDİ. Mevcut GET /enhance/{job} poll'una (editor 2.5sn, live-polling 5sn) oturur; yeni WS yok.
Throttle: progress yazımı ≥1sn (PG'yi dövmemek + fake-hızlı satır akıtmamak).

## Faz-1 — Agent (stream + parse + report)
- `runEnhanceClaude` → streaming exec: `claude -p --output-format stream-json --verbose
  --dangerously-skip-permissions`, cwd=read-only worktree. StdoutPipe satır-tara (distiller stream
  runner deseni). Her JSON satırını parse → progress sinyali:
  - tool_use Read/Glob → "📖 Okunuyor: <kısa path>"
  - tool_use Grep → "🔎 Aranıyor: <pattern>"
  - assistant text (ara) → "🤔 Düşünülüyor…"
  - result → final spec (done).
- Her sinyali gateway'e raporla (en az ~1sn throttle; fake-hızlı YOK). Final result = spec.
- Token disiplini: path/pattern progress'e girebilir (kod yapısı, gizli değil); claude auth agent'ta
  kalır; token/secret progress'e ASLA girmez.

## Faz-2 — statestore + Gateway (progress taşı)
- statestore: EnhanceJob'a `Progress string` + `ProgressAt time.Time`; `UpdateEnhanceProgress(id,
  detail)` (progress + progress_at=now). Migration 00011: `ALTER TABLE enhance_jobs ADD progress
  text NOT NULL DEFAULT '', ADD progress_at timestamptz` (additive, canlı-PG güvenli).
- agent-API: `POST /projects/{id}/agent/enhance/{job}/progress {detail}` → UpdateEnhanceProgress.
- `GET /enhance/{job}` response'una `progress` + `progress_at` (veya `idle_seconds` türet) ekle.
- agentclient: `ReportEnhanceProgress(projectID, jobID, detail)`.

## Faz-3 — Frontend (canlı satır + idle)
- `client.enhance`: poll'da progress + progress_at oku; `onProgress(detail)` callback (distillStream
  onProgress deseni) ile IntakeChat'e ilet.
- IntakeChat: statik mesaj yerine CANLI progress satırı; her yeni progress'te güncelle. Idle: now -
  progress_at ≥ 15/20/30sn → "⏳ X saniyedir işlem yok" (kademeli). Gerçek hız; sahte akış yok.
- web vitest (fake onProgress → satır güncellenir + idle tier) + editör reuse.

## Faz-4 — Doğrula + deploy (AYNI disiplin)
- Go gate (build+test+vet+golangci+-race) + GERÇEK-PG conformance (migration 00011) + web vitest +
  editör vitest+tsc+lint — HEPSİ yeşil.
- commit+push develop → gateway auto-deploy (00011 goose) + HER İKİ agent rebuild/redeploy
  (darwin-amd64 everva launchctl, linux davinci systemd).
- SELF-TEST: enhance çalıştır, job progress'i curl ile izle → canlı satırların aktığını + idle
  tespitini KENDİM doğrula (fake-green YOK).
- fork inject (`build/inject-extension.sh` → app'e) → kullanıcı relaunch → görsel kanıt.

## Disiplin (değişmez)
verified develop commits; fake-green YOK (progress GERÇEK claude event'lerinden; uydurma satır yok);
additive (frozen StateStore + distiller promptları + KindDiff dokunulmaz; enhance_jobs ALTER
additive); token/secret disiplini (gateway/claude token sohbete/log/git'e ASLA; progress'te yalnız
kod-yapısı path/pattern); no isolation:worktree; optiway main/develop'a dokunma (conductor/optiway
OK); davinci/everva ölmedikçe restart etme (feature deploy'u hariç); Playwright/curl ile KENDİM
doğrula; web cockpit'i ÖNERME (native editör). Faz-S/Faz-1/Faz-2/live-polling ile AYNI standart.
