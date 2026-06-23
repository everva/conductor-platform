# ADR-0050 — Gateway lease, task'ı "running"a çevirir (status tutarlılığı)

## Bağlam
Kullanıcı (2026-06-23) editörde bir tutarsızlık bildirdi: soldaki **sessions-tree**'de bir görev
`todo` görünürken, **board**'da AYNI görev `Running` kolonunda. İki yüzey aynı gateway'i okuyor ama
farklı alanlara bakıyor:

- **board** (`web/src/fleet/board.ts` `columnFor`): AKTİF LEASE'i canlı "çalışıyor" sinyali olarak
  kullanır — lease varsa görev `running`, `task.status`'tan BAĞIMSIZ. (Lease, asıl çalışma sinyali;
  store'daki "running" penceresi kısa.)
- **sessions-tree** (`editor/src/sessionsTree.ts`): `task.status`'u gösterir (gateway `/tasks`).

Kök neden: gateway'in agent-API'sinde `handleAgentLease` lease aldıktan sonra `task.Status`'u
`todo/ready` → `running` **FLİP ETMİYORDU**. Lease canlıyken `/tasks` hâlâ `todo` dönüyor → tree
`todo`, board `running`. (Daemon'un kendi tick'i registry geçişiyle status'u ilerletir; eksik olan
yalnız GATEWAY-mediated yoldu — davinci/prod'un kullandığı yol.)

## Karar
**Lease, görevin yaşam-döngü status'unu da ilerletsin (ADDITIVE):**

- `handleAgentLease`: `AcquireLease` başarılı olunca leased görev `Status="running"` yazılır
  (`updateTask`). Lease zaten host'u kaydeder; burada yalnız status ilerletilir (Task'ta `HostID`
  alanı yok — lease host kaydıdır).
- `handleAgentReleaseLease`: SAHİBİ, terminal verdict OLMADAN bırakırsa (çökme / merge çakışması /
  terk) görev hâlâ `running` ise `ready`'ye geri çevrilir → tekrar koşulabilir; aksi halde sahipsiz
  bir "running" olarak takılı kalırdı. result/merge yolunun zaten taşıdığı `blocked` /
  `awaiting-approval` / `done` EZİLMEZ. SAHİP-OLMAYAN (idempotent no-op) release ise status'a
  DOKUNMAZ (başka host hâlâ koşuyor olabilir) — ownership release ÖNCESİ yakalanır.

Bu, frozen-additive (ADR-0021): yeni alan/imza yok, yalnız var olan store yazımlarının lease/release
anlarına eklenmesi. Editör tarafı DEĞİŞMEDİ — sessions-tree zaten `/tasks` status'unu sadık render
ediyordu; yalan söyleyen gateway'di.

## Doğrulama
- `cmd/conductor-api/agent_test.go`: lease→status running (yanıt + store); sahip-release→ready;
  sahip-olmayan-release running'i korur; blocked verdict release'te ezilmez.
- `cmd/conductor-api/onboard_pg_test.go` `TestAgentLeaseStatusFlipPG`: GERÇEK-PG round-trip — lease →
  `/tasks` running → sahip-release → ready.
- `editor/src/sessionsTree.test.ts`: `taskStatusPresentation("running")` zaten kapsıyor (zincir:
  gateway flip → `/tasks` running → tree running + board running AYNI).
- Opt-in `web/e2e-realgw/`: gerçek gateway'e karşı board `Running` + `/tasks` running (mock'suz).
