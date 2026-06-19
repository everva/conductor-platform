# ADR-0029 — Webview, paylaşılan cockpit UI'yı cross-dir source-alias ile tüketir (4B-3)

## Bağlam
4B-3 fork webview'inde 3B cockpit panellerini (fleet/event/müdahale/intake-chat) gösterecek. Bunlar web cockpit'i
ile AYNI bileşenler — 4A-2 onları transport-agnostik yapıp `web/src/cockpit.ts` barrel'ında topladı. Webview React
bundle'ının bu barrel'ı tüketmesi gerek. ADR-0028 **fiziksel paylaşım mekanizmasını** (workspace paketi mi, başka mı)
"`editor/` gerçek ihtiyacını tanımlayınca" diye ERTELEMİŞTİ. O an geldi.

## Karar (kullanıcı, 2026-06-19)
**Cross-dir source alias.** `editor/`'ın webview build'i (mevcut esbuild + React eklenir) `web/src/cockpit.ts`'i
**kaynak olarak** bir path-alias üstünden import eder. `web/` toolchain'ine DOKUNULMAZ. Bileşenler tek yerde
(`web/src`) kalır → drift yok, çift bakım yok. **Workspace paketi (ADR-0028'in "doğru" yapısı) ERTELENMEYE devam**
(gerekirse 4D/sonrası).

Reddedilen: monorepo workspace paketi şimdi — ~25 bileşeni taşımak + tüm `web/` import'larını + `events.gen.ts`
codegen yolunu + CI codegen-sync'i + vite/vitest/tsconfig'i + CI root-`npm ci`'yi değiştirmek; YEŞİL `web/`'i bozma
riski yüksek, ön-iş ağır. ADR-0028 ilkesi (spekülatif restructure'dan kaçın) hâlâ geçerli; alias aynı reuse'u
düşük riskle verir.

## Yapı
- `editor/webview/main.tsx` — React entry: `acquireVsCodeApi()` → poster, `window` message dinleyicisi → subscribe;
  4B-2 `createBridgeTransports(poster, subscribe)` → `{ http, events }`; barrel'dan `FleetDashboard`+`ApiClient` import
  edilir; **fork modu** mount: `token=""`, `make*Client = () => new ApiClient({ transport: http })`,
  `eventTransport = events` (4A-2 kontratı: eventTransport varsa enabled:true, token'sız çalışır).
- `editor/tsconfig.webview.json` — DOM lib + `jsx: react-jsx`, strict; `paths` alias (`@cockpit` →
  `../web/src/cockpit.ts`) çözer; `webview/**`'i typecheck eder. (Host tsconfig'i Node/CJS olarak ayrı kalır.)
- `editor/esbuild.config.mjs` — İKİNCİ build: webview entry → `dist/webview/main.js` (+ css), `platform:"browser"`,
  jsx automatic, `alias` `@cockpit` → `../web/src/cockpit.ts`, React **bundle edilir**, `.css` bundle'lanır. Host
  bundle'ı (cjs, vscode external) olduğu gibi.
- `editor/src/extension.ts` `FleetViewProvider` — placeholder yerine bundle'ı yükler: `localResourceRoots =
  dist/webview`, HTML `webview.asWebviewUri(main.js)` + nonce'lu `<script>` + bundle css linki, `<div id="root">`.
  CSP sıkı kalır: `script-src 'nonce-…'`, `style-src 'nonce-…' ${cspSource}`, `connect-src 'none'` (webview KENDİ
  ağını yapamaz — veri yalnız host köprüsünden), `default-src 'none'`.

## Doğrulama (deterministik vs canlı)
- **Deterministik gate** (CI'da): (1) **webview typecheck** — alias çözülür; `FleetDashboard` prop'ları bridge
  transport'larıyla TİP-UYUMLU olmalı (4A-1 `HttpTransport`/`EventTransport` kontratlarının bridge tarafından
  structural implement edildiğinin kanıtı — uyumsuzsa tsc patlar). (2) **webview build** — esbuild React+cockpit'i
  `web/src`'ten alias ile bundle eder; cross-dir paylaşımın uçtan-uca çalıştığının kanıtı. (3) host gate aynen.
- **Canlı kabul** (opt-in/manuel, electron + gerçek gateway): extension yüklenir, connect, paneller canlı veri.
  CP_VSCODE_SMOKE deseni gibi — headless CI'da değil.

## Frozen kontratlara etki
HİÇBİRİ. `web/` dokunulmaz (salt kaynak olarak okunur). engine/statestore/EventBus alakasız. 4A-1 transport
kontratları değişmez — bridge onları structural sağlar.

## Durum
✅ Karar (4B-3). Cross-dir source alias; webview esbuild+React, `web/src/cockpit.ts` reuse; workspace paketi
ertelenmeye devam. Sıra: 4B-3 impl → (4C native) → 4D fork.
