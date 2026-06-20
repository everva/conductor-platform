# ADR-0034 — Scenario/spec read projection: GET /projects/{id}/scenarios (E2/B2)

## Bağlam
Agent-native session view (redesign E2) bir task'a tıklayınca **SPEC**'i göstermeli: scenario'nun
acceptance kriterleri (+ title/lane/tier/holdout) — director "agent neye tutuluyor"u görür (Augment
Intent'in "living spec"i). Kod incelemesi (plan §0/B2): `taskDTO` yalnız `scenario_id` taşıyor; scenario'nun
title/acceptance'ı HİÇBİR GET endpoint'inde yok — scenario'lar yalnız `POST /intake` / `/distill` ile YAZILIYOR.
Frozen `StateStore`'da `ListScenarios(projectID)` + `GetScenario(id)` zaten VAR; eksik = gateway projeksiyonu.

## Seçenekler
- **(a) `GET /projects/{id}/scenarios`** — mevcut `GET /projects/{id}/tasks` desenini birebir aynalar; web
  task'ın `scenario_id`'siyle filtreler/cache'ler. Mevcut `scenarioDTO` (snake_case) reuse.
- **(b) `GET /scenarios/{id}`** — tekil; ama liste daha az round-trip (board→session geçişinde proje
  scenario'ları bir kerede gelir) ve tasks deseniyle tutarlı.
- **(c) taskDTO'ya scenario'yu göm** — taskDTO'yu şişirir + board (tüm task'lar) gereksiz acceptance taşır.

## Karar
**(a).** `GET /projects/{id}/scenarios` additive read: project yoksa 404; `ListScenarios` → id'ye göre sıralı
→ mevcut `scenarioDTO` (id/title/lane/tier/deps/acceptance/hidden_holdout_ref, snake_case) → JSON array
(nil slice'lar `[]`). Gateway **saf projeksiyon** olarak kalır (ADR-0025); frozen StateStore/EventBus imzaları
dokunulmaz. Web `Scenario` tipi (zaten distill'den var) reuse edilir → tek scenario tipi.

## Sonuçlar
- `cmd/conductor-api/server.go`: route + `handleProjectScenarios` (handleProjectTasks'ı aynalar).
- Testler (`server_test.go`): happy-path (sıralı + acceptance + nil→[]), boş→`[]`, bilinmeyen-proje→404.
- Rule#9: build + vet + `-race` real-PG (conductor-api) + golangci 0 yeşil. Additive read, davranış-değişmez.
- Editör (E2c) session view "Spec/Plan" panelini task.scenario_id → bu endpoint ile doldurur.
