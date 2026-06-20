# ADR-0033 — Verifier verdict event: per-gate checks in KindDecision (E2/B1)

## Bağlam
Agent-native session view'in (redesign E2) ve platformun **fark yaratıcısının** kalbi: bir karta tıklayınca
"deterministik gate NEDEN geçti/geçmedi" görünmeli — `go build ✓ / go test ✓ / hidden holdout ✓` +
evidence. Bu, rakiplerin LLM-yargıcına karşı Conductor'ın **güvenilir (makineyle kanıtlanmış)** verdict'i.
AMA kod incelemesi (plan §0/B1): conductor `PhaseReview/KindDecision` event'i yalnız `{result}` taşıyor;
verifier'ın döndürdüğü per-gate `[]engine.Check` **atılıyordu** (`review, _, verErr := Verify(...)`,
`conductor.go`). Yani verdict detayı event bus'a / gateway'e / editöre HİÇ ulaşmıyordu.

## Seçenekler
- **(a) KindDecision payload'ına bounded `checks` ekle.** Mevcut `KindDecision` kind'ını reuse → yeni event
  Kind YOK (events.gen.ts codegen dokunulmaz). Payload `map[string]any` = additive (ADR-0021). KindDiff gibi
  sınırlı (PG NOTIFY ~8KB). Tek emit noktası verify'dan sonra → tüm yollar (pass/changes/hold) kapsanır.
- **(b) Yeni `KindVerdict` event'i.** Daha açık ama events taksonomisini büyütür (codegen + frozen-ish) ve
  ekstra plumbing. Gereksiz.
- **(c) Gateway'de session-aggregation read.** Gateway repo/gate görmez; checks event-only. Reddedildi.

## Karar
**(a).** Conductor, verify başarıyla döndükten SONRA tek bir `emitVerdict` çağrısıyla `PhaseReview/
KindDecision` event'i emit eder: `{result, checks:[{name,result,evidence}]}`. **BOUNDED** (checks
`maxVerdictChecks=24` ile capli; evidence zaten verifier'da ilk-satır-capli). `events.Event` ZARFI imza-sabit;
yalnız freeform payload genişler. Session view bir verdict'i `checks` alanının VARLIĞIYLA tanır (retry/approved
KindDecision'lardan ayırt eder). Observability-only: nil-emitter no-op, tick'i ASLA değiştirmez.

## Sonuçlar
- `internal/conductor/conductor.go`: `review, checks, verErr := Verify(...)` (checks artık yakalanıyor) +
  `emitVerdict(...)` (verify sonrası, result-branch'ten ÖNCE → pass/changes-requested/human-hold hepsini kapsar).
- Testler (`verdict_test.go`): pass → 3-check verdict; changes-requested → FAILING check + evidence yüzeyde
  (Rule#9 / "neden bloklandı"). `diff_test.go` green-path sıralaması verdict'i içerecek şekilde güncellendi.
- Rule#9: build + vet (her iki tag) + `-race` real-PG + golangci 0 + e2e suite real-PG yeşil. Frozen-additive.
- Editör (E2c) session view "Verifier verdict" panelini bu event'ten render eder.
