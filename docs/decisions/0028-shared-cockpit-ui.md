# ADR-0028 — Paylaşılabilir cockpit UI: açık barrel + transport injection (4A-2)

## Bağlam
ADR-0027 fork yönünü sabitledi: 3B React bileşenleri (fleet / events / interventions / intake-chat) hem web
cockpit'i hem de (4B) fork webview'i tarafından **transport-agnostik** olarak reuse edilecek. 4A-1 alt-katmanda
(`ApiClient`/`useEventStream`) transport seam'ini açtı (`HttpTransport`/`EventTransport`). 4A-2 bu seam'i **bileşen
ağacının yukarısına** taşıyıp paylaşılabilir yüzeyi netleştirmeli.

İki açık soru vardı: (1) **paylaşım mekanizması** — fiziksel workspace paketi mi, yoksa repo-içi açık modül mü?
(2) bileşenler şu an **token-merkezli** (`token: string` alıp default fetch/WS transport'u içeride kuruyor; ayrıca
`token.length===0`'ı "oturum hazır değil" kapısı olarak kullanıyor) — fork token TUTMAYACAĞINA göre (ADR-0027:
"token webview'e GİRMEZ") bileşenler token'sız nasıl mount edilecek?

## Karar
**1) Mekanizma = açık public barrel (`web/src/cockpit.ts`), şimdilik `web/` içinde. Fiziksel workspace paketi
ERTELENDİ (4B/4D — `editor/` gerçek ihtiyacını tanımlayınca).** Sebep: bileşenler audit'te zaten paylaşılabilir
çıktı (fleet/events/intake'de `auth/session` veya sert web-global bağımlılığı YOK; tek web-coupling default-transport
yardımcılarında: `defaultBaseUrl` `import.meta.env`, `deriveWsBase` `window.location` — ikisi de injected-transport
yolunda baypas edilir). Tüketici (`editor/`) henüz YOK; onun kısıtlarını bilmeden monorepo'ya bölmek **spekülatif
genelleme** olur + CI/Go-codegen (`events.gen.ts` yol kontrolü) churn'ü getirir. Barrel, "neyin paylaşıldığını" net
kılar, web onu **ilk tüketici** olarak kullanır (editor/ 4B'de ikinci tüketici), fiziksel ayrım gerekirse 4B/4D'de
trivial olur.

**2) Auth = transport'un işi; readiness = host'un işi (token'dan ayrıştırıldı).** Bileşenler `eventTransport?:
EventTransport` (ve mevcut `make*Client` REST factory'leri) ile inject alır. Readiness için hook'larda `enabled`
default'u `token.length > 0` yapıldı ve iç kapı `!enabled`'a indirildi (DAVRANIŞ KORUNUR: token boşken default
enabled=false, doluyken true — eski `!enabled || token.length===0` ile aynı). Böylece fork `enabled: true` + boş/
yok token + injected transport ile **token'sız** mount eder; web değişmeden token-modunda kalır.

### Barrel yüzeyi (`web/src/cockpit.ts`)
- Mount: `FleetDashboard` (+ props) — bir host'un mount ettiği tek cockpit bileşeni (fleet/events/intake sekmeleri).
- REST seam: `ApiClient`, `ApiError`, `DistillNoScenariosError`, `HttpTransport`/`HttpRequest`/`HttpResponse`,
  `FetchTransport`, `defaultBaseUrl`.
- Event seam: `EventTransport`/`EventSubscription`, `WebSocketTransport`, `useEventStream`, `wsUrl`.
- Domain tipleri: `Project`/`Task`/`Host`/`Event`/`EventQuery`/… (`types.ts` reexport). `events.gen.ts` TEK-KAYNAK
  kalır (api seam üzerinden tüketilir; kopyalanmaz).

### İki mount modu
- **Web (token modu):** `<FleetDashboard token={realToken} .../>` — default `FetchTransport`+`WebSocketTransport`
  token'dan kurulur. Bugünkü davranış, byte-for-byte.
- **Fork (transport modu, 4B):** host `eventTransport` (postMessage-köprü) + transport-bağlı `make*Client`
  factory'leri verir, `token=""`; bileşenler token-agnostik çalışır, auth host'ta (SecretStorage), token webview'e
  GİRMEZ.

## Sınır (rot'a karşı kilit)
ESLint `no-restricted-imports`: paylaşılan modüller (`src/fleet/**`, `src/events/**`, `src/intake/**`, `src/api/**`)
web-bootstrap'i (`**/auth/**`, `main`) import EDEMEZ. (Şu an etmiyorlar; kural sınırı dondurur.) Default-transport
yardımcıları (`import.meta.env`/`window.location`) yalnız `FetchTransport`/`WebSocketTransport` içinde kalır.

## Frozen kontratlara etki
HİÇBİRİ. engine.go + statestore + EventBus dokunulmaz. Değişiklik yalnız `web/`; hepsi ADDITIVE (yeni opsiyonel
`eventTransport`; `enabled` default'u davranış-koruyarak türetildi). Mevcut web testleri DEĞİŞMEDEN geçer.

## Durum
✅ Karar (4A-2). Açık barrel + transport injection threaded; readiness token'dan ayrı; sınır eslint ile kilitli;
fiziksel workspace paketi `editor/` (4B) gelene dek ertelendi. Sıra: 4A-2 implementasyonu → 4B.
