# ADR-0025 — API gateway: ayrı `cmd/conductor-api` servisi (3A-0)

## Bağlam
Faz-3 insan-cockpit'i (web + ileride editör fork) için bir **API gateway** gerekiyor: tarayıcı/fork doğrudan
Postgres'e ya da daemon iç-paketlerine bağlanamaz; aralarında frontend-agnostik bir HTTP/WS yüzeyi şart
(PHASE-3-PLAN, Dalga 3A). İki seçenek: **(A)** gateway'i mevcut conductor daemon'ına gömmek (daemon hem tick
koşar hem API sunar); **(B)** AYRI bir servis (`cmd/conductor-api`) — daemon'lar işi yapar, gateway yalnız
merkezi store/bus üzerinden okur+kontrol eder. Karar (2026-06-18): **(B) ayrı servis.**

## Karar
**API gateway = ayrı `cmd/conductor-api` servisi; read + control; daemon'a doğrudan komut YOK.**
- **Ayrı binary/servis:** `cmd/conductor-api`, kendi `main.go`'su; merkezi Postgres'e `-dsn` ile bağlanır
  (daemon'la aynı DSN), `statestore.NewPostgresStore` + `events.NewPostgresBus` (boş DSN = in-memory, test/dev).
  Daemon (`cmd/conductor`) DEĞİŞMEZ — frozen tick-runner; gateway onun yanında, ayrı süreç/pod.
- **Veri yolu = paylaşılan store/bus (tek doğruluk kaynağı):** gateway HİÇBİR yeni state tutmaz. Okuma
  doğrudan `statestore.StateStore` (ListProjects/ListTasks/ListHosts/GetLease/…) ve `events.EventBus`
  (Subscribe, PG LISTEN/NOTIFY) üzerinden; kontrol mevcut conductor seam'lerini REUSE eder:
  onboard→`store.CreateProject`, intake→`intake.IntakeFile`, pause/resume→`conductor.StorePauser`,
  abort→`conductor.StoreAborter`, approve→`conductor.StoreApprover`. **Yeni iş mantığı yok — daemon ile birebir
  aynı deterministik aksiyonlar** (conductorctl'in HTTP'ye taşınmış hali).
- **Daemon'a doğrudan-komut YOK:** gateway daemon'a RPC/ssh/sinyal göndermez. Kontrol, store'a durum yazıp
  (Project.Paused / Task.AbortRequested / Task.Approved) daemon'ın bir SONRAKİ tick'inde okumasıyla olur —
  conductorctl'in zaten kullandığı cross-process desen (ADR-0011 §4). Bu, gateway↔daemon arası SPOF/coupling'i
  ortadan kaldırır: gateway düşse daemon'lar çalışmaya devam eder; daemon düşse gateway read-only kalır.
- **Yüzey:** REST (read: `GET /projects`,`/projects/{id}/tasks`,`/hosts`,`/status`,`/events`; control: `POST`
  onboard/intake/pause/resume/abort/approve) + WebSocket (`/ws` — bus aboneliği, tarayıcıya tipli event push).
- **`POST /projects/{id}/distill` (intake-chat yardımcısı, 3B-4a; review F8):** yazışma metnini sunucu-tarafı
  `intake.Distiller` (`claude -p`) ile ÖNERİLEN senaryolara damıtır ve intake-hazır YAML döndürür. **Karar mercii
  DEĞİL, taslak yardımcısı:** HİÇBİR ŞEY persist etmez; insan YAML'i gözden geçirip onaylayarak mevcut `/intake`'e
  POST'lar (orada deterministik intake validasyonu çalışır). LLM yalnız danışman (çekirdek prensiple tutarlı);
  hata→ErrNoScenarios/ErrMalformed→422 (asla uydurmaz). Distiller env'i `envsafe.Sanitize`'dan geçer (F7); gateway
  pod'unda `claude -p` için yazılabilir HOME emptyDir mount'lanır (F6).
  Tipli JSON; event tipleri N-9 codegen'den (events `types.gen.ts`) türeyen TS ile hizalı.
- **Auth:** bearer-token (Authorization: Bearer …); token secret'ten (`CONDUCTOR_API_TOKEN`/k8s Secret) gelir,
  sabit-zaman karşılaştırma; `/healthz`/`/readyz` hariç tüm endpoint'ler token ister. mTLS/OIDC sonraya
  ertelendi (önce çalışan tek-token; ADR-0026'da session detayı). DSN/token asla response/log'da geçmez
  (httpserver.go §secret-free deseni).
- **Frontend-agnostik kontrat (fork köprüsü):** API hiçbir React/web varsayımı yapmaz — saf HTTP/WS+JSON.
  Web cockpit (3B) ve ileride editör fork (Faz-4) AYNI gateway'i kullanır; gateway kalıcı, frontend takılıp
  çıkarılabilir. Bu yüzden tüm tipler N-9 tek-kaynak şemadan türer (drift yok).

## Gerekçe
- **Sorumluluk ayrımı + frozen daemon:** daemon'ı (en kritik, en çok-test edilmiş süreç) bir HTTP sunucusuyla
  şişirmek risk; ADR-0024 "daemon iş yapar" rolünü bozar. Ayrı servis = daemon dokunulmaz kalır, gateway
  bağımsız ölçeklenir/deploy edilir/yeniden başlar.
- **Tek doğruluk kaynağı korunur:** gateway state tutmadığı, yalnız paylaşılan store/bus'a yansıdığı için
  conductorctl ile birebir tutarlı; "ikinci truth" drift'i imkânsız. Kontrol seam'leri zaten cross-process
  kanıtlı (pause/abort/approve -dsn ile çalışıyor).
- **Köprü değeri:** ayrı, frontend-agnostik gateway Faz-4 fork tarafından OLDUĞU GİBİ reuse edilir; web kabuğu
  atılsa bile gateway+tipli client kalır (PHASE-3-PLAN köprü gerekçesi).
- **Güvenlik:** read-mostly + dar control yüzeyi; daemon'a yazma yetkisi yok (yalnız store-durumu); token-auth +
  secret-free response. Saldırı yüzeyi conductorctl'in yapabildikleriyle sınırlı.

## Sonuç — Dalga 3A kapsamı (bu kararla netleşti)
- **3A-1 Read API (REST):** `GET /projects`, `/projects/{id}/tasks`, `/hosts` (capabilities+heartbeat-yaşı),
  `/status`, `/events?project=&since=`; bearer-auth; tipli JSON; store'dan doğru veri (deterministik + skip-gated real-PG).
- **3A-2 Live events (WS):** `/ws` → `events.EventBus.Subscribe` (PG LISTEN/NOTIFY) → tarayıcıya tipli push;
  `Filter` ile proje/intervention filtresi. Kabul: docker-PG'de publish → WS client alır (bağımsız doğrula).
- **3A-3 Control API (REST POST):** onboard/intake/pause/resume/abort/approve; conductor seam reuse; auth-gated;
  store'u doğru değiştirir; T3 approve→merge canlı (gateway üzerinden, daemon'ın sonraki tick'i).
- **3A-4 Paketleme:** Dockerfile (kök Dockerfile multi-binary deseni) + k8s (Deployment+Service+Ingress,
  deploy/k8s P4 deseni; non-root; secret=token+DSN). Kabul: docker build + kustomize/kubectl dry-run valid.

## Frozen kontratlara etki
HİÇBİRİ değişmez. Gateway tümüyle MEVCUT seam'leri TÜKETİR: `statestore.StateStore` (okuma + UpdateProject/
UpdateTask additive yüzeyleri), `events.EventBus`, `conductor.Store{Pauser,Aborter,Approver}`, `intake.IntakeFile`.
engine.go + statestore arayüz imzaları DOKUNULMAZ (ADR-0021 additive ilkesi; burada ek bile yok, salt tüketim).

## Durum
✅ Karar verildi (3A-0). Ayrı `cmd/conductor-api`; daemon'a doğrudan-komut REDDEDİLDİ (store-yansıması). 3A-1 başlıyor.
