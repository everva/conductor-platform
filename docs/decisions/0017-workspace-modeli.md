# ADR-0017 — Workspace modeli: per-project clone + per-task worktree

## Bağlam
Review (O6) + brief §5.2: workspace klon-modeli kararsızdı (full-clone vs worktree vs shared). 4-5 proje
paralel × disk maliyeti + izolasyon + gh-token + holdout-gizliliği (ADR-0018) düşünülmemişti.

## Karar
- **Per-project full-clone** (bir kez, kullanıcı çalışma klonundan İZOLE — optiway deseni). Brief: dedicated klon.
- **Per-task `git worktree`** (taze `develop`'tan, ADR-0004) — disk-verimli; aynı repoda paralel branch için
  doğru araç (xirigo `pipeline-run` worktree deseni). Repo-başına-1 lease (ADR-0008) olduğundan aynı repoda
  çakışan worktree gerekmez ama task'lar arası izolasyon korunur.
- **Cleanup:** task bitince worktree silinir (retention: blocked task'ların worktree'si teşhis için tutulabilir,
  süreli temizlik).
- **Auth:** `gh auth setup-git` credential-helper (writable). **Deploy-key YASAK** (read-only çıktı — brief dersi).
- **Holdout izolasyonu:** verify için AYRI verify-worktree (ADR-0018) — gizli holdout develop worktree'sine asla yazılmaz.

## Gerekçe
Worktree = full-clone'un disk patlamasından kaçınır + paralel branch'ler için git-doğal. İzole per-project klon
= kullanıcının çalışma kopyasını kirletmez (kanıtlı). gh-token = writable (deploy-key tuzağı yaşandı).

## Sonuç
- provisioner (ADR-0004/0009) bunu uygular.
- Faz-1a: tek proje, tek worktree; Faz-1b: çok proje, governor cap.

## Durum
✅ Kapandı (O6).
