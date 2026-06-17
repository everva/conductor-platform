# ADR-0016 — Liveness & recovery: somut (ADR-0006'yı operasyonelleştirir)

## Bağlam
Review (K5): ADR-0006 sentinel'i 3 katman olarak KAVRAMSAL anlattı; xirigo'nun `conductor-tick.sh` +
`auto-reconcile.py`'daki SOMUT, yaşanmış-çözülmüş çözümler plana girmemiş. "Sentinel iskelete gömülü, ayrı
servis değil" basitleştirmesi tehlikeli: bağımsız bir backstop olmadan conductor kendi ölümünü kurtaramaz.

## Karar
ADR-0006 Katman-1 (deterministik taban) ve Katman-3 (backstop) şu somut mekanizmalarla uygulanır (xirigo port):
- **Atomik lock:** `mkdir`-based (macOS'te `flock` yok); üst-üste binen tick'ler no-op.
- **Stale-lock steal:** holder PID `kill -0` ile ölüyse lock çalınır (crash sonrası kurtarma — "105-dk stall" dersi).
- **Progress-aware watchdog:** kör `timeout` DEĞİL — claude child-tree + build/test-log **mtime** aktif mi bakar;
  aktif uzun build'i ÖLDÜRMEZ (false-positive); FREEZE_LIMIT (sessizlik, ~30dk) + MAX_TOTAL backstop (~6h,
  ADR-0006 Katman-3).
- **Orphan-sweep:** kill sonrası init'e reparent olan `claude` subtree'yi avla (`pgrep` + parent==1) → **çift-yazan
  orchestrator** felaketini önle.
- **BAĞIMSIZ auto-reconcile:** conductor loop'una GÖMÜLÜ DEĞİL → **ayrı launchd job** (her N dk): dead-tick→kickstart,
  stale-lease→drop (ADR-0010 reaper), commit+review-pass→done reconcile (ADR-0010 Y3). Conductor ölse de yaşar.

ADR-0006 Katman-2 (LLM-danışman gri-bölge) → **Faz-1b** (walking skeleton'da deterministik taban+backstop yeter).

## Gerekçe
Bağımsız backstop = conductor'ın kendi ölümünü kurtarma şartı (xirigo kanıtı). Progress-aware watchdog =
aktif-build'i-öldürme false-positive'inden kaçınır (DF retry-loop muadili). Orphan-sweep = çift-yazan
orchestrator'ı engeller.

## Sonuç
- reconcile ayrı job → ADR-0010 (intent vs gözlemlenen-durum türetme) + ADR-0004 (squash-trailer tespiti) ile bağlı.
- Faz-1a'nın parçası (ilk gece-koşusu bunsuz takılır).

## Durum
✅ Kapandı (K5). LLM-danışman katmanı Faz-1b.
