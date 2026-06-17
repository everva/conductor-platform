# ADR-0013 — Faz-1 bölünmesi: 1a (walking skeleton) / 1b (ölçek)

## Bağlam
İki bağımsız review'ın META-bulgusu: Faz-1 scope ters sıralı — altyapı (Postgres, events, intake, scaffolder)
önce, **en riskli varsayım (engine'in gerçek LLM-pipeline mekaniği + LLM çıktı güvenilmezliği) en sona**
bırakılmış. İlk uçtan-uca yeşil haftalarca gecikir; o gelene dek sistemin kalbi test edilmemiş kalır.

## Karar
Faz-1 ikiye bölünür.

**Faz-1a — risk-öldüren walking skeleton (ÖNCE):**
- Elle yazılmış senaryo + reçete (YAML) — intake/scaffolder YOK.
- **Dosya/in-memory StateStore** (Postgres DEĞİL) — `StateStore` arayüzü (ADR-0008/0010) zaten soyut.
- Tek `claude -p` performer (ADR-0014 kontratı) → deterministik verify (lokal test/exit-code, ADR-0003).
- Tek task → per-task worktree (ADR-0017) → squash-merge develop (ADR-0004).
- macOS launchd temeli (ADR-0015) + deterministik liveness/recovery (ADR-0016).
- **Amaç:** ADR-0014 (engine mekaniği + LLM dayanıklılık), ADR-0015 (macOS gerçeği), ADR-0018 (holdout
  izolasyon) EN ÖNCE, gerçek koşuda test edilir.

**Faz-1b — ölçek (SONRA):**
- StateStore impl swap → **merkezi Postgres** (ADR-0010 kararı KORUNUR; sadece devreye-giriş 1b'ye kayar).
- Governor paralel (3-4 worker, repo-başına-1, ADR-0008) + lease reaper (ADR-0010).
- Intake asistanlı damıtma (ADR-0012) + scaffolder per-stack (ADR-0009).
- Events Postgres+LISTEN/NOTIFY + JSON Schema codegen (ADR-0011).
- Bağımsız heartbeat + harici stall-alert (xirigo deseni).

## Gerekçe
Walking skeleton = en riskli varsayımı önce öldürür; altyapı (Postgres/codegen/UI) bekleyebilir. StateStore
soyut olduğu için 1a→1b geçişi temiz, mimari kırılma yok. Kullanıcının Postgres kararı korunur.

## Sonuç
- PHASE-1-PLAN buna göre revize edilir; "bitti" kriteri 1a ve 1b için ayrı.
- 1a tek-host/tek-task olduğundan lease-atomikliği (Postgres'in asıl değeri) henüz kritik değil → dosya yeter.

## Durum
✅ Kapandı (META-bulgu).
