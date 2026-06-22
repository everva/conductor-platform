# ADR-0037 — Native "Conductors" sessions TreeView (native-IDE layout, N2)

## Bağlam
N0 (ADR-0036) editör-alanı Command Center panelini, N1 (ADR-0032) onu startup default-surface'i yaptı —
board artık ana editör alanında. Bu, activity-bar **sidebar webview'ini** (`conductor.fleet`, AYNI cockpit
board'unu render eder) büyük ölçüde **gereksiz** kıldı. Devin Desktop modelinde sol ray bir **native sessions
listesi** ("Your Devins"), bespoke bir web-board değil. Native-IDE planı (`docs/EDITOR-NATIVE-AGENT-IDE-PLAN.md`)
N2'de bunu ister: native **sessions TreeView**.

Kısıt: ADR-0031 (thin-overlay) → workbench'e patch yok, sadece extension API. ADR-0032 B4: "default surface
thin-overlay'de mümkün". Veri host-side okunur (token header'da; webview/provider'a ASLA — token disiplini DONMUŞ).
Gateway READ yüzeyi (server.go, ADR-0025): `GET /projects` + `GET /projects/{id}/tasks`, her ikisi requireAuth.

## Seçenekler
- **(a) Native TreeView'ı sidebar'ın BİRİNCİL view'i yap; webview'i `collapsed` ikincil olarak TUT (coexist).**
  Düşük-risk, geri-alınabilir (N0'ın coexist deseni). Native ray gelir; webview board, kullanıcı gerçek fork
  runtime'ında native rayı doğrulayana kadar çökmeyen bir fallback olarak kalır (collapsed → otomatik resolve/
  bridge YOK, çift-WS yok).
- **(b) Webview'i ŞİMDİ tamamen kaldır + değiştir.** Daha temiz ama tek adımda daha büyük: test'li bir bileşeni
  (FleetViewProvider + placeholderHtml + testleri) kullanıcı runtime'da doğrulamadan söker; dead-code şelalesi.
  Reddedildi (N2'de değil — kullanıcı native rayı onaylayınca temizlik follow-up'ı).
- **(c) Yalnız webview board kal.** Devin-dışı + N1'den sonra ana-alan CC ile çift. Reddedildi.

## Karar
**(a).** N2 şunu sevk eder:
- **`conductor.sessions` native `TreeDataProvider`** ("Conductors"): projeler (Conductor'lar) → task'ları
  (session'lar), her task **status codicon'lu** (lifecycle'ı bir bakışta okunur). Status→codicon eşlemesi
  **board'un status sözlüğüne dayalı** (`web/src/fleet/board.ts` columnFor): running=`play-circle`(blue) /
  awaiting-approval=`git-pull-request`(yellow) / blocked=`warning`(red) / done=`pass-filled`(green) / default=
  `circle-outline`. **UYDURMA YOK** — status vocab gerçek bucketing'den.
- Veri = **host-side authed `FleetReadClient`** (`fleetReadClient.ts`): vscode-FREE, ControlClient'in READ
  kardeşi; `GET /projects` + `/projects/{id}/tasks`; token YALNIZ Authorization header'da (leak-guard test'li);
  herhangi bir hata → boş liste (tree welcome view gösterir), asla throw etmez.
- **Tıkla → editör-alanı Command Center'ı reveal** (`conductor.open`). Webview'i belirli bir session'ın
  detayına **deep-link** etmek = **N3** (session = workspace). N2 tıklama = reveal (dürüst, hafif).
- Yenileme: **bağlantı durumu değişince** (onStateChange) + **manuel** `conductor.refreshSessions` (view-title
  `$(refresh)` butonu + palette) + **viewsWelcome** (boşken "Connect to a gateway").
- Sidebar webview `conductor.fleet` **`visibility: collapsed` ile KALIR** (coexist; collapsed → resolve/bridge
  yok). Tam kaldırma = kullanıcı native rayı gerçek fork runtime'da onayladıktan sonra follow-up.

## Sonuçlar
- **N2 (bu commit, `editor/` only):** yeni `editor/src/fleetReadClient.ts` (+ leak-guard test) + `editor/src/
  sessionsTree.ts` (pure `taskStatusPresentation` + `SessionsTreeProvider` + unit test) + `extension.ts` wiring
  (activate: FleetReadClient + SessionsTreeProvider + `createTreeView` + refresh-on-connect + refresh komutu) +
  `package.json` (`conductor.sessions` view birincil + webview collapsed + viewsWelcome + refresh komutu +
  view/title menu) + mock primitives (EventEmitter/TreeItem/TreeItemCollapsibleState/ThemeIcon/ThemeColor/
  createTreeView). Backend/Go DOKUNULMADI (ADR-0021 frozen-additive). Token disiplini DONMUŞ.
- **Doğrulama (Rule#9 + gerçek runtime):** editör gate (typecheck×2 + eslint-0 + **vitest 196/3** + esbuild)
  yeşil; **`CP_VSCODE_SMOKE=1` electron smoke YEŞİL** (GERÇEK 1.125.1 host: extension aktive, `conductor.sessions`
  view contributed + createTreeView attach throw-suz, `conductor.refreshSessions` register + execute temiz; N0/N1
  hâlâ yeşil). Tree İÇERİĞİ (proje/task render) unit-test'lerle kanıtlı (gateway gerektirir → smoke'ta boş).
- **Bilinen refinement'lar (dürüst):** deep session-linking → N3; canlı WS-tetikli refresh (şu an connect+manuel)
  → sonra; lease-bazlı "running" zenginleştirme (şu an stored-status) → sonra; sidebar webview tam kaldırma →
  kullanıcı-onayı sonrası follow-up.
- **Sıradaki:** **N3** session = workspace (session detail webview + native diff [4C-1] split, resizable groups;
  tree tıklama o session'ı açar) → N4 native-etkileşim → N5 fork layout-default + capstone (KULLANICI ONAYI).
- ADR-0021 / ADR-0025 (gateway READ yüzeyi) / ADR-0031 (thin-overlay) / ADR-0032 / ADR-0036 KORUNUR.
